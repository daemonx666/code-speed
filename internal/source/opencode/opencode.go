// Package opencode reads OpenCode's SQLite database (Agent Speed
// spec/METRICS.md §4.5 and §5.2, MIT). Two layouts are supported: the 2.x
// one (tables session_v2 and session_message) and the one older and current
// releases write (tables session and message, one JSON object per message).
package opencode

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

// OpenCode is the source id.
const OpenCode usage.Source = "opencode"

func init() {
	source.Register(source.Spec{
		Source: OpenCode,
		Label:  "OpenCode",
		Color:  "#eab308",
		Detect: func(string) bool { _, err := os.Stat(DBPath()); return err == nil },
		Poller: func() source.Poller { return &poller{} },
		Every:  5 * time.Second,
	})
}

// DBPath is where OpenCode keeps its database.
func DBPath() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "opencode", "opencode.db")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// layout names the tables of one database layout.
type layout struct {
	name     string
	sessions string
	messages string
	v2       bool // messages have type and seq columns
}

var layouts = []layout{
	{"v2", "session_v2", "session_message", true},
	{"legacy", "session", "message", false},
}

type sessMeta struct {
	dir     string
	parent  bool
	created int64
}

type poller struct {
	path string // set by tests; else DBPath()

	db        *sql.DB
	immutable bool
	size      int64
	mtime     time.Time
	have      map[string]bool // layouts present in the open database
	hw        map[string]int64
	sess      map[string]sessMeta
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
	p.have = map[string]bool{}
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		p.close()
		return err
	}
	tables := map[string]bool{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			tables[n] = true
		}
	}
	rows.Close()
	for _, l := range layouts {
		p.have[l.name] = tables[l.sessions] && tables[l.messages]
	}
	return nil
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	path := p.dbPath()
	if path == "" {
		return nil
	}
	first := p.hw == nil
	if first {
		p.hw = map[string]int64{}
		p.sess = map[string]sessMeta{}
	}
	if err := p.ensure(path); err != nil {
		return err
	}
	if p.db == nil {
		return nil
	}
	for _, l := range layouts {
		if !p.have[l.name] {
			continue
		}
		if err := p.read(ctx, l, since.UnixMilli(), now.UnixMilli(), first, path, d, sink); err != nil {
			p.close()
			return err
		}
	}
	return nil
}

type msgRow struct {
	id, session, kind string
	created, updated  int64
	data              string
}

func (p *poller) meta(l layout, id string) sessMeta {
	key := l.name + "|" + id
	if m, ok := p.sess[key]; ok {
		return m
	}
	var dir sql.NullString
	var parent sql.NullString
	var created sql.NullInt64
	err := p.db.QueryRow(`SELECT directory, parent_id, time_created FROM `+l.sessions+` WHERE id = ?`, id).Scan(&dir, &parent, &created)
	m := sessMeta{dir: dir.String, parent: parent.Valid && parent.String != "", created: created.Int64}
	if err == nil {
		p.sess[key] = m // an unknown session is asked again next time
	}
	return m
}

