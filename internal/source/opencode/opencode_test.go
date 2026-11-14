package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

const t0 = int64(1792490700000)

func at(s int64) int64 { return t0 + s*1000 }

func mk(t *testing.T, stmts ...string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
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

func poll(t *testing.T, p *poller, since int64, d *source.Dedup) *sourcetest.Sink {
	t.Helper()
	t.Cleanup(p.close) // Windows can't remove a file that is still open
	s := sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.UnixMilli(since), time.UnixMilli(at(100)), d, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestV2Layout(t *testing.T) {
	path, db := mk(t,
		`CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text, parent_id text, slug text, directory text, version text, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message (id text PRIMARY KEY, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)`,
	)
	exec(t, db, `INSERT INTO session_v2 VALUES ('ses_main','p',NULL,'m','/x/nebula','2',?,?)`, at(0), at(48))
	exec(t, db, `INSERT INTO session_v2 VALUES ('ses_sub','p','ses_main','s','/x/nebula','2',?,?)`, at(20), at(30))
	ins := func(id, sid, kind string, seq, c, u int64, data string) {
		exec(t, db, `INSERT INTO session_message VALUES (?,?,?,?,?,?,?)`, id, sid, kind, seq, c, u, data)
	}
	ins("u1", "ses_main", "user", 1, at(1), at(1), `{"text":"x"}`)
	ins("msg_oc1", "ses_main", "assistant", 2, at(10), at(20), fmt.Sprintf(
		`{"time":{"created":%d,"streamed":%d,"completed":%d},"model":{"id":"claude-opus-5-5","providerID":"anthropic"},"finish":"tool-calls","tokens":{"input":4,"output":300,"reasoning":100,"cache":{"read":1000,"write":200}}}`,
		at(10), at(14), at(15)))
	ins("cmp_1", "ses_main", "compaction", 3, at(43), at(47), fmt.Sprintf(
		`{"time":{"created":%d,"completed":%d},"model":{"id":"claude-haiku-5"},"tokens":{"input":5000,"output":800,"reasoning":0,"cache":{"read":0,"write":0}}}`, at(43), at(47)))
	ins("copy", "ses_main", "assistant", 4, at(-100), at(-100), fmt.Sprintf(
		`{"time":{"created":%d,"streamed":%d},"model":{"id":"m"},"tokens":{"input":9,"output":9,"reasoning":0,"cache":{"read":0,"write":0}}}`, at(-100), at(-99)))
	ins("msg_oc3", "ses_sub", "assistant", 1, at(21), at(24), fmt.Sprintf(
		`{"time":{"created":%d,"streamed":%d},"model":{"id":"claude-sonnet-5-5","providerID":"anthropic"},"finish":"tool-calls","tokens":{"input":3,"output":90,"reasoning":30,"cache":{"read":0,"write":500}}}`, at(21), at(24)))
	ins("msg_oc4", "ses_sub", "assistant", 2, at(30), at(30), fmt.Sprintf(
		`{"time":{"created":%d},"model":{"id":"claude-sonnet-5-5","providerID":"anthropic"}}`, at(30)))

	p := &poller{path: path}
	d := source.NewDedup()
	s := poll(t, p, at(-10), d)
	if len(s.Resp) != 3 {
		t.Fatalf("responses = %d: %v", len(s.Resp), s.Order)
	}
	r := s.Resp["opencode:msg_oc1"]
	want := usage.Tokens{Input: 1204, CachedInput: 1000, CacheWrite: 200, Output: 400, Reasoning: 100}
	if r.Tokens != want || r.StartMs != at(10) || r.EndMs != at(14) || r.Provider != "anthropic" || r.Project != "nebula" || !r.Final || r.Model != "claude-opus-5-5" {
		t.Errorf("oc1 = %+v", r)
	}
	if c := s.Resp["opencode:cmp_1"]; c.Input != 5000 || c.Output != 800 || c.EndMs != at(47) || c.StartMs != at(43) {
		t.Errorf("cmp = %+v", c)
	}
	if r := s.Resp["opencode:msg_oc3"]; r.Input != 503 || r.Output != 120 || r.Reasoning != 30 {
		t.Errorf("oc3 = %+v", r)
	}
	main := s.Agents[path+"#ses_main"]
	if main.Phase != usage.Tools || main.Subagent || main.Model != "claude-haiku-5" && main.Model != "claude-opus-5-5" || main.Context != 1204 {
		t.Errorf("main = %+v", main)
	}
	sub := s.Agents[path+"#ses_sub"]
	if sub.Phase != usage.Generating || sub.InFlightMs != at(30) || !sub.Subagent || sub.Project != "nebula" || sub.Context != 503 {
		t.Errorf("sub = %+v", sub)
	}

	// incremental: the in-flight message finishes, an idle row follows
	exec(t, db, `UPDATE session_message SET time_updated=?, data=? WHERE id='msg_oc4'`, at(60), fmt.Sprintf(
		`{"time":{"created":%d,"streamed":%d},"model":{"id":"claude-sonnet-5-5","providerID":"anthropic"},"finish":"stop","tokens":{"input":10,"output":50,"reasoning":0,"cache":{"read":90,"write":0}}}`, at(30), at(34)))
	s = poll(t, p, at(-10), d)
	if len(s.Resp) > 2 {
		t.Fatalf("incremental responses = %d", len(s.Resp))
	}
	if r := s.Resp["opencode:msg_oc4"]; r.Input != 100 || r.CachedInput != 90 || r.EndMs != at(34) {
		t.Errorf("oc4 = %+v", r)
	}
	if sub := s.Agents[path+"#ses_sub"]; sub.Phase != usage.Idle || sub.Context != 100 || sub.InFlightMs != 0 {
		t.Errorf("sub after = %+v", sub)
	}
	if len(s.Agents) > 2 {
		t.Errorf("only touched sessions are reported, got %d", len(s.Agents))
	}
	// nothing changes: the same rows are read again, upserts only
	s = poll(t, p, at(-10), d)
	if len(s.Resp) > 1 {
		t.Errorf("idle poll reread %d responses", len(s.Resp))
	}
}

func TestLegacyLayout(t *testing.T) {
	path, db := mk(t,
		"CREATE TABLE `session` (id text PRIMARY KEY, project_id text, parent_id text, slug text, directory text, title text, version text, time_created integer, time_updated integer)",
		"CREATE TABLE `message` (id text PRIMARY KEY, session_id text, time_created integer, time_updated integer, data text)",
	)
	exec(t, db, `INSERT INTO session VALUES ('ses_a','p',NULL,'a','/home/me/proj','t','1',?,?)`, at(0), at(30))
	ins := func(id string, c, u int64, data string) {
		exec(t, db, `INSERT INTO message VALUES (?,?,?,?,?)`, id, "ses_a", c, u, data)
	}
	ins("m1", at(1), at(1), `{"role":"user","time":{"created":1}}`)
	ins("m2", at(2), at(8), fmt.Sprintf(
		`{"role":"assistant","time":{"created":%d,"completed":%d},"modelID":"gpt-5.4","providerID":"tuyoo","finish":"tool-calls","cost":0.5,"tokens":{"total":14145,"input":13792,"output":353,"reasoning":10,"cache":{"read":100,"write":0}}}`, at(2), at(8)))
	ins("m3", at(9), at(9), fmt.Sprintf(`{"role":"assistant","time":{"created":%d},"modelID":"gpt-5.4","providerID":"tuyoo","tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}`, at(9)))

	p := &poller{path: path}
	s := poll(t, p, at(-10), source.NewDedup())
	if len(s.Resp) > 2 {
		t.Fatalf("responses = %v", s.Order)
	}
	r := s.Resp["opencode:m2"]
	if r.Input != 13892 || r.CachedInput != 100 || r.Output != 363 || r.Reasoning != 10 || r.Model != "gpt-5.4" || r.Provider != "tuyoo" || r.Project != "proj" || r.StartMs != at(2) || r.EndMs != at(8) {
		t.Errorf("m2 = %+v", r)
	}
	a := s.Agents[path+"#ses_a"]
	if a.Phase != usage.Generating || a.InFlightMs != at(9) || a.Context != 13892 || a.Model != "gpt-5.4" || a.LastLineMs != at(9) {
		t.Errorf("agent = %+v", a)
	}
	// responses ended before since are not read on the first poll
	s = poll(t, &poller{path: path}, at(20), source.NewDedup())
	if len(s.Resp) != 0 {
		t.Errorf("old responses read: %v", s.Order)
	}
}

func TestMissingAndUnsupported(t *testing.T) {
	p := &poller{path: filepath.Join(t.TempDir(), "none.db")}
	if s := poll(t, p, 0, nil); len(s.Resp) != 0 {
		t.Error("responses from nothing")
	}
	path, _ := mk(t, `CREATE TABLE other (a text)`)
	if s := poll(t, &poller{path: path}, 0, nil); len(s.Resp) != 0 || len(s.Agents) != 0 {
		t.Error("unsupported database contributed")
	}
}

func TestReadOnlyNoSidecars(t *testing.T) {
	path, db := mk(t,
		`PRAGMA journal_mode=WAL`,
		`CREATE TABLE session_v2 (id text PRIMARY KEY, project_id text, parent_id text, slug text, directory text, version text, time_created integer, time_updated integer)`,
		`CREATE TABLE session_message (id text PRIMARY KEY, session_id text, type text, seq integer, time_created integer, time_updated integer, data text)`,
	)
	db.Close() // a clean close removes -wal and -shm
	for _, x := range []string{"-wal", "-shm"} {
		os.Remove(path + x)
	}
	p := &poller{path: path}
	poll(t, p, 0, nil)
	p.close()
	for _, x := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + x); err == nil {
			t.Errorf("%s was created", x)
		}
	}
}

