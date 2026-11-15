// Package goose reads Goose's (block/goose) SQLite session database. Goose
// (schema v18, v1.59) stores each assistant message with a metadata_json
// column whose "usage" object holds the response's token counts and timing.
// Sessions older than v1.10 were JSONL files without per-response usage;
// they are not read.
//
// Still to check against a real database: that inference metadata is on
// every assistant message, that created_timestamp is the time the stream
// ended, the macOS data path, and what ACP / CLI-wrapper providers write
// into usage.
package goose

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Goose is the source id.
const Goose usage.Source = "goose"

func init() {
	source.Register(source.Spec{
		Source: Goose,
		Label:  "Goose",
		Color:  "#b45309",
		Detect: func(string) bool { _, err := os.Stat(DBPath()); return err == nil },
		Poller: func() source.Poller { return &poller{} },
		Every:  5 * time.Second,
	})
}

const rel = "sessions/sessions.db"

// candidates lists where Goose may keep its database, most likely first.
func candidates() []string {
	var out []string
	if v := os.Getenv("GOOSE_PATH_ROOT"); v != "" && filepath.IsAbs(v) {
		out = append(out, filepath.Join(v, "data", rel))
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		out = append(out, filepath.Join(v, "goose", rel))
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		out = append(out, filepath.Join(home, ".local", "share", "goose", rel))
		if runtime.GOOS == "darwin" {
			b := filepath.Join(home, "Library", "Application Support", "Block", "goose")
			out = append(out, filepath.Join(b, "data", rel), filepath.Join(b, rel))
		}
	}
	if runtime.GOOS == "windows" {
		if v := os.Getenv("APPDATA"); v != "" {
			out = append(out, filepath.Join(v, "Block", "goose", "data", rel))
		}
	}
	return out
}

// DBPath is where Goose keeps its database: the first candidate that
// exists, else the first.
func DBPath() string {
	c := candidates()
	for _, p := range c {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if len(c) > 0 {
		return c[0]
	}
	return ""
}

type sessMeta struct {
	dir, model string
	parent     bool
}

type poller struct {
	path string // set by tests; else DBPath()

	db        *sql.DB
	opened    string
	immutable bool
	size      int64
	mtime     time.Time
	query     string // "" when the schema has no usage data
	hw        int64
	started   bool
	last      map[string]usage.AgentState
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
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols[n] = true
	}
	return cols, rows.Err()
}

// ensure opens the database, or reopens it when an immutable connection is stale.
func (p *poller) ensure(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		p.close()
		return nil
	}
	if p.db != nil && (p.opened != path || p.immutable && fileChanged(path, st, p)) {
		p.close()
	}
	if p.db != nil {
		return nil
	}
	db, imm, err := sqlitedb.Open(path)
	if err != nil {
		return err
	}
	p.db, p.opened, p.immutable, p.size, p.mtime = db, path, imm, st.Size(), st.ModTime()
	p.query = ""
	mc, err := columns(db, "messages")
	if err != nil {
		p.close()
		return err
	}
	if !mc["metadata_json"] || !mc["created_timestamp"] || !mc["session_id"] || !mc["role"] {
		return nil // an old schema: no per-response usage
	}
	sc, err := columns(db, "sessions")
	if err != nil {
		p.close()
		return err
	}
	opt := func(c string) string {
		if sc[c] {
			return "s." + c
		}
		return "NULL"
	}
	msgID := "NULL"
	if mc["message_id"] {
		msgID = "m.message_id"
	}
	join := ""
	if sc["id"] {
		join = ` LEFT JOIN sessions s ON s.id = m.session_id`
	}
	p.query = `SELECT m.id, m.session_id, ` + msgID + `, m.created_timestamp, m.metadata_json, ` +
		opt("working_dir") + `, ` + opt("model_config_json") + `, ` + opt("parent_session_id") +
		` FROM messages m` + join +
		` WHERE m.id > ? AND m.role = 'assistant' AND m.metadata_json LIKE '%"usage"%' ORDER BY m.id`
	if join == "" {
		p.query = strings.NewReplacer("s.working_dir", "NULL", "s.model_config_json", "NULL", "s.parent_session_id", "NULL").Replace(p.query)
	}
	return nil
}

