// Package copilot reads GitHub Copilot CLI's session store, a SQLite
// database (~/.copilot/session-store.db). Its table assistant_usage_events
// holds one row per model call: tokens, duration and time to first token.
// CLIs older than about 1.0.69 don't write that table; there is nothing to
// read from them (events.jsonl carries no per-call usage and is not read).
package copilot

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Copilot is the source id.
const Copilot usage.Source = "copilot"

func init() {
	source.Register(source.Spec{
		Source: Copilot,
		Label:  "Copilot CLI",
		Color:  "#db2777",
		Detect: func(string) bool { _, err := os.Stat(DBPath()); return err == nil },
		Poller: func() source.Poller { return &poller{} },
		Every:  5 * time.Second,
	})
}

// DBPath is where Copilot CLI keeps its session store: $COPILOT_HOME, else
// the .copilot folder of the user's home (%USERPROFILE% on Windows).
func DBPath() string {
	if v := os.Getenv("COPILOT_HOME"); v != "" {
		return filepath.Join(v, "session-store.db")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".copilot", "session-store.db")
}

// usageCols are the columns read from assistant_usage_events, after id.
// One missing from an older database reads as NULL.
var usageCols = []string{
	"session_id", "model", "input_tokens", "output_tokens", "cache_read_tokens",
	"cache_write_tokens", "reasoning_tokens", "initiator", "created_at",
	"agent_id", "duration_ms", "time_to_first_token_ms", "reasoning_effort",
}

type row struct {
	id                                  int64
	session, model, initiator, created  string
	agent, effort                       string
	in, out, cr, cw, rs, duration, ttft int64
}

