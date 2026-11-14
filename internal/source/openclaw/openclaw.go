// Package openclaw reads OpenClaw's agent databases (Agent Speed
// spec/METRICS.md §4.14, MIT). From OpenClaw 2026.8.1 (agent schema 8 and
// later) the Gateway keeps every session transcript in one SQLite database
// per agent (<state>/agents/<agentId>/agent/openclaw-agent.sqlite), one
// transcript entry per row in the JSONL entry format of Pi.
//
// Not read: JSONL sessions of 2026.7 and older, PostgreSQL stores, and the
// trajectory rows (effort and speed: the usage model has no field for them,
// and their Brotli streams need a decoder this module does not carry).
package openclaw

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/source/zstdread"
	"github.com/daemonx666/code-speed/internal/usage"
)

// OpenClaw is the source id.
const OpenClaw usage.Source = "openclaw"

const dbName = "openclaw-agent.sqlite"

func init() {
	source.Register(source.Spec{
		Source: OpenClaw,
		Label:  "OpenClaw",
		Color:  "#ef4444",
		Detect: func(home string) bool { return len(stateDirs(home)) > 0 },
		Poller: func() source.Poller { return &poller{} },
		Every:  2 * time.Second,
	})
}

// ---- roots -----------------------------------------------------------

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func expand(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

var profileRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// stateDirs lists the existing state folders: $OPENCLAW_STATE_DIR alone, or
// .openclaw and the named profiles .openclaw-<name> of the base folder.
func stateDirs(home string) []string {
	if v := os.Getenv("OPENCLAW_STATE_DIR"); v != "" {
		v = filepath.Clean(expand(v, home))
		if isDir(v) {
			return []string{v}
		}
		return nil
	}
	base := home
	if v := os.Getenv("OPENCLAW_HOME"); v != "" {
		base = filepath.Clean(expand(v, home))
	}
	if base == "" {
		return nil
	}
	var out []string
	if p := filepath.Join(base, ".openclaw"); isDir(p) {
		out = append(out, p)
	}
	ents, _ := os.ReadDir(base)
	var names []string
	for _, e := range ents {
		n, ok := strings.CutPrefix(e.Name(), ".openclaw-")
		if !ok || !profileRe.MatchString(n) || strings.EqualFold(n, "default") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		if p := filepath.Join(base, n); isDir(p) {
			out = append(out, p)
		}
	}
	return out
}

// databases lists the agent databases of the state folders.
func databases(states []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, st := range states {
		ents, _ := os.ReadDir(filepath.Join(st, "agents"))
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") || e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
				continue
			}
			p := filepath.Join(st, "agents", e.Name(), "agent", dbName)
			if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// ---- poller ----------------------------------------------------------

const (
	maxEntry   = source.MaxLine
	maxZstdOut = 4 << 20
)

type winState struct {
	w       *window
	lastSeq int64
	updated int64
	gen     string
}

type dbState struct {
	sig  string
	wins map[string]*winState
}

type poller struct {
	// dbs, when set by tests, replaces the discovery
	dbs func() []string

	started bool
	state   map[string]*dbState
	dec     interface {
		DecodeAll(in, out []byte) ([]byte, error)
	}
}

func (p *poller) databases() []string {
	if p.dbs != nil {
		return p.dbs()
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return databases(stateDirs(h))
}

func mtimeOf(path string) (time.Time, int64) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, -1
	}
	return fi.ModTime(), fi.Size()
}

func signature(path string) (string, time.Time) {
	m1, s1 := mtimeOf(path)
	m2, s2 := mtimeOf(path + "-wal")
	newest := m1
	if m2.After(newest) {
		newest = m2
	}
	return m1.String() + "|" + strconv.FormatInt(s1, 10) + "|" + m2.String() + "|" + strconv.FormatInt(s2, 10), newest
}

type filterSink struct {
	source.Sink
	since int64
}

func (f filterSink) Response(r usage.Response) {
	if r.EndMs < f.since {
		return
	}
	f.Sink.Response(r)
}

func (p *poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	if p.state == nil {
		p.state = map[string]*dbState{}
	}
	type ref struct {
		path  string
		mtime time.Time
	}
	var refs []ref
	for _, path := range p.databases() {
		_, m := signature(path)
		refs = append(refs, ref{path, m})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if !refs[i].mtime.Equal(refs[j].mtime) {
			return refs[i].mtime.Before(refs[j].mtime)
		}
		return refs[i].path < refs[j].path
	})
	var out source.Sink = sink
	if !p.started {
		out = filterSink{sink, since.UnixMilli()}
	}
	var firstErr error
	for _, r := range refs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := p.pollDB(ctx, r.path, since, d, out, sink); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	p.started = true
	return firstErr
}

type winRow struct {
	id        string
	created   int64
	updated   int64
	acp       bool
	harness   string
	spawnedBy string
}

func columns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func pick(cols map[string]bool, name string) string {
	if cols[name] {
		return name
	}
	return "NULL"
}

