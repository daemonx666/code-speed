// Package zcode reads ZCode's (Zhipu) native SQLite store (Agent Speed
// spec/METRICS.md §4.10, MIT). The session table gives identity, folder and
// parent; model_usage is the only token source.
package zcode

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/usage"
)

// ZCode is the source id.
const ZCode usage.Source = "zcode"

func init() {
	source.Register(source.Spec{
		Source: ZCode, Label: "ZCode", Color: "#6366f1",
		Detect: func(string) bool { p := DBPath(); return p != "" && exists(p) },
		Poller: func() source.Poller { return &poller{} },
		Every:  2 * time.Second,
	})
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// DBPath is ZCode's database: $ZCODE_DATA_BASE_DIR replaces the home base,
// with .zcode/cli/db/db.sqlite underneath.
func DBPath() string {
	base := os.Getenv("ZCODE_DATA_BASE_DIR")
	if base == "" {
		h, err := os.UserHomeDir()
		if err != nil || h == "" {
			return ""
		}
		base = h
	}
	return filepath.Join(base, ".zcode", "cli", "db", "db.sqlite")
}

// needed lists the columns the reader requires; a store without them is
// not parsed.
var needed = map[string][]string{
	"session": {"id", "directory", "parent_id", "time_updated"},
	"model_usage": {"id", "session_id", "provider_id", "model_id", "status", "started_at", "completed_at",
		"input_tokens", "output_tokens", "reasoning_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"},
}

type sessMeta struct {
	dir    string
	parent bool
}

type poller struct {
	path string // set by tests; else DBPath()

	db        *sql.DB
	immutable bool
	size      int64
	mtime     time.Time
	ok        bool // supported schema
	hasMsg    bool
	hasPart   bool

	hw    int64 // completed_at high-water mark; 0 before the first poll
	sess  map[string]sessMeta
	model map[string]string // latest response model per session
	ctx   map[string]int64  // latest input per session
	last  map[string]int64  // latest completed_at per session
	seen  int64             // session time_updated high-water mark
}

func (p *poller) dbPath() string {
	if p.path != "" {
		return p.path
	}
	return DBPath()
}

func (p *poller) close() {
	if p.db != nil {
		p.db.Close()
		p.db = nil
	}
}

func (p *poller) ensure(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		p.close()
		return nil
	}
	if p.db != nil && p.immutable {
		if exists(path+"-wal") || st.Size() != p.size || !st.ModTime().Equal(p.mtime) {
			p.close()
		}
	}
	if p.db != nil {
		return nil
	}
	db, imm, err := sqlitedb.Open(path)
	if err != nil {
		return err
	}
	p.db, p.immutable, p.size, p.mtime = db, imm, st.Size(), st.ModTime()
	cols := map[string]map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		p.close()
		return err
	}
	var tables []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			tables = append(tables, n)
		}
	}
	rows.Close()
	for _, t := range tables {
		switch t {
		case "session", "model_usage", "message", "part":
		default:
			continue
		}
		cs, err := db.Query(`SELECT name FROM pragma_table_info('` + t + `')`)
		if err != nil {
			p.close()
			return err
		}
		cols[t] = map[string]bool{}
		for cs.Next() {
			var n string
			if cs.Scan(&n) == nil {
				cols[t][n] = true
			}
		}
		cs.Close()
	}
	p.ok = true
	for t, list := range needed {
		for _, c := range list {
			if !cols[t][c] {
				p.ok = false
			}
		}
	}
	has := func(t string, list ...string) bool {
		for _, c := range list {
			if !cols[t][c] {
				return false
			}
		}
		return true
	}
	p.hasMsg = has("message", "session_id", "time_created", "data")
	p.hasPart = has("part", "session_id", "time_created", "data")
	return nil
}

func num(v any) int64 {
	var f float64
	switch x := v.(type) {
	case int64:
		return min(max(x, 0), source.TokCap)
	case float64:
		f = x
	default:
		return 0
	}
	if math.IsNaN(f) || f <= 0 {
		return 0
	}
	return int64(math.Min(f, source.TokCap))
}

// msOf reads a time column: a positive number of Unix ms, else 0.
func msOf(v any) int64 {
	switch x := v.(type) {
	case int64:
		return max(x, 0)
	case float64:
		if math.IsNaN(x) || x <= 0 || x > 1e15 {
			return 0
		}
		return int64(x)
	}
	return 0
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	path := p.dbPath()
	if path == "" {
		return nil
	}
	first := p.sess == nil
	if first {
		p.sess = map[string]sessMeta{}
		p.model = map[string]string{}
		p.ctx = map[string]int64{}
		p.last = map[string]int64{}
	}
	if err := p.ensure(path); err != nil {
		return err
	}
	if p.db == nil || !p.ok {
		return nil
	}
	if err := p.poll(ctx, since.UnixMilli(), first, path, d, sink); err != nil {
		p.close()
		return err
	}
	return nil
}

func (p *poller) meta(id string) sessMeta {
	if m, ok := p.sess[id]; ok {
		return m
	}
	var dir, parent sql.NullString
	err := p.db.QueryRow(`SELECT directory, parent_id FROM session WHERE id = ?`, id).Scan(&dir, &parent)
	m := sessMeta{dir: dir.String, parent: parent.String != ""}
	if err == nil {
		p.sess[id] = m
	}
	return m
}

func agentID(path, sid string) string { return path + "#" + sid }

