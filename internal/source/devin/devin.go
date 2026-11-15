// Package devin reads the Devin CLI's SQLite database (Agent Speed
// spec/METRICS.md §4.17, MIT): every assistant row of message_nodes holds
// the usage, total time and time to first token of one model call. Only
// usage metadata is read, never message content, titles or credentials.
package devin

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Devin is the source id.
const Devin usage.Source = "devin"

func init() {
	source.Register(source.Spec{
		Source: Devin,
		Label:  "Devin",
		Color:  "#d946ef",
		Detect: func(string) bool {
			for _, p := range defaultDatabases() {
				if isRegular(p) {
					return true
				}
			}
			return false
		},
		Poller: func() source.Poller { return &poller{} },
		Every:  2 * time.Second,
	})
}

const (
	maxJSON    = 32 << 20
	windowSlop = 15 * 60_000
)

// bounded is the column when it is text of at most 32 MiB, else NULL.
func bounded(col string) string {
	return "CASE WHEN typeof(" + col + ") = 'text' AND length(CAST(" + col + " AS BLOB)) <= " +
		strconv.Itoa(maxJSON) + " THEN " + col + " END"
}

type poller struct {
	paths func() []string // set by tests; else the automatic database

	dbs map[string]*database
}

func (p *poller) databases() []string {
	if p.paths != nil {
		return p.paths()
	}
	return defaultDatabases()
}

type agent struct {
	id        string
	cwd       string
	project   string
	sessModel string
	callModel string
	phase     usage.Phase
	since     int64
	hasSince  bool
	inFlight  int64
	last      int64
}

func (a *agent) model() string {
	if a.callModel != "" {
		return a.callModel
	}
	return a.sessModel
}

func (a *agent) setPhase(ph usage.Phase, ms int64) {
	if a.hasSince && ms < a.since {
		return
	}
	a.phase, a.since, a.hasSince = ph, ms, true
	a.inFlight = 0
	if ph == usage.Generating {
		a.inFlight = ms
	}
}

type database struct {
	path      string
	db        *sql.DB
	immutable bool
	fi        os.FileInfo
	supported bool
	hw        int64 // highest row_id read; -1 before the first read
	agents    map[string]*agent
}

func (d *database) close() {
	if d.db != nil {
		d.db.Close()
		d.db = nil
	}
}

func columns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('`+table+`')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

func (d *database) ensure(ctx context.Context) (bool, error) {
	lst, err := os.Lstat(d.path)
	if err != nil || !lst.Mode().IsRegular() {
		d.close()
		d.fi = nil
		return false, nil
	}
	reset := func() { d.hw, d.agents = -1, map[string]*agent{} }
	if d.agents == nil {
		reset()
	}
	if d.db != nil {
		switch {
		case d.fi != nil && (!os.SameFile(d.fi, lst) || lst.Size() < d.fi.Size()):
			d.close()
			reset()
		case d.immutable:
			if _, werr := os.Stat(d.path + "-wal"); werr == nil || lst.Size() != d.fi.Size() || !lst.ModTime().Equal(d.fi.ModTime()) {
				d.close()
			}
		}
	}
	if d.db == nil {
		db, imm, err := sqlitedb.Open(d.path)
		if err != nil {
			return false, nil // locked, not a database: try again next time
		}
		d.db, d.immutable, d.fi = db, imm, lst
		ses, err1 := columns(ctx, db, "sessions")
		msg, err2 := columns(ctx, db, "message_nodes")
		d.supported = err1 == nil && err2 == nil
		for _, c := range []string{"id", "working_directory", "model"} {
			d.supported = d.supported && ses[c]
		}
		for _, c := range []string{"row_id", "session_id", "chat_message", "created_at"} {
			d.supported = d.supported && msg[c]
		}
	}
	return d.supported, nil
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, dd *source.Dedup, sink source.Sink) error {
	if p.dbs == nil {
		p.dbs = map[string]*database{}
	}
	var live []*database
	seen := map[string]bool{}
	var firstErr error
	for _, path := range p.databases() {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			real = path
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		d := p.dbs[path]
		if d == nil {
			d = &database{path: path, hw: -1}
			p.dbs[path] = d
		}
		ok, err := d.ensure(ctx)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if !ok {
			continue
		}
		if err := d.read(ctx, since.UnixMilli()-windowSlop, live, dd, sink); err != nil {
			d.close()
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		live = append(live, d)
	}
	return firstErr
}

type row struct {
	rowid   int64
	sid     string
	created any
	msg     sql.NullString
}

// rowTime is created_at × 1000 truncated: an integer or a finite real.
func rowTime(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		if t > math.MaxInt64/1000 {
			return math.MaxInt64, true
		}
		if t < math.MinInt64/1000 {
			return math.MinInt64, true
		}
		return t * 1000, true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return truncI64(t * 1000), true
	}
	return 0, false
}