func (p *poller) pollDB(ctx context.Context, path string, since time.Time, d *source.Dedup, out, sink source.Sink) error {
	st := p.state[path]
	sig, _ := signature(path)
	if st != nil && st.sig == sig {
		return nil
	}
	db, _, err := sqlitedb.Open(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tables := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			tables[n] = true
		}
	}
	rows.Close()
	if st == nil {
		st = &dbState{wins: map[string]*winState{}}
		p.state[path] = st
	}
	st.sig = sig
	if !tables["session_windows"] || !tables["transcript_events"] {
		return nil // an older schema holds no sessions
	}

	wc, err := columns(ctx, tx, "session_windows")
	if err != nil {
		return err
	}
	ec, err := columns(ctx, tx, "transcript_events")
	if err != nil {
		return err
	}
	q := `SELECT session_id, created_at, ` + pick(wc, "transcript_updated_at") + `, ` + pick(wc, "acp_owned") +
		`, ` + pick(wc, "agent_harness_id") + `, ` + pick(wc, "spawned_by") + ` FROM session_windows ORDER BY created_at, session_id`
	wr, err := tx.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	var wins []winRow
	for wr.Next() {
		var r winRow
		var upd, acp sql.NullInt64
		var harness, spawned sql.NullString
		if err := wr.Scan(&r.id, &r.created, &upd, &acp, &harness, &spawned); err != nil {
			wr.Close()
			return err
		}
		r.updated = r.created
		if upd.Valid {
			r.updated = upd.Int64
		}
		r.acp, r.harness, r.spawnedBy = acp.Int64 == 1, harness.String, spawned.String
		wins = append(wins, r)
	}
	wr.Close()
	if err := wr.Err(); err != nil {
		return err
	}

	gens := map[string]string{}
	if tables["transcript_rewrite_watermarks"] {
		gr, err := tx.QueryContext(ctx, `SELECT session_id, generation FROM transcript_rewrite_watermarks`)
		if err == nil {
			for gr.Next() {
				var id, g string
				if gr.Scan(&id, &g) == nil {
					gens[id] = g
				}
			}
			gr.Close()
		}
	}

	cutoff := since.Add(-15 * time.Minute).UnixMilli()
	for _, r := range wins {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ws := st.wins[r.id]
		fresh := ws == nil
		reset := false
		headerOnly := false
		switch {
		case fresh:
			ws = &winState{w: newWindow(path+"#"+r.id, d), lastSeq: -1, gen: gens[r.id]}
			st.wins[r.id] = ws
			headerOnly = !p.started && r.updated < cutoff
		default:
			gen := gens[r.id]
			advanced := r.updated > ws.updated
			if gen != ws.gen {
				reset = true
			} else if !advanced {
				continue
			} else {
				var maxSeq sql.NullInt64
				if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM transcript_events WHERE session_id = ?`, r.id).Scan(&maxSeq); err != nil {
					return err
				}
				reset = maxSeq.Int64 < ws.lastSeq
			}
			if reset {
				ws.w = newWindow(path+"#"+r.id, d)
				ws.lastSeq = -1
				ws.gen = gen
			}
		}
		ws.w.sink = out
		ws.w.dedup = d
		ws.w.setRow(r.spawnedBy, r.acp, r.harness)
		ws.updated = r.updated
		if err := p.readRows(ctx, tx, ec, r.id, ws, headerOnly); err != nil {
			return err
		}
		sink.Agent(ws.w.state())
	}
	return nil
}

// readRows feeds the rows after ws.lastSeq; headerOnly reads the first
// session entry and then skips to the last row.
func (p *poller) readRows(ctx context.Context, tx *sql.Tx, ec map[string]bool, id string, ws *winState, headerOnly bool) error {
	if headerOnly {
		var maxSeq sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM transcript_events WHERE session_id = ?`, id).Scan(&maxSeq); err != nil {
			return err
		}
		defer func() { ws.lastSeq = max(ws.lastSeq, maxSeq.Int64) }()
	}
	q := `SELECT seq, event_json, ` + pick(ec, "event_zstd") + `, ` + pick(ec, "event_utf8_bytes") +
		` FROM transcript_events WHERE session_id = ? AND seq > ? ORDER BY seq`
	rows, err := tx.QueryContext(ctx, q, id, ws.lastSeq)
	if err != nil {
		return err
	}
	defer rows.Close()
	scanned := 0
	for rows.Next() {
		var seq int64
		var js sql.NullString
		var zst []byte
		var size sql.NullInt64
		if err := rows.Scan(&seq, &js, &zst, &size); err != nil {
			return err
		}
		if !headerOnly {
			ws.lastSeq = seq
		}
		b, ok := p.decode(js, zst, size)
		if !ok {
			continue
		}
		if headerOnly {
			if scanned++; scanned > 64 {
				return nil
			}
			if isHeader(b) {
				ws.w.entry(b)
				return nil
			}
			continue
		}
		ws.w.entry(b)
	}
	return rows.Err()
}

func isHeader(b []byte) bool {
	return gjson.GetBytes(b, "type").String() == "session"
}

// decode returns an entry's JSON text; ok is false for a row that does not
// decode (a malformed line).
func (p *poller) decode(js sql.NullString, zst []byte, size sql.NullInt64) ([]byte, bool) {
	var b []byte
	switch {
	case js.Valid:
		b = []byte(js.String)
	case len(zst) > 0:
		lim := size.Int64
		if lim <= 0 || lim > maxZstdOut {
			return nil, false
		}
		if p.dec == nil {
			dec, err := zstdread.Decoder()
			if err != nil {
				return nil, false
			}
			p.dec = dec
		}
		out, err := p.dec.DecodeAll(zst, make([]byte, 0, min(lim, 1<<16)))
		if err != nil || int64(len(out)) > lim {
			return nil, false
		}
		b = out
	default:
		return nil, false
	}
	if len(b) > maxEntry || !utf8.Valid(b) || !source.IsObject(b) {
		return nil, false
	}
	return b, true
}