func (p *poller) read(ctx context.Context, l layout, since, now int64, first bool, path string, d *source.Dedup, sink source.Sink) error {
	hw, ok := p.hw[l.name]
	if !ok {
		hw = since - 60_000
	}
	q := `SELECT id, session_id, 'message', time_created, time_updated, data FROM ` + l.messages +
		` WHERE time_updated >= ? ORDER BY time_created, id`
	if l.v2 {
		q = `SELECT id, session_id, type, time_created, time_updated, data FROM ` + l.messages +
			` WHERE time_updated >= ? ORDER BY session_id, seq`
	}
	rows, err := p.db.QueryContext(ctx, q, hw)
	if err != nil {
		return err
	}
	var got []msgRow
	newHW := hw
	for rows.Next() {
		var r msgRow
		if err := rows.Scan(&r.id, &r.session, &r.kind, &r.created, &r.updated, &r.data); err != nil {
			rows.Close()
			return err
		}
		got = append(got, r)
		newHW = max(newHW, r.updated)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	touched := map[string]bool{}
	for _, r := range got {
		touched[r.session] = true
		m := p.meta(l, r.session)
		if resp, ok := p.response(l, r, m, path); ok {
			if first && resp.EndMs < since {
				continue
			}
			if d != nil {
				if mine, _ := d.Claim(resp.Key, path); !mine {
					continue
				}
			}
			sink.Response(resp)
		}
	}
	p.hw[l.name] = newHW
	for sid := range touched {
		if err := p.state(ctx, l, sid, now, path, sink); err != nil {
			return err
		}
	}
	return nil
}

// tok reads a token count: a number truncated toward zero, clamped to 0…10^12.
func tok(r gjson.Result) int64 {
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

func tokens(t gjson.Result) usage.Tokens {
	cr, cw := tok(t.Get("cache.read")), tok(t.Get("cache.write"))
	rs := tok(t.Get("reasoning"))
	return usage.Tokens{
		Input:       source.Sat(source.Sat(tok(t.Get("input")), cr), cw),
		CachedInput: cr,
		CacheWrite:  cw,
		Output:      source.Sat(tok(t.Get("output")), rs),
		Reasoning:   rs,
	}
}

func ms(r gjson.Result) int64 {
	if r.Type != gjson.Number {
		return 0
	}
	f := r.Float()
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 || f > 1e15 {
		return 0
	}
	return int64(f)
}

// modelOf returns the model and provider ids of an assistant message.
func modelOf(l layout, data gjson.Result) (model, provider string) {
	if l.v2 {
		return data.Get("model.id").String(), data.Get("model.providerID").String()
	}
	model, provider = data.Get("modelID").String(), data.Get("providerID").String()
	if model == "" {
		model = data.Get("model.modelID").String()
		if model == "" {
			model = data.Get("model.id").String()
		}
	}
	if provider == "" {
		provider = data.Get("model.providerID").String()
	}
	return
}

func agentID(path, session string) string { return path + "#" + session }

// response maps one message row to a response, when it is one.
func (p *poller) response(l layout, r msgRow, m sessMeta, path string) (usage.Response, bool) {
	if !gjson.Valid(r.data) {
		return usage.Response{}, false
	}
	data := gjson.Parse(r.data)
	var start, end int64
	switch {
	case l.v2 && r.kind == "assistant", !l.v2 && data.Get("role").String() == "assistant":
		start = ms(data.Get("time.created"))
		if start < m.created-2000 {
			return usage.Response{}, false // a fork's copy of its parent
		}
		if l.v2 {
			end = ms(data.Get("time.streamed"))
		} else {
			end = ms(data.Get("time.completed"))
		}
		if end == 0 { // in flight
			return usage.Response{}, false
		}
	case l.v2 && r.kind == "compaction":
		start = ms(data.Get("time.created"))
		end = ms(data.Get("time.completed"))
		if end == 0 {
			end = r.updated
		}
	default:
		return usage.Response{}, false
	}
	t := data.Get("tokens")
	if !t.IsObject() {
		return usage.Response{}, false
	}
	model, provider := modelOf(l, data)
	return usage.Response{
		Key:      "opencode:" + r.id,
		Source:   OpenCode,
		Agent:    agentID(path, r.session),
		Project:  source.Project(m.dir),
		Model:    model,
		Provider: provider,
		StartMs:  start,
		EndMs:    end,
		Tokens:   tokens(t),
		Final:    true,
	}, true
}

// state reports the latest state of one session.
func (p *poller) state(ctx context.Context, l layout, sid string, now int64, path string, sink source.Sink) error {
	m := p.meta(l, sid)
	st := usage.AgentState{
		Agent:    agentID(path, sid),
		Source:   OpenCode,
		Project:  source.Project(m.dir),
		Subagent: m.parent,
		Phase:    usage.Idle,
	}
	var last sql.NullInt64
	if err := p.db.QueryRowContext(ctx, `SELECT MAX(time_updated) FROM `+l.messages+` WHERE session_id = ?`, sid).Scan(&last); err != nil {
		return err
	}
	st.LastLineMs = last.Int64
	q := `SELECT 'message', data, time_updated FROM ` + l.messages + ` WHERE session_id = ? ORDER BY time_created DESC, id DESC LIMIT 12`
	if l.v2 {
		q = `SELECT type, data, time_updated FROM ` + l.messages + ` WHERE session_id = ? ORDER BY seq DESC LIMIT 12`
	}
	rows, err := p.db.QueryContext(ctx, q, sid)
	if err != nil {
		return err
	}
	defer rows.Close()
	phaseSet, ctxSet, modelSet := false, false, false
	for rows.Next() {
		var kind, data string
		var upd int64
		if err := rows.Scan(&kind, &data, &upd); err != nil {
			return err
		}
		if !gjson.Valid(data) {
			continue
		}
		v := gjson.Parse(data)
		assistant := (l.v2 && kind == "assistant") || (!l.v2 && v.Get("role").String() == "assistant")
		if l.v2 && kind == "idle" && !phaseSet {
			phaseSet = true
			continue
		}
		if !assistant {
			continue
		}
		if ms(v.Get("time.created")) < m.created-2000 {
			continue
		}
		if !modelSet {
			st.Model, _ = modelOf(l, v)
			modelSet = st.Model != ""
		}
		done := ms(v.Get("time.completed")) > 0
		if l.v2 {
			done = ms(v.Get("time.streamed")) > 0
		}
		if !phaseSet {
			phaseSet = true
			switch {
			case !done:
				if c := ms(v.Get("time.created")); c > 0 && now-c <= usage.MaxInterval {
					st.Phase, st.InFlightMs = usage.Generating, c
				}
			case v.Get("finish").String() == "tool-calls":
				st.Phase = usage.Tools
			}
		}
		if !ctxSet && done && v.Get("tokens").IsObject() {
			ctxSet = true
			st.Context = tokens(v.Get("tokens")).Input
		}
		if phaseSet && ctxSet && modelSet {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	sink.Agent(st)
	return nil
}