// The spec's own fixture, when Agent Speed's repository is next door.
func TestSpecFixture(t *testing.T) {
	path := "/Users/tu/code/github/agent-speed/spec/fixtures/opencode/opencode.db"
	if _, err := os.Stat(path); err != nil {
		t.Skip("no spec fixture")
	}
	// work on a copy: the fixture itself must stay untouched
	cp := filepath.Join(t.TempDir(), "opencode.db")
	b, _ := os.ReadFile(path)
	os.WriteFile(cp, b, 0o644)
	s := poll(t, &poller{path: cp}, 1792490000000, source.NewDedup())
	r := s.Resp["opencode:msg_oc1"]
	if r.Input != 1204 || r.Output != 400 || r.StartMs != 1792490701010 || r.EndMs != 1792490705010 {
		t.Errorf("oc1 = %+v", r)
	}
	if _, ok := s.Resp["opencode:msg_copy"]; ok {
		t.Error("fork copy counted")
	}
	if len(s.Resp) != 4 { // oc1, oc2, cmp_1, oc3
		t.Errorf("responses = %v", s.Order)
	}
	if a := s.Agents[cp+"#ses_sub"]; a.Phase != usage.Generating || !a.Subagent {
		t.Errorf("sub = %+v", a)
	}
	if a := s.Agents[cp+"#ses_main"]; a.Phase != usage.Idle {
		t.Errorf("main = %+v", a)
	}
}