func (p *poller) poll(ctx context.Context, since int64, first bool, path string, d *source.Dedup, sink source.Sink) error {
	from := p.hw - 60_000
	if first {
		from = since
	}
	// started_at is not needed to find rows: a call that began before the
	// window but completed in it is included by completed_at.
	rows, err := p.db.QueryContext(ctx, `SELECT id, session_id, provider_id, model_id, status, started_at, completed_at,
		input_tokens, output_tokens, reasoning_tokens, cache_creation_input_tokens, cache_read_input_tokens
		FROM model_usage WHERE completed_at IS NOT NULL AND completed_at >= ? ORDER BY completed_at, id`, from)
	if err != nil {
		return err
	}
	type row struct {
		r usage.Response
		s string
	}
	var got []row
	hw := p.hw
	for rows.Next() {
		var id, sid string
		var prov, model, status sql.NullString
		var started, completed, in, out, rs, cw, cr any
		if err := rows.Scan(&id, &sid, &prov, &model, &status, &started, &completed, &in, &out, &rs, &cw, &cr); err != nil {
			rows.Close()
			return err
		}
		end := msOf(completed)
		if end == 0 || status.String == "running" {
			continue
		}
		hw = max(hw, end)
		got = append(got, row{usage.Response{
			Key: "zcode:" + id, Source: ZCode, Agent: agentID(path, sid),
			Model: model.String, Provider: prov.String,
			StartMs: msOf(started), EndMs: end,
			Tokens: usage.Tokens{Input: num(in), CachedInput: num(cr), CacheWrite: num(cw),
				Output: num(out), Reasoning: num(rs)},
			Final: true,
		}, sid})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	touched := map[string]bool{}
	for _, g := range got {
		touched[g.s] = true
		m := p.meta(g.s)
		g.r.Project = source.Project(m.dir)
		p.model[g.s] = g.r.Model
		p.ctx[g.s] = g.r.Input
		p.last[g.s] = max(p.last[g.s], g.r.EndMs)
		if d != nil {
			if mine, _ := d.Claim(g.r.Key, path); !mine {
				continue
			}
		}
		sink.Response(g.r)
	}
	p.hw = max(hw, p.hw)

	// sessions with news: new usage, or recently updated
	sfrom := p.seen - 60_000
	if first {
		sfrom = since
	}
	srows, err := p.db.QueryContext(ctx, `SELECT id, time_updated FROM session WHERE time_updated >= ?`, sfrom)
	if err != nil {
		return err
	}
	for srows.Next() {
		var sid string
		var upd any
		if err := srows.Scan(&sid, &upd); err != nil {
			srows.Close()
			return err
		}
		touched[sid] = true
		p.seen = max(p.seen, msOf(upd))
	}
	err = srows.Err()
	srows.Close()
	if err != nil {
		return err
	}
	// a running call keeps its session current
	rr, err := p.db.QueryContext(ctx, `SELECT DISTINCT session_id FROM model_usage WHERE completed_at IS NULL AND status = 'running'`)
	if err == nil {
		for rr.Next() {
			var sid string
			if rr.Scan(&sid) == nil {
				touched[sid] = true
			}
		}
		rr.Close()
	}
	for sid := range touched {
		if err := p.state(ctx, sid, path, sink); err != nil {
			return err
		}
	}
	return nil
}

// state reports a session's latest phase. Usage is written at completion,
// so a running call, a user message that no usage has answered, or a pending
// tool part says what is in flight.
func (p *poller) state(ctx context.Context, sid, path string, sink source.Sink) error {
	m := p.meta(sid)
	st := usage.AgentState{
		Agent: agentID(path, sid), Source: ZCode, Project: source.Project(m.dir),
		Subagent: m.parent, Model: p.model[sid], Context: p.ctx[sid], Phase: usage.Idle,
	}
	var upd, maxDone, runStart sql.NullInt64
	_ = p.db.QueryRowContext(ctx, `SELECT time_updated FROM session WHERE id = ?`, sid).Scan(&upd)
	_ = p.db.QueryRowContext(ctx, `SELECT MAX(completed_at) FROM model_usage WHERE session_id = ?`, sid).Scan(&maxDone)
	_ = p.db.QueryRowContext(ctx, `SELECT MAX(started_at) FROM model_usage WHERE session_id = ? AND completed_at IS NULL AND status = 'running'`, sid).Scan(&runStart)
	st.LastLineMs = max(upd.Int64, maxDone.Int64, runStart.Int64)
	switch {
	case runStart.Valid && runStart.Int64 > 0:
		st.Phase, st.InFlightMs = usage.Generating, runStart.Int64
	default:
		if p.hasMsg {
			var created, updated sql.NullInt64
			var data sql.NullString
			err := p.db.QueryRowContext(ctx, `SELECT time_created, time_updated, data FROM message WHERE session_id = ?
				ORDER BY time_created DESC, id DESC LIMIT 1`, sid).Scan(&created, &updated, &data)
			if err == nil {
				st.LastLineMs = max(st.LastLineMs, created.Int64, updated.Int64)
				if gjson.Get(data.String, "role").String() == "user" && created.Int64 > maxDone.Int64 {
					st.Phase, st.InFlightMs = usage.Generating, created.Int64
				}
			}
		}
		if st.Phase == usage.Idle && p.hasPart {
			prow, err := p.db.QueryContext(ctx, `SELECT data FROM part WHERE session_id = ? ORDER BY time_created DESC, id DESC LIMIT 12`, sid)
			if err == nil {
				for prow.Next() {
					var data sql.NullString
					if prow.Scan(&data) != nil {
						break
					}
					v := gjson.Parse(data.String)
					if v.Get("type").String() == "tool" {
						if s := v.Get("state.status").String(); s == "pending" || s == "running" {
							st.Phase = usage.Tools
						}
						break // only the latest tool part decides
					}
				}
				prow.Close()
			}
		}
	}
	sink.Agent(st)
	return nil
}
