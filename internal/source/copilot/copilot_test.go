package copilot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
)

const fullSchema = `CREATE TABLE assistant_usage_events (id INTEGER PRIMARY KEY, session_id TEXT, model TEXT,
 input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER,
 reasoning_tokens INTEGER, total_nano_aiu INTEGER, request_multiplier REAL, initiator TEXT,
 token_details_json TEXT, created_at TEXT, agent_id TEXT, duration_ms INTEGER,
 time_to_first_token_ms INTEGER, reasoning_effort TEXT)`

const sessSchema = `CREATE TABLE sessions (id TEXT PRIMARY KEY, cwd TEXT, repository TEXT, summary TEXT, created_at TEXT, updated_at TEXT)`

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func mk(t *testing.T, stmts ...string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session-store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec(t, db, stmts...)
	return path, db
}

func exec(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err, s)
		}
	}
}

func poll(t *testing.T, p *poller, since string, d *source.Dedup) *sourcetest.Sink {
	t.Helper()
	t.Cleanup(p.close) // Windows can't remove a file that is still open
	s := sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.UnixMilli(ms(since)), time.UnixMilli(ms("2026-11-14T10:00:00Z")), d, s); err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	long = "2026-11-14T00:00:00Z"
	// id, session, model, in, out, cr, cw, rs, nano, mult, initiator, details, created, agent, dur, ttft, effort
	ins = `INSERT INTO assistant_usage_events VALUES `
)

func fixture(t *testing.T) (string, *sql.DB) {
	return mk(t, fullSchema, sessSchema,
		`INSERT INTO sessions VALUES ('s1','/work/nebula','o/nebula','x','2026-11-14T08:00:00Z','2026-11-14T09:00:00Z')`,
		ins+`(1,'s1','claude-sonnet-5-5',10000,500,8000,1000,200,5,1.0,NULL,'{}','2026-11-14T09:00:12.500Z',NULL,9500,1800,'high')`,
		ins+`(2,'s1','claude-haiku-5',20000,800,0,0,0,5,0.33,'compaction','{}','2026-11-14T09:01:00Z',NULL,5000,500,NULL)`,
		ins+`(3,'s1','claude-sonnet-5-5',3000,100,0,0,0,5,1.0,'sub-agent','{}','2026-11-14 09:02:10',  'ag1',4000,1000,NULL)`,
		ins+`(4,'s1','claude-sonnet-5-5',4000,50,0,0,0,5,1.0,NULL,'{}','2026-11-14T09:03:00Z',NULL,NULL,NULL,NULL)`,
		ins+`(5,'s1','m',0,0,0,0,0,0,1.0,NULL,'{}','2026-11-14T09:04:00Z',NULL,1000,100,NULL)`,
		ins+`(6,'','m',9,9,0,0,0,0,1.0,NULL,'{}','2026-11-14T09:04:00Z',NULL,1000,100,NULL)`,
		ins+`(7,'s1','m',9,9,0,0,0,0,1.0,NULL,'{}','garbage',NULL,1000,100,NULL)`,
	)
}

func TestMappingAndSpeed(t *testing.T) {
	path, _ := fixture(t)
	p := &poller{path: path}
	s := poll(t, p, long, source.NewDedup())
	if n := len(s.Responses()); n != 4 {
		t.Fatalf("%d responses, want 4: %+v", n, s.Responses())
	}
	r := s.Resp["copilot:s1:1"]
	if r.Source != Copilot || r.Project != "nebula" || r.Model != "claude-sonnet-5-5" || r.Effort != "high" || !r.Final {
		t.Fatalf("%+v", r)
	}
	if r.EndMs != ms("2026-11-14T09:00:12.500Z") || r.StartMs != ms("2026-11-14T09:00:03.000Z") || r.FirstMs != ms("2026-11-14T09:00:04.800Z") {
		t.Fatalf("times %d %d %d", r.StartMs, r.FirstMs, r.EndMs)
	}
	if r.Input != 10000 || r.CachedInput != 8000 || r.CacheWrite != 1000 || r.Reasoning != 200 || r.Output != 700 {
		t.Fatalf("tokens %+v", r.Tokens)
	}
	if !r.Measured() {
		t.Fatal("not measured")
	}

	c := s.Resp["copilot:s1:2"] // compaction: tokens only
	if c.StartMs != 0 || c.FirstMs != 0 || c.Input != 20000 || c.Output != 800 || c.Measured() {
		t.Fatalf("compaction %+v", c)
	}

	sub := s.Resp["copilot:s1:3"] // zoneless time is UTC
	if sub.EndMs != ms("2026-11-14T09:02:10Z") || sub.StartMs != sub.EndMs-4000 || sub.Agent == r.Agent {
		t.Fatalf("sub-agent %+v", sub)
	}

	nd := s.Resp["copilot:s1:4"] // no duration
	if nd.StartMs != 0 || nd.Output != 50 || nd.Measured() {
		t.Fatalf("no duration %+v", nd)
	}

	main, ok := s.Agents[r.Agent]
	if !ok || main.Subagent || main.Model != "claude-sonnet-5-5" || main.Project != "nebula" ||
		main.LastLineMs != ms("2026-11-14T09:03:00Z") || main.Context != 4000 || main.Phase != "idle" {
		t.Fatalf("state %+v", main)
	}
	if sa := s.Agents[sub.Agent]; !sa.Subagent || sa.Context != 3000 {
		t.Fatalf("sub state %+v", sa)
	}
}