func fileChanged(path string, st os.FileInfo, p *poller) bool {
	_, werr := os.Stat(path + "-wal")
	return werr == nil || st.Size() != p.size || !st.ModTime().Equal(p.mtime)
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
	first := !p.started
	if first {
		p.started = true
		p.last = map[string]usage.AgentState{}
	}
	rows, err := p.db.QueryContext(ctx, p.query, p.hw)
	if err != nil {
		p.close()
		return err
	}
	type row struct {
		id                    int64
		session               string
		msgID                 sql.NullString
		created               sql.NullInt64
		meta                  sql.NullString
		dir, modelCfg, parent sql.NullString
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.session, &r.msgID, &r.created, &r.meta, &r.dir, &r.modelCfg, &r.parent); err != nil {
			rows.Close()
			p.close()
			return err
		}
		got = append(got, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		p.close()
		return err
	}
	touched := map[string]usage.AgentState{}
	for _, r := range got {
		p.hw = max(p.hw, r.id)
		m := sessMeta{
			dir:    r.dir.String,
			model:  gjson.Get(r.modelCfg.String, "model_name").String(),
			parent: r.parent.Valid && r.parent.String != "",
		}
		resp, ok := response(r.id, r.session, r.msgID.String, r.created.Int64, r.meta.String, m, path)
		if !ok {
			continue
		}
		st := usage.AgentState{
			Agent:      resp.Agent,
			Source:     Goose,
			Project:    resp.Project,
			Model:      resp.Model,
			Subagent:   m.parent,
			Phase:      usage.Idle,
			LastLineMs: resp.EndMs,
			Context:    resp.Input,
		}
		touched[r.session] = st
		if first && resp.EndMs < since.UnixMilli() {
			continue
		}
		if d != nil {
			if mine, _ := d.Claim(resp.Key, path); !mine {
				continue
			}
		}
		sink.Response(resp)
	}
	for _, st := range touched {
		sink.Agent(st)
	}
	return nil
}

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

// response maps one assistant message row to a response, when it is one.
func response(id int64, session, msgID string, created int64, meta string, m sessMeta, path string) (usage.Response, bool) {
	if !gjson.Valid(meta) {
		return usage.Response{}, false
	}
	md := gjson.Parse(meta)
	u := md.Get("usage")
	if !u.IsObject() {
		return usage.Response{}, false
	}
	out := tok(u.Get("outputTokens"))
	if out == 0 {
		return usage.Response{}, false // the empty usage written while streaming
	}
	cr, cw := tok(u.Get("cacheReadTokens")), tok(u.Get("cacheWriteTokens"))
	in := max(tok(u.Get("inputTokens")), source.Sat(cr, cw))

	end := created
	if end <= 0 {
		return usage.Response{}, false
	}
	if end <= 1e10 {
		end *= 1000
	}
	var start, first int64
	if el := ms(u.Get("elapsedMs")); el > 0 {
		start = end - el
		if start <= 0 {
			start = 0
		} else if ttft := ms(u.Get("timeToFirstTokenMs")); ttft > 0 && ttft <= el {
			first = start + ttft
		}
	}
	model := md.Get("inference.resolvedModel").String()
	if model == "" {
		model = md.Get("inference.requestedModel").String()
	}
	if model == "" {
		model = m.model
	}
	key := "goose:" + session + ":" + msgID
	if msgID == "" {
		key = "goose:" + session + ":#" + strconv.FormatInt(id, 10)
	}
	return usage.Response{
		Key:      key,
		Source:   Goose,
		Agent:    path + "#" + session,
		Project:  source.Project(m.dir),
		Model:    model,
		Provider: md.Get("inference.provider").String(),
		StartMs:  start,
		EndMs:    end,
		FirstMs:  first,
		Tokens: usage.Tokens{
			Input:       in,
			CachedInput: cr,
			CacheWrite:  cw,
			Output:      out,
		},
		Final: true,
	}, true
}