// truncI64 truncates toward zero, saturating to the signed 64-bit range.
func truncI64(f float64) int64 {
	switch {
	case math.IsNaN(f):
		return 0
	case f >= 9.223372036854775807e18:
		return math.MaxInt64
	case f <= -9.223372036854775808e18:
		return math.MinInt64
	}
	return int64(f)
}

func (d *database) query(ctx context.Context, where, order string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, `SELECT row_id, session_id, created_at, `+bounded("chat_message")+
		` FROM message_nodes `+where+` ORDER BY row_id `+order, args...)
}

func scanRow(rows *sql.Rows) (row, error) {
	var r row
	var sid sql.NullString
	err := rows.Scan(&r.rowid, &sid, &r.created, &r.msg)
	r.sid = sid.String
	if !sid.Valid {
		r.sid = "\x00"
	}
	return r, err
}

func (d *database) read(ctx context.Context, floor int64, earlier []*database, dd *source.Dedup, sink source.Sink) error {
	var maxRow sql.NullInt64
	if err := d.db.QueryRowContext(ctx, `SELECT MAX(row_id) FROM message_nodes`).Scan(&maxRow); err != nil {
		return err
	}
	if d.hw >= 0 && maxRow.Int64 < d.hw {
		d.agents, d.hw = map[string]*agent{}, -1 // rows went away: read again
	}
	var rows []row
	if d.hw < 0 {
		rs, err := d.query(ctx, "", "DESC")
		if err != nil {
			return err
		}
		for rs.Next() {
			r, err := scanRow(rs)
			if err != nil {
				rs.Close()
				return err
			}
			// rows are in time order; a row without a time neither ends the read nor is left out
			if ms, ok := rowTime(r.created); ok && ms < floor {
				break
			}
			rows = append(rows, r)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return err
		}
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	} else if maxRow.Int64 > d.hw {
		rs, err := d.query(ctx, "WHERE row_id > ?", "", d.hw)
		if err != nil {
			return err
		}
		for rs.Next() {
			r, err := scanRow(rs)
			if err != nil {
				rs.Close()
				return err
			}
			rows = append(rows, r)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return err
		}
	}
	d.hw = max(d.hw, maxRow.Int64)

	touched := map[string]bool{}
	for _, r := range rows {
		ag, err := d.agentFor(ctx, r.sid, earlier)
		if err != nil {
			return err
		}
		if ag == nil {
			continue // a session the sessions table does not list
		}
		touched[r.sid] = true
		ms, hasMs := rowTime(r.created)
		if hasMs {
			ag.last = max(ag.last, ms)
		}
		if !hasMs || !r.msg.Valid || !gjson.Valid(r.msg.String) {
			continue // malformed
		}
		obj := gjson.Parse(r.msg.String)
		if !obj.IsObject() {
			continue
		}
		role := obj.Get("role")
		if role.Type != gjson.String {
			continue
		}
		switch role.Str {
		case "assistant":
			if resp, ok := d.call(obj, r, ag, ms); ok {
				if mine, _ := dd.Claim(resp.Key, d.path); mine {
					sink.Response(resp)
					if resp.Model != "" {
						ag.callModel = resp.Model
					}
				}
			}
			tc := obj.Get("metadata.finish_reason")
			if a := obj.Get("tool_calls"); (a.IsArray() && len(a.Array()) > 0) || (tc.Type == gjson.String && tc.Str == "tool_calls") {
				ag.setPhase(usage.Tools, ms)
			} else {
				ag.setPhase(usage.Idle, ms)
			}
		case "user", "tool":
			ag.setPhase(usage.Generating, ms)
		}
	}
	for sid := range touched {
		ag := d.agents[sid]
		sink.Agent(usage.AgentState{
			Agent: ag.id, Source: Devin, Project: ag.project, Model: ag.model(),
			Phase: ag.phase, InFlightMs: ag.inFlight, LastLineMs: ag.last,
		})
	}
	return nil
}

