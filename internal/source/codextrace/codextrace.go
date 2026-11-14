// Package codextrace finds the Codex requests that ran at the priority
// service tier ("fast mode") in Codex's own trace database
// (~/.codex/logs_*.sqlite, table logs).
//
// Only markers are read, never kept: a request's turn id, thread id,
// model and timestamp. The request bodies the table holds are parsed in
// memory for service_tier and dropped. The database is opened read-only
// through sqlitedb, and scanned incrementally by row id.
//
// Evidence of priority, as CodexBar reads it (CostUsageScanner+CodexPriority.swift):
//   - a "websocket request:" row whose JSON has type "response.create"
//     and service_tier "priority"; the turn id is turn.id= / turn_id= in
//     the row's span prefix, or the JSON's turn_id;
//   - a "Submission sub=Submission {" row containing
//     service_tier: Some(Some("priority")); the turn is the submission id;
//   - a tag row ("service_tier":"priority") whose span prefix names
//     turn_id= and model=, the form current Codex writes.
//
// No evidence means standard: nothing here guesses.
package codextrace

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
)

// Turn is what is known of one priority turn.
type Turn struct {
	Thread string
	Model  string
	TsSec  int64
}

type cursor struct {
	lastID int64
}

// Trace is the incremental reader of a Codex home's trace databases.
type Trace struct {
	dir      string
	since    time.Time
	interval time.Duration

	mu      sync.Mutex
	last    time.Time
	cursors map[string]*cursor
	turns   map[string]Turn
}

// New reads the logs_*.sqlite files in codexHome, looking at rows
// stamped at or after since.
func New(codexHome string, since time.Time) *Trace {
	return &Trace{dir: codexHome, since: since, interval: 2 * time.Second,
		cursors: map[string]*cursor{}, turns: map[string]Turn{}}
}

var (
	defMu sync.Mutex
	defs  = map[string]*Trace{}
)

// ForHome is the shared Trace of a Codex home, made on first use. It
// reads the last 8 days.
func ForHome(codexHome string) *Trace {
	defMu.Lock()
	defer defMu.Unlock()
	t := defs[codexHome]
	if t == nil {
		t = New(codexHome, time.Now().AddDate(0, 0, -8))
		defs[codexHome] = t
	}
	return t
}

// Refresh scans rows added since the last call, at most once per
// interval. It reports whether it ran a scan.
func (t *Trace) Refresh() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.last.IsZero() && time.Since(t.last) < t.interval {
		return false
	}
	t.last = time.Now()
	files, _ := filepath.Glob(filepath.Join(t.dir, "logs_*.sqlite"))
	for _, f := range files {
		if fi, err := os.Stat(f); err != nil || fi.IsDir() {
			continue
		}
		t.scan(f)
	}
	return true
}

// Priority reports whether the turn ran at the priority tier. thread
// may be empty; when both sides know it, they must agree.
func (t *Trace) Priority(thread, turn string) bool {
	if turn == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.turns[turn]
	if !ok {
		return false
	}
	return thread == "" || m.Thread == "" || m.Thread == thread
}

// Turns is how many priority turns are known.
func (t *Trace) Turns() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.turns)
}

func (t *Trace) scan(path string) {
	db, _, err := sqlitedb.Open(path)
	if err != nil {
		return
	}
	defer db.Close()
	cur := t.cursors[path]
	if cur == nil {
		cur = &cursor{}
		t.cursors[path] = cur
	}
	var maxID sql.NullInt64
	if db.QueryRow(`select max(id) from logs`).Scan(&maxID) != nil {
		return
	}
	if maxID.Valid && maxID.Int64 < cur.lastID {
		cur.lastID = 0 // the table was rebuilt
	}
	rows, err := db.Query(`select id, ts, thread_id, feedback_log_body from logs
		where id > ? and ts >= ? and feedback_log_body like '%priority%'
		and (feedback_log_body like '%websocket request:%'
		  or feedback_log_body like '%service_tier: Some(Some("priority"))%'
		  or feedback_log_body like '%"service_tier":"priority"%')
		order by id`, cur.lastID, t.since.Unix())
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, ts int64
		var thread, body sql.NullString
		if rows.Scan(&id, &ts, &thread, &body) != nil {
			return
		}
		cur.lastID = max(cur.lastID, id)
		if !body.Valid {
			continue
		}
		if turn, m, ok := ParseRow(body.String); ok {
			if m.Thread == "" {
				m.Thread = thread.String
			}
			m.TsSec = ts
			t.turns[turn] = m
		}
	}
	// ids up to the largest seen have been looked at; rows that did not
	// match the filter are skipped for good
	if maxID.Valid && rows.Err() == nil {
		cur.lastID = max(cur.lastID, maxID.Int64)
	}
}

// ParseRow reads one log body: when it is evidence of a priority
// request, the turn id and what else the row says. The body is not kept.
func ParseRow(body string) (turn string, m Turn, ok bool) {
	if i := strings.Index(body, "websocket request:"); i >= 0 {
		prefix := body[:i]
		req := strings.TrimSpace(body[i+len("websocket request:"):])
		if !gjson.Valid(req) {
			return "", m, false
		}
		j := gjson.Parse(req)
		if j.Get("type").Str != "response.create" || j.Get("service_tier").Str != "priority" {
			return "", m, false
		}
		turn = spanValue(prefix, "turn.id")
		if turn == "" {
			turn = spanValue(prefix, "turn_id")
		}
		if turn == "" {
			turn = j.Get("turn_id").Str
		}
		if turn == "" {
			return "", m, false
		}
		return turn, Turn{Thread: spanValue(prefix, "thread_id"), Model: j.Get("model").Str}, true
	}
	if strings.Contains(body, `service_tier: Some(Some("priority"))`) {
		i := strings.Index(body, "Submission sub=Submission {")
		if i < 0 {
			return "", m, false
		}
		sub := body[i+len("Submission sub=Submission {"):]
		k := strings.Index(sub, `id: "`)
		if k < 0 {
			return "", m, false
		}
		rest := sub[k+len(`id: "`):]
		end := strings.IndexByte(rest, '"')
		if end <= 0 {
			return "", m, false
		}
		return rest[:end], Turn{Thread: spanValue(body[:i], "thread_id")}, true
	}
	if strings.Contains(body, `"service_tier":"priority"`) {
		turn = spanValue(body, "turn_id")
		if turn == "" {
			return "", m, false
		}
		return turn, Turn{Thread: spanValue(body, "thread_id"), Model: spanValue(body, "model")}, true
	}
	return "", m, false
}

// spanValue reads name=value from a tracing span prefix: the value ends
// at white space or one of , ] ) } :. A quoted value loses its quotes.
func spanValue(s, name string) string {
	for from := 0; ; {
		i := strings.Index(s[from:], name+"=")
		if i < 0 {
			return ""
		}
		i += from
		if i > 0 {
			if c := s[i-1]; c != ' ' && c != '{' && c != ',' && c != '.' && c != '\t' && c != '(' && c != '[' && c != ':' {
				from = i + len(name) + 1 // part of a longer name
				continue
			}
		}
		v := s[i+len(name)+1:]
		v = strings.TrimPrefix(v, `"`)
		end := strings.IndexAny(v, " \t\r\n,])}:\"")
		if end < 0 {
			end = len(v)
		}
		return v[:end]
	}
}
