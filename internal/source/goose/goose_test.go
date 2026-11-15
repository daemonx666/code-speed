package goose

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
)

const endS = int64(1792490700)

func mk(t *testing.T, stmts ...string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err, s)
		}
	}
	return path, db
}

func v18(t *testing.T) (string, *sql.DB) {
	return mk(t,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, working_dir TEXT, model_config_json TEXT, parent_session_id TEXT)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, message_id TEXT, session_id TEXT, role TEXT, content_json TEXT, created_timestamp INTEGER, metadata_json TEXT)`,
		`INSERT INTO sessions VALUES ('s1','/home/u/proj','{"model_name":"fallback-model"}',NULL)`,
		`INSERT INTO sessions VALUES ('s2','/home/u/proj','{}','s1')`,
	)
}

func add(t *testing.T, db *sql.DB, sess, mid, role string, ts int64, meta string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO messages (message_id, session_id, role, content_json, created_timestamp, metadata_json) VALUES (?,?,?,?,?,?)`,
		mid, sess, role, "[]", ts, meta); err != nil {
		t.Fatal(err)
	}
}

func poll(t *testing.T, p *poller, d *source.Dedup) *sourcetest.Sink {
	t.Helper()
	t.Cleanup(p.close)
	s := sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.UnixMilli(0), time.Now(), d, s); err != nil {
		t.Fatal(err)
	}
	return s
}

const full = `{"usage":{"inputTokens":1000,"outputTokens":200,"totalTokens":1200,"cacheReadTokens":400,"cacheWriteTokens":100,"cost":0.01,"elapsedMs":4000,"timeToFirstTokenMs":500},"inference":{"provider":"anthropic","requestedModel":"req","resolvedModel":"claude-x"}}`

func TestPoll(t *testing.T) {
	path, db := v18(t)
	add(t, db, "s1", "m1", "assistant", endS, full)
	add(t, db, "s1", "m2", "assistant", endS+10, `{"usage":{"inputTokens":50,"outputTokens":7}}`)
	add(t, db, "s1", "m3", "assistant", endS*1000+20000, `{"usage":{"inputTokens":5,"outputTokens":3}}`)
	add(t, db, "s1", "m4", "assistant", endS+30, `{"usage":{"inputTokens":5,"outputTokens":0}}`)
	add(t, db, "s1", "m5", "user", endS+40, `{"usage":{"inputTokens":5,"outputTokens":9}}`)
	add(t, db, "s2", "c1", "assistant", endS+50, `{"usage":{"inputTokens":9,"outputTokens":4},"inference":{"requestedModel":"req"}}`)
	p := &poller{path: path}
	d := source.NewDedup()
	s := poll(t, p, d)

	r := s.Resp["goose:s1:m1"]
	end := endS * 1000
	if r.Input != 1000 || r.CachedInput != 400 || r.CacheWrite != 100 || r.Output != 200 || r.Reasoning != 0 {
		t.Fatalf("tokens %+v", r.Tokens)
	}
	if r.EndMs != end || r.StartMs != end-4000 || r.FirstMs != end-3500 {
		t.Fatalf("times %d %d %d", r.StartMs, r.EndMs, r.FirstMs)
	}
	if r.Model != "claude-x" || r.Provider != "anthropic" || r.Project != "proj" || !r.Final || r.Source != Goose {
		t.Fatalf("meta %+v", r)
	}
	r = s.Resp["goose:s1:m2"]
	if r.StartMs != 0 || r.FirstMs != 0 || r.Output != 7 || r.Model != "fallback-model" || r.EndMs != (endS+10)*1000 {
		t.Fatalf("tokens-only %+v", r)
	}
	if r := s.Resp["goose:s1:m3"]; r.EndMs != endS*1000+20000 {
		t.Fatalf("ms timestamp %d", r.EndMs)
	}
	for _, k := range []string{"goose:s1:m4", "goose:s1:m5"} {
		if _, ok := s.Resp[k]; ok {
			t.Fatalf("%s should be dropped", k)
		}
	}
	if c := s.Resp["goose:s2:c1"]; c.Model != "req" {
		t.Fatalf("child %+v", c)
	}
	if len(s.Resp) != 4 {
		t.Fatalf("got %d responses", len(s.Resp))
	}
	if a := s.Agents[path+"#s2"]; !a.Subagent {
		t.Fatalf("s2 not subagent: %+v", a)
	}
	if a := s.Agents[path+"#s1"]; a.Subagent || a.Model == "" {
		t.Fatalf("s1 state %+v", a)
	}

	// second poll: nothing; then a new row
	s = poll(t, p, d)
	if len(s.Resp) != 0 {
		t.Fatalf("second poll got %d", len(s.Resp))
	}
	add(t, db, "s1", "m6", "assistant", endS+60, `{"usage":{"inputTokens":1,"outputTokens":2}}`)
	s = poll(t, p, d)
	if len(s.Resp) != 1 || s.Resp["goose:s1:m6"].Output != 2 {
		t.Fatalf("new row: %+v", s.Resp)
	}
}

func TestMissingAndOldSchema(t *testing.T) {
	p := &poller{path: filepath.Join(t.TempDir(), "none.db")}
	if s := poll(t, p, source.NewDedup()); len(s.Resp) != 0 {
		t.Fatal("missing db yielded rows")
	}
	path, _ := mk(t,
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, working_dir TEXT)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content_json TEXT, created_timestamp INTEGER)`,
		`INSERT INTO messages VALUES (1,'s','assistant','[]',1)`,
	)
	if s := poll(t, &poller{path: path}, source.NewDedup()); len(s.Resp) != 0 {
		t.Fatal("old schema yielded rows")
	}
}

func TestGoosePathRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOOSE_PATH_ROOT", root)
	want := filepath.Join(root, "data", "sessions", "sessions.db")
	if got := candidates()[0]; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if got := DBPath(); got != want {
		t.Fatalf("DBPath %s", got)
	}
	t.Setenv("GOOSE_PATH_ROOT", "relative/dir")
	if got := candidates()[0]; got == filepath.Join("relative/dir", "data", "sessions", "sessions.db") {
		t.Fatal("relative root honored")
	}
}