func TestOldSchemaMissingColumns(t *testing.T) {
	path, _ := mk(t,
		`CREATE TABLE assistant_usage_events (id INTEGER PRIMARY KEY, session_id TEXT, model TEXT, input_tokens INTEGER, output_tokens INTEGER, created_at TEXT)`,
		ins+`(1,'s1','m',100,20,'2026-11-14T09:00:00Z')`,
	)
	s := poll(t, &poller{path: path}, long, nil)
	r, ok := s.Resp["copilot:s1:1"]
	if !ok || r.Input != 100 || r.Output != 20 || r.StartMs != 0 || r.Project != "" {
		t.Fatalf("%+v", s.Resp)
	}
}

func TestNoUsageTable(t *testing.T) {
	path, _ := mk(t, sessSchema)
	s := poll(t, &poller{path: path}, long, nil)
	if len(s.Responses()) != 0 {
		t.Fatal("responses from a database without usage")
	}
}

func TestFirstPollSince(t *testing.T) {
	path, _ := fixture(t)
	s := poll(t, &poller{path: path}, "2026-11-14T09:02:00Z", nil)
	if len(s.Resp) != 2 || s.Resp["copilot:s1:3"].Key == "" || s.Resp["copilot:s1:4"].Key == "" {
		t.Fatalf("%v", s.Order)
	}
}

func TestIncrementalAndDedup(t *testing.T) {
	path, db := fixture(t)
	d := source.NewDedup()
	p := &poller{path: path}
	s := poll(t, p, long, d)
	if len(s.Resp) != 4 {
		t.Fatal(len(s.Resp))
	}
	// a second poller on a copy of the database (another path, as with a
	// WSL home) claims nothing: the keys belong to the first
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "session-store.db")
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if s2 := poll(t, &poller{path: copyPath}, long, d); len(s2.Resp) != 0 {
		t.Fatalf("dedup: %v", s2.Order)
	}
	// nothing new: nothing emitted
	s3 := sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.UnixMilli(ms(long)), time.Now(), d, s3); err != nil || len(s3.Resp)+len(s3.Agents) != 0 {
		t.Fatal(err, s3.Order)
	}
	exec(t, db, ins+`(8,'s1','m',500,60,0,0,0,0,1.0,NULL,'{}','2026-11-14T09:05:00Z',NULL,2000,400,NULL)`)
	s4 := sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.UnixMilli(ms(long)), time.Now(), d, s4); err != nil {
		t.Fatal(err)
	}
	if len(s4.Resp) != 1 || s4.Resp["copilot:s1:8"].Output != 60 {
		t.Fatalf("%v", s4.Order)
	}
	if len(s4.Agents) != 1 {
		t.Fatalf("%v", s4.Agents)
	}
}

func TestMissingDB(t *testing.T) {
	p := &poller{path: filepath.Join(t.TempDir(), "none.db")}
	s := poll(t, p, long, nil)
	if len(s.Resp) != 0 {
		t.Fatal("responses without a database")
	}
}

func TestDBReset(t *testing.T) {
	path, db := fixture(t)
	p := &poller{path: path}
	poll(t, p, long, nil)
	exec(t, db, `DELETE FROM assistant_usage_events`,
		ins+`(1,'s9','m',70,7,0,0,0,0,1.0,NULL,'{}','2026-11-14T09:30:00Z',NULL,1000,100,NULL)`)
	s := sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.UnixMilli(ms(long)), time.Now(), nil, s); err != nil {
		t.Fatal(err)
	}
	if r, ok := s.Resp["copilot:s9:1"]; !ok || r.Input != 70 {
		t.Fatalf("%v", s.Order)
	}
}

func TestDBPathEnv(t *testing.T) {
	t.Setenv("COPILOT_HOME", filepath.Join("x", "cp"))
	if got, want := DBPath(), filepath.Join("x", "cp", "session-store.db"); got != want {
		t.Fatal(got)
	}
}