type poller struct {
	path string // set by tests; else DBPath()

	db        *sql.DB
	immutable bool
	size      int64
	mtime     time.Time
	query     string // SELECT of the columns this database has; "" without the table
	hasSess   bool   // a sessions table exists
	started   bool
	hw        int64
	cwd       map[string]string
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

func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// ensure opens the database, or reopens it when an immutable connection is stale.
func (p *poller) ensure(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		p.close()
		return nil // no database: nothing to read
	}
	if p.db != nil && p.immutable {
		_, werr := os.Stat(path + "-wal")
		if werr == nil || st.Size() != p.size || !st.ModTime().Equal(p.mtime) {
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
	if err := p.probe(); err != nil {
		p.close()
		return err
	}
	return nil
}

func (p *poller) probe() error {
	p.query, p.hasSess = "", false
	ev, err := columns(p.db, "assistant_usage_events")
	if err != nil {
		return err
	}
	if !ev["id"] || !ev["session_id"] {
		return nil // a CLI that doesn't write usage events
	}
	sel := []string{"id"}
	for _, c := range usageCols {
		if ev[c] {
			sel = append(sel, c)
		} else {
			sel = append(sel, "NULL")
		}
	}
	p.query = `SELECT ` + strings.Join(sel, ", ") + ` FROM assistant_usage_events WHERE id > ? ORDER BY id`
	se, err := columns(p.db, "sessions")
	if err != nil {
		return err
	}
	p.hasSess = se["id"] && se["cwd"]
	return nil
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	path := p.dbPath()
	if path == "" {
		return nil
	}
	if err := p.ensure(path); err != nil {
		return err
	}
	if p.db == nil || p.query == "" {
		return nil
	}
	if err := p.read(ctx, since.UnixMilli(), path, d, sink); err != nil {
		p.close()
		return err
	}
	return nil
}

func num(v sql.NullInt64) int64 {
	if !v.Valid || v.Int64 < 0 {
		return 0
	}
	return min(v.Int64, source.TokCap)
}

// parseTime reads created_at as Unix ms: an RFC 3339 time, or a zoneless
// "YYYY-MM-DD HH:MM:SS[.fff]" (also with a T), which is UTC.
func parseTime(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if ms, ok := source.ParseTime(s); ok {
		return ms, true
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

func (p *poller) project(session string) string {
	if !p.hasSess {
		return ""
	}
	if c, ok := p.cwd[session]; ok {
		return source.Project(c)
	}
	var cwd sql.NullString
	if err := p.db.QueryRow(`SELECT cwd FROM sessions WHERE id = ?`, session).Scan(&cwd); err != nil {
		return "" // an unknown session is asked again next time
	}
	p.cwd[session] = cwd.String
	return source.Project(cwd.String)
}

func agentID(path, session, agent string) string {
	if agent != "" {
		return path + "#" + session + "#" + agent
	}
	return path + "#" + session
}

// response maps a row to a response; ok is false for one to drop.
func (r row) response(path string) (usage.Response, bool) {
	end, ok := parseTime(r.created)
	if !ok || r.session == "" {
		return usage.Response{}, false
	}
	// input_tokens already includes the cached tokens, unlike OpenCode's.
	// Open question (unverified): whether output_tokens includes
	// reasoning_tokens as OpenAI's completion tokens do. It is treated as
	// OpenCode does, output excluding reasoning, and the two are added so
	// that the output speed counts the thinking; if the SDK in fact
	// includes reasoning in output_tokens, this overcounts by that much.
	t := usage.Tokens{
		Input:       r.in,
		CachedInput: r.cr,
		CacheWrite:  r.cw,
		Output:      source.Sat(r.out, r.rs),
		Reasoning:   r.rs,
	}
	if t.Input == 0 && t.CachedInput == 0 && t.CacheWrite == 0 && t.Output == 0 {
		return usage.Response{}, false
	}
	resp := usage.Response{
		Key:    fmt.Sprintf("copilot:%s:%d", r.session, r.id),
		Source: Copilot,
		Agent:  agentID(path, r.session, r.agent),
		Model:  r.model,
		EndMs:  end,
		Tokens: t,
		Effort: r.effort,
		Final:  true,
	}
	// created_at is taken as the end of the call (checked for the CLI;
	// assumed the same for the desktop app and the VS Code host, which
	// write the same table). The version that added duration_ms and
	// time_to_first_token_ms is not known: older rows have none and are
	// kept for their tokens only. A compaction is not a chat reply, so it
	// is never measured for speed either.
	if r.duration > 0 && r.initiator != "compaction" {
		resp.StartMs = end - r.duration
		if r.ttft > 0 && r.ttft <= r.duration {
			resp.FirstMs = resp.StartMs + r.ttft
		}
	}
	return resp, true
}

func (p *poller) read(ctx context.Context, since int64, path string, d *source.Dedup, sink source.Sink) error {
	var top sql.NullInt64
	if err := p.db.QueryRowContext(ctx, `SELECT MAX(id) FROM assistant_usage_events`).Scan(&top); err != nil {
		return err
	}
	first := !p.started
	if first {
		p.cwd = map[string]string{}
	}
	if top.Int64 < p.hw { // also an emptied table (NULL reads as 0)
		p.hw = 0 // the database was reset
	}
	rows, err := p.db.QueryContext(ctx, p.query, p.hw)
	if err != nil {
		return err
	}
	var got []row
	for rows.Next() {
		var r row
		var session, model, initiator, created, agent, effort sql.NullString
		var in, out, cr, cw, rs, dur, ttft sql.NullInt64
		if err := rows.Scan(&r.id, &session, &model, &in, &out, &cr, &cw, &rs, &initiator, &created, &agent, &dur, &ttft, &effort); err != nil {
			rows.Close()
			return err
		}
		r.session, r.model, r.initiator, r.created, r.agent, r.effort =
			session.String, model.String, initiator.String, created.String, agent.String, effort.String
		r.in, r.out, r.cr, r.cw, r.rs, r.duration, r.ttft = num(in), num(out), num(cr), num(cw), num(rs), num(dur), num(ttft)
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	states := map[string]usage.AgentState{}
	var order []string
	for _, r := range got {
		p.hw = max64(p.hw, r.id)
		resp, ok := r.response(path)
		if !ok {
			continue
		}
		if first && resp.EndMs < since {
			continue
		}
		if d != nil {
			if mine, _ := d.Claim(resp.Key, path); !mine {
				continue
			}
		}
		resp.Project = p.project(r.session)
		sink.Response(resp)

		st, seen := states[resp.Agent]
		if !seen {
			order = append(order, resp.Agent)
		}
		if !seen || resp.EndMs >= st.LastLineMs {
			st = usage.AgentState{
				Agent:      resp.Agent,
				Source:     Copilot,
				Project:    resp.Project,
				Model:      resp.Model,
				Subagent:   r.agent != "",
				Phase:      usage.Idle,
				LastLineMs: resp.EndMs,
				Context:    resp.Input,
			}
			states[resp.Agent] = st
		}
	}
	p.started = true
	for _, a := range order {
		sink.Agent(states[a])
	}
	return nil
}

func max64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}
