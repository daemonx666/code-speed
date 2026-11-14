package qoder

import (
	"context"
	"database/sql"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Qoder is the source id of the Qoder IDE.
const Qoder usage.Source = "qoder"

func init() {
	source.Register(source.Spec{
		Source: Qoder,
		Label:  "Qoder",
		Color:  "#8b5cf6",
		Detect: func(home string) bool {
			for _, f := range ideFolders(home) {
				if st, err := os.Stat(f); err == nil && st.IsDir() {
					return true
				}
			}
			return false
		},
		Poller: func() source.Poller { return &poller{} },
		Every:  2 * time.Second,
	})
}

// Qoder IDE (METRICS §4.18): the agent core keeps every chat in one SQLite
// database per edition; each assistant row of chat_message holds the usage
// one model call reported. Only usage metadata is read, never content.

const (
	maxJSON    = 1 << 20
	windowSlop = 15 * 60_000
)

// bounded is the column when it is text of at most 1 MiB, else NULL.
func bounded(col string) string {
	return "CASE WHEN typeof(" + col + ") = 'text' AND length(CAST(" + col + " AS BLOB)) <= " +
		strconv.Itoa(maxJSON) + " THEN " + col + " END"
}

type poller struct {
	paths func() []string // set by tests; else the automatic databases

	dbs map[string]*database
}

func (p *poller) databases() []string {
	if p.paths != nil {
		return p.paths()
	}
	home, _ := os.UserHomeDir()
	var out []string
	for _, f := range ideDatabases(home) {
		out = append(out, f)
	}
	return out
}

type session struct {
	cwd      string
	project  string
	subagent bool
}

type agent struct {
	id       string
	sess     session
	model    string
	phase    usage.Phase
	since    int64 // time of the row that last set the phase
	hasSince bool
	inFlight int64
	req      string // request id of the assistant row that set the phase
	last     int64
	ctx, win int64
	prev     int64 // time of the session's previous row
	hasPrev  bool
	known    bool // prev is known (else asked of the database)
}

func (a *agent) setPhase(ph usage.Phase, ms int64, req string) bool {
	if a.hasSince && ms < a.since {
		return false
	}
	a.phase, a.since, a.hasSince, a.req = ph, ms, true, req
	a.inFlight = 0
	if ph == usage.Generating {
		a.inFlight = ms
	}
	return true
}

type database struct {
	path      string
	db        *sql.DB
	immutable bool
	fi        os.FileInfo
	supported bool
	hasParent bool
	hasRecord bool
	hasReq    bool
	hw        int64 // highest rowid read; -1 before the first read
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

// ensure opens the database (again when needed). ok is false when there is
// nothing to read.
func (d *database) ensure(ctx context.Context) (ok bool, err error) {
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
		msg, err1 := columns(ctx, db, "chat_message")
		ses, err2 := columns(ctx, db, "chat_session")
		d.supported = err1 == nil && err2 == nil
		for _, c := range []string{"id", "session_id", "role", "token_info", "model_info", "gmt_create"} {
			d.supported = d.supported && msg[c]
		}
		d.supported = d.supported && ses["session_id"] && ses["project_uri"]
		d.hasParent, d.hasReq = ses["parent_session_id"], msg["request_id"]
		rec, err3 := columns(ctx, db, "chat_record")
		d.hasRecord = err3 == nil && rec["request_id"] && rec["extra"]
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
	id      any
	sid     string
	role    any
	created any
	token   sql.NullString
	model   sql.NullString
	req     sql.NullString
}

func rowTime(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) || math.Abs(t) > 9e18 {
			return 0, false
		}
		return int64(t), true
	}
	return 0, false
}

func (d *database) query(ctx context.Context, where, order string, args ...any) (*sql.Rows, error) {
	req := "NULL"
	if d.hasReq {
		req = "request_id"
	}
	return d.db.QueryContext(ctx, `SELECT rowid, id, session_id, role, gmt_create, `+bounded("token_info")+`, `+
		bounded("model_info")+`, `+req+` FROM chat_message `+where+` ORDER BY rowid `+order, args...)
}