// call is the response of an assistant row whose metadata.metrics is an
// object, none when its input and output are both 0.
func (d *database) call(obj gjson.Result, r row, ag *agent, ms int64) (usage.Response, bool) {
	m := obj.Get("metadata.metrics")
	if !m.IsObject() {
		return usage.Response{}, false
	}
	cache, write := tokInt(m.Get("cache_read_tokens")), tokInt(m.Get("cache_creation_tokens"))
	in := min(tokInt(m.Get("input_tokens"))+cache+write, source.TokCap)
	out := tokInt(m.Get("output_tokens"))
	if in == 0 && out == 0 {
		return usage.Response{}, false
	}
	key := ""
	if v := obj.Get("metadata.request_id"); v.Type == gjson.String {
		key = strings.TrimSpace(v.Str)
	}
	if key != "" {
		key = "devin:" + r.sid + ":request:" + key
	} else {
		key = "devin:" + r.sid + ":row:" + strconv.FormatInt(r.rowid, 10)
	}
	model := ""
	if v := obj.Get("metadata.generation_model"); v.Type == gjson.String {
		model = strings.TrimSpace(v.Str)
	}
	if model == "" {
		model = ag.sessModel
	}
	resp := usage.Response{
		Key: key, Source: Devin, Agent: ag.id, Project: ag.project, Model: model, EndMs: ms, Final: true,
		Tokens: usage.Tokens{Input: in, CachedInput: cache, CacheWrite: write, Output: out},
	}
	if tt, ok := millis(m.Get("total_time_ms")); ok && tt > 0 {
		resp.StartMs = satSub(ms, tt)
		if tf, ok := millis(m.Get("ttft_ms")); ok && tf > 0 && tf <= tt && resp.StartMs > 0 {
			resp.FirstMs = resp.StartMs + tf
		}
	}
	return resp, true
}

func satSub(a, b int64) int64 {
	if a < math.MinInt64+b {
		return math.MinInt64
	}
	return a - b
}

// millis is a finite JSON number truncated to an integer.
func millis(r gjson.Result) (int64, bool) {
	if r.Type != gjson.Number {
		return 0, false
	}
	f := r.Float()
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return truncI64(f), true
}

func tokInt(r gjson.Result) int64 {
	if r.Type != gjson.Number {
		return 0
	}
	f := r.Float()
	switch {
	case math.IsNaN(f) || f <= 0:
		return 0
	case f >= source.TokCap:
		return source.TokCap
	}
	return int64(f)
}

// sessionModel is sessions.model trimmed, unless blank or "adaptive" (a
// routing mode, not a model).
func sessionModel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "adaptive") {
		return ""
	}
	return s
}

// agentFor returns the agent of a session, created on its first row; nil
// when the database does not list the session or an earlier database has it.
func (d *database) agentFor(ctx context.Context, sid string, earlier []*database) (*agent, error) {
	if ag := d.agents[sid]; ag != nil {
		return ag, nil
	}
	if sid == "\x00" {
		return nil, nil
	}
	var cwd, model sql.NullString
	if err := d.db.QueryRowContext(ctx, `SELECT working_directory, model FROM sessions WHERE id = ?`, sid).Scan(&cwd, &model); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range earlier {
		var one int
		err := e.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, sid).Scan(&one)
		if err == nil {
			return nil, nil // an earlier database has this session
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	project := lastComponent(cwd.String)
	if project == "" {
		project = sid
	}
	ag := &agent{id: d.path + "#" + sid, cwd: cwd.String, project: project,
		sessModel: sessionModel(model.String), phase: usage.Idle}
	d.agents[sid] = ag
	return ag, nil
}

func lastComponent(p string) string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