func scanRow(rows *sql.Rows) (row, error) {
	var r row
	var sid sql.NullString
	err := rows.Scan(&r.rowid, &r.id, &sid, &r.role, &r.created, &r.token, &r.model, &r.req)
	r.sid = sid.String
	if !sid.Valid {
		r.sid = "\x00"
	}
	return r, err
}

func (d *database) read(ctx context.Context, floor int64, earlier []*database, dd *source.Dedup, sink source.Sink) error {
	var maxRow sql.NullInt64
	if err := d.db.QueryRowContext(ctx, `SELECT MAX(rowid) FROM chat_message`).Scan(&maxRow); err != nil {
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
		rs, err := d.query(ctx, "WHERE rowid > ?", "", d.hw)
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
	finished := map[string]int64{}
	for _, r := range rows {
		ag, err := d.agentFor(ctx, r.sid, earlier)
		if err != nil {
			return err
		}
		if ag == nil {
			continue // a session chat_session does not list
		}
		if !ag.known {
			ag.known = true
			ag.prev, ag.hasPrev, err = d.previous(ctx, r.sid, r.rowid)
			if err != nil {
				return err
			}
		}
		start, hasStart := ag.prev, ag.hasPrev
		ms, hasMs := rowTime(r.created)
		ag.prev, ag.hasPrev = ms, hasMs
		touched[r.sid] = true
		if hasMs {
			ag.last = max(ag.last, ms)
		}
		role, isStr := r.role.(string)
		if !hasMs || !isStr {
			continue
		}
		switch role {
		case "assistant":
			if resp, ok := d.call(r, ag, ms, start, hasStart); ok {
				if mine, _ := dd.Claim(resp.Key, d.path); mine {
					sink.Response(resp)
					if resp.Model != "" {
						ag.model = resp.Model
					}
					ag.ctx = resp.Input
					ag.win = r.window()
				}
			}
			ag.setPhase(usage.Tools, ms, r.req.String)
		case "user", "tool":
			ag.setPhase(usage.Generating, ms, "")
		}
	}
	// an assistant row's request that has finished: its agent is idle
	for sid, ag := range d.agents {
		if ag.phase != usage.Tools || ag.req == "" || !d.hasRecord {
			continue
		}
		at, ok := finished[ag.req]
		if !ok {
			var err error
			if at, ok, err = d.finishedAt(ctx, ag.req); err != nil {
				return err
			}
			if !ok {
				continue
			}
			finished[ag.req] = at
		}
		if at >= ag.since {
			ag.phase, ag.req = usage.Idle, ""
			touched[sid] = true
		}
	}
	for sid := range touched {
		ag := d.agents[sid]
		sink.Agent(usage.AgentState{
			Agent: ag.id, Source: Qoder, Project: ag.sess.project, Model: ag.model, Subagent: ag.sess.subagent,
			Phase: ag.phase, InFlightMs: ag.inFlight, LastLineMs: ag.last, Context: ag.ctx, ContextWindow: ag.win,
		})
	}
	return nil
}

func (r row) window() int64 {
	if !r.token.Valid {
		return 0
	}
	return tokInt(gjson.Get(r.token.String, "max_input_tokens"))
}

// call is the response of an assistant row whose token_info is a JSON
// object, none when its input and output are both 0.
func (d *database) call(r row, ag *agent, ms, start int64, hasStart bool) (usage.Response, bool) {
	if !r.token.Valid || !gjson.Valid(r.token.String) {
		return usage.Response{}, false
	}
	info := gjson.Parse(r.token.String)
	if !info.IsObject() {
		return usage.Response{}, false
	}
	in, out := tokInt(info.Get("prompt_tokens")), tokInt(info.Get("completion_tokens"))
	if in == 0 && out == 0 {
		return usage.Response{}, false
	}
	key := ""
	if s, ok := r.id.(string); ok {
		key = strings.TrimSpace(s)
	}
	if key == "" {
		key = r.sid + ":row:" + strconv.FormatInt(r.rowid, 10)
	}
	var model string
	if r.model.Valid && gjson.Valid(r.model.String) {
		if mi := gjson.Parse(r.model.String); mi.IsObject() {
			if mk := mi.Get("model_key"); mk.Type == gjson.String {
				model = strings.TrimSpace(mk.Str)
			}
		}
	}
	resp := usage.Response{
		Key: "qoder:" + key, Source: Qoder, Agent: ag.id, Project: ag.sess.project, Model: model,
		EndMs: ms, Final: true,
		Tokens: usage.Tokens{
			Input: in, CachedInput: min(tokInt(info.Get("cached_tokens")), in), Output: out,
		},
	}
	if hasStart {
		resp.StartMs = start
	}
	return resp, true
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

// previous is the time of the session's row before rowid.
func (d *database) previous(ctx context.Context, sid string, rowid int64) (ms int64, ok bool, err error) {
	var v any
	err = d.db.QueryRowContext(ctx, `SELECT gmt_create FROM chat_message WHERE session_id = ? AND rowid < ? ORDER BY rowid DESC LIMIT 1`,
		sid, rowid).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	ms, ok = rowTime(v)
	return
}

// finishedAt is the latest turn-info.finishedAt of the request's chat_record rows.
func (d *database) finishedAt(ctx context.Context, req string) (at int64, ok bool, err error) {
	rs, err := d.db.QueryContext(ctx, `SELECT `+bounded("extra")+` FROM chat_record WHERE request_id = ?`, req)
	if err != nil {
		return 0, false, err
	}
	defer rs.Close()
	for rs.Next() {
		var extra sql.NullString
		if err := rs.Scan(&extra); err != nil {
			return 0, false, err
		}
		if !extra.Valid || !gjson.Valid(extra.String) {
			continue
		}
		root := gjson.Parse(extra.String)
		if !root.IsObject() {
			continue
		}
		f := root.Get("ai-coding/turn-info.finishedAt")
		if f.Type != gjson.Number || math.IsInf(f.Float(), 0) || math.IsNaN(f.Float()) || math.Abs(f.Float()) > 9e18 {
			continue
		}
		v := int64(f.Float())
		if !ok || v > at {
			at, ok = v, true
		}
	}
	return at, ok, rs.Err()
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
	q := `SELECT project_uri, NULL FROM chat_session WHERE session_id = ?`
	if d.hasParent {
		q = `SELECT project_uri, parent_session_id FROM chat_session WHERE session_id = ?`
	}
	var uri, parent sql.NullString
	if err := d.db.QueryRowContext(ctx, q, sid).Scan(&uri, &parent); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range earlier {
		var one int
		err := e.db.QueryRowContext(ctx, `SELECT 1 FROM chat_session WHERE session_id = ?`, sid).Scan(&one)
		if err == nil {
			return nil, nil // an earlier database has this session
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	cwd := projectPath(uri.String)
	project := lastComponent(cwd)
	if project == "" {
		project = sid
	}
	ag := &agent{id: d.path + "#" + sid, phase: usage.Idle,
		sess: session{cwd: cwd, project: project, subagent: strings.TrimSpace(parent.String) != ""}}
	d.agents[sid] = ag
	return ag, nil
}

var driveRe = regexp.MustCompile(`^/[A-Za-z]:`)

// projectPath is the working folder of chat_session.project_uri: a path as
// it is, or a file:// URI's percent-decoded path.
func projectPath(uri string) string {
	if strings.TrimSpace(uri) == "" {
		return ""
	}
	if len(uri) < 7 || !strings.EqualFold(uri[:7], "file://") {
		return uri
	}
	rest := uri[7:]
	if !strings.HasPrefix(rest, "/") {
		i := strings.Index(rest, "/")
		if i < 0 {
			return ""
		}
		rest = rest[i:]
	}
	dec, err := url.PathUnescape(rest)
	if err != nil {
		dec = rest
	}
	if driveRe.MatchString(dec) {
		dec = dec[1:]
	}
	return dec
}

func lastComponent(p string) string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
