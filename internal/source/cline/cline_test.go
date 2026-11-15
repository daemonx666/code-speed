package cline

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func testEnv(home string, vars map[string]string) *env {
	return &env{home: home, goos: "linux", getenv: func(k string) string { return vars[k] }}
}

func poll(t *testing.T, p *poller, d *source.Dedup, sink source.Sink) {
	t.Helper()
	if err := p.Poll(context.Background(), time.UnixMilli(0), time.Now(), d, sink); err != nil {
		t.Fatal(err)
	}
}

// aHome has the legacy fixture under VS Code's globalStorage.
func aHome(t *testing.T) (home, gs string) {
	home = t.TempDir()
	gs = filepath.Join(home, ".config", "Code", "User", "globalStorage", clineExt)
	copyTree(t, "testdata/a", gs)
	return
}

func TestFormatA(t *testing.T) {
	home, gs := aHome(t)
	p := &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink := sourcetest.NewSink()
	poll(t, p, source.NewDedup(), sink)
	rs := sink.Responses()
	if len(rs) != 2 {
		t.Fatalf("got %d responses, want 2 (in-flight and partial skipped): %+v", len(rs), rs)
	}
	r1, r2 := rs[0], rs[1]
	if r1.Key != "cline:a:t1:1000000" || r2.Key != "cline:a:t1:1020000" {
		t.Errorf("keys %s %s", r1.Key, r2.Key)
	}
	if r1.Tokens != (usage.Tokens{Input: 160, CachedInput: 50, CacheWrite: 10, Output: 120}) {
		t.Errorf("r1 tokens %+v", r1.Tokens)
	}
	if r1.StartMs != 1000000 || r1.EndMs != 1010000 || r2.StartMs != 1020000 || r2.EndMs != 1027900 {
		t.Errorf("timing %d-%d %d-%d", r1.StartMs, r1.EndMs, r2.StartMs, r2.EndMs)
	}
	if out := r1.Output + r2.Output; out != 263 {
		t.Errorf("output %d, want 263", out)
	}
	if dur := (r1.EndMs - r1.StartMs) + (r2.EndMs - r2.StartMs); dur != 17900 {
		t.Errorf("duration %d", dur)
	}
	if !r1.Measured() || !r2.Measured() {
		t.Error("not measured")
	}
	if r1.Model != "claude-sonnet-4" || r1.Provider != "anthropic" {
		t.Errorf("r1 model %q/%q", r1.Model, r1.Provider)
	}
	// model fallback: no modelInfo anywhere but task_metadata.json
	if r2.Model != "claude-fallback" || r2.Provider != "cline" {
		t.Errorf("r2 model %q/%q", r2.Model, r2.Provider)
	}
	if r1.Project != "nebula" || r1.Agent != filepath.Join(gs, "tasks", "t1", "ui_messages.json") {
		t.Errorf("project %q agent %q", r1.Project, r1.Agent)
	}
}

func TestInFlightBecomesComplete(t *testing.T) {
	home, gs := aHome(t)
	p := &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink := sourcetest.NewSink()
	poll(t, p, source.NewDedup(), sink)
	ui := filepath.Join(gs, "tasks", "t1", "ui_messages.json")
	os.WriteFile(ui, []byte(`[{"ts":5,"type":"say","say":"api_req_started","text":"{\"request\":\"q\"}"}]`), 0o644)
	sink2 := sourcetest.NewSink()
	poll(t, p, source.NewDedup(), sink2)
	if n := len(sink2.Responses()); n != 0 {
		t.Errorf("in-flight request produced %d responses", n)
	}
}

func TestMismatchedSanityUnmeasured(t *testing.T) {
	home, gs := aHome(t)
	api := filepath.Join(gs, "tasks", "t1", "api_conversation_history.json")
	os.WriteFile(api, []byte(`[{"role":"assistant","ts":1010000,"metrics":{"tokens":{"completion":999}}}]`), 0o644)
	p := &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink := sourcetest.NewSink()
	poll(t, p, source.NewDedup(), sink)
	r := sink.Responses()[0]
	if r.StartMs != 0 || r.Measured() {
		t.Errorf("mismatch should be unmeasured: %+v", r)
	}
}

func TestRooUnmeasuredUnlessCountsMatch(t *testing.T) {
	home := t.TempDir()
	gs := filepath.Join(home, ".config", "Code", "User", "globalStorage", rooExt)
	copyTree(t, "testdata/a", gs)
	// the fixture has 3 assistant entries for 2 requests: unmeasured
	p := &poller{k: kind{src: Roo, ext: rooExt}, e: testEnv(home, nil)}
	sink := sourcetest.NewSink()
	poll(t, p, source.NewDedup(), sink)
	rs := sink.Responses()
	if len(rs) != 2 || rs[0].Source != Roo || rs[0].Key != "roo-code:a:t1:1000000" {
		t.Fatalf("%+v", rs)
	}
	for _, r := range rs {
		if r.Measured() {
			t.Errorf("measured despite count mismatch: %+v", r)
		}
	}
	// now one assistant entry per request
	api := filepath.Join(gs, "tasks", "t1", "api_conversation_history.json")
	os.WriteFile(api, []byte(`[{"role":"assistant","ts":1010000},{"role":"user","ts":1011000},{"role":"assistant","ts":1027900}]`), 0o644)
	p = &poller{k: kind{src: Roo, ext: rooExt}, e: testEnv(home, nil)}
	sink = sourcetest.NewSink()
	poll(t, p, source.NewDedup(), sink)
	for _, r := range sink.Responses() {
		if !r.Measured() {
			t.Errorf("not measured: %+v", r)
		}
	}
}

func TestFormatB(t *testing.T) {
	home := t.TempDir()
	copyTree(t, "testdata/b", filepath.Join(home, ".cline", "data"))
	p := &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink := sourcetest.NewSink()
	poll(t, p, source.NewDedup(), sink)
	main, ok := sink.Resp["cline:b:s1:m4"]
	if !ok {
		t.Fatalf("missing m4: %v", sink.Order)
	}
	if main.Output != 164 || main.EndMs-main.StartMs != 4859 || !main.Measured() {
		t.Errorf("m4 %+v", main)
	}
	if main.Tokens != (usage.Tokens{Input: 35912, CachedInput: 34332, CacheWrite: 7, Output: 164}) {
		t.Errorf("m4 tokens %+v", main.Tokens)
	}
	if main.Model != "anthropic/claude-opus-4.8-fast" || main.Provider != "cline" {
		t.Errorf("model %q/%q", main.Model, main.Provider)
	}
	first := sink.Resp["cline:b:s1:m2"]
	if first.StartMs != 1783376868634 || first.EndMs != 1783376873493 {
		t.Errorf("m2 %+v", first)
	}
	sub, ok := sink.Resp["cline:b:s1.sub1:m2"]
	if !ok || sub.Output != 20 || sub.EndMs-sub.StartMs != 2000 {
		t.Errorf("sub-agent %+v (%v)", sub, ok)
	}
	if len(sink.Responses()) != 3 {
		t.Errorf("got %d responses", len(sink.Responses()))
	}
}

func TestDedupAcrossStores(t *testing.T) {
	home, _ := aHome(t)
	copyTree(t, "testdata/a", filepath.Join(home, ".cline", "data"))
	p := &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink := &countSink{Sink: sourcetest.NewSink()}
	poll(t, p, source.NewDedup(), sink)
	if sink.n != 2 {
		t.Errorf("sink saw %d responses, want 2", sink.n)
	}
}

type countSink struct {
	*sourcetest.Sink
	n int
}

func (c *countSink) Response(r usage.Response) { c.n++; c.Sink.Response(r) }

func TestRereadOnlyOnChange(t *testing.T) {
	home, gs := aHome(t)
	p := &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink := &countSink{Sink: sourcetest.NewSink()}
	d := source.NewDedup()
	poll(t, p, d, sink)
	poll(t, p, d, sink)
	if sink.n != 2 {
		t.Fatalf("unchanged file reread: %d responses", sink.n)
	}
	ui := filepath.Join(gs, "tasks", "t1", "ui_messages.json")
	mt := time.Now().Add(time.Hour)
	if err := os.Chtimes(ui, mt, mt); err != nil {
		t.Fatal(err)
	}
	poll(t, p, d, sink)
	if sink.n != 4 {
		t.Errorf("changed file not reread: %d", sink.n)
	}
}

func TestSince(t *testing.T) {
	home, _ := aHome(t)
	p := &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink := sourcetest.NewSink()
	// request 1 ended at 1010000, request 2 at 1027900
	if err := p.Poll(context.Background(), time.UnixMilli(1020000), time.Now(), source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	if rs := sink.Responses(); len(rs) != 1 || rs[0].EndMs != 1027900 {
		t.Errorf("since filter: %+v", rs)
	}
	// a file older than since is not read
	old := time.Now().Add(-48 * time.Hour)
	filepath.WalkDir(home, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			os.Chtimes(p, old, old)
		}
		return nil
	})
	p = &poller{k: kind{src: Cline, sdk: true, ext: clineExt}, e: testEnv(home, nil)}
	sink = sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.Now().Add(-time.Hour), time.Now(), source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.Responses()) != 0 {
		t.Error("old file produced responses")
	}
}

func TestRoots(t *testing.T) {
	k := kind{src: Cline, sdk: true, ext: clineExt}
	mk := func(goos string, vars map[string]string) env {
		return env{home: filepath.Join("/h"), goos: goos, getenv: func(s string) string { return vars[s] }}
	}
	has := func(ss []storeA, want string) bool {
		for _, s := range ss {
			if s.root == want {
				return true
			}
		}
		return false
	}
	gsub := filepath.Join("User", "globalStorage", clineExt)
	if st := k.storesA(mk("darwin", nil)); !has(st, filepath.Join("/h", "Library", "Application Support", "Cursor", gsub)) || !has(st, filepath.Join("/h", ".cline", "data")) {
		t.Errorf("darwin %v", st)
	}
	if st := k.storesA(mk("linux", nil)); !has(st, filepath.Join("/h", ".config", "Code - Insiders", gsub)) {
		t.Errorf("linux %v", st)
	}
	if st := k.storesA(mk("windows", map[string]string{"APPDATA": `C:\Users\x\AppData\Roaming`})); !has(st, filepath.Join(`C:\Users\x\AppData\Roaming`, "Windsurf", gsub)) {
		t.Errorf("windows %v", st)
	}
	if d := mk("linux", map[string]string{"CLINE_DATA_DIR": "/d"}).dataDir(); d != "/d" {
		t.Errorf("CLINE_DATA_DIR: %s", d)
	}
	if d := mk("linux", map[string]string{"CLINE_DIR": "/c"}).dataDir(); d != filepath.Join("/c", "data") {
		t.Errorf("CLINE_DIR: %s", d)
	}
	if d := mk("linux", map[string]string{"CLINE_DATA_DIR": "/d", "CLINE_DIR": "/c"}).dataDir(); d != "/d" {
		t.Errorf("precedence: %s", d)
	}
	if d := mk("linux", nil).sessionsDir(); d != filepath.Join("/h", ".cline", "data", "sessions") {
		t.Errorf("sessions: %s", d)
	}
	if d := mk("linux", map[string]string{"CLINE_SESSION_DATA_DIR": "/s"}).sessionsDir(); d != "/s" {
		t.Errorf("CLINE_SESSION_DATA_DIR: %s", d)
	}
	roo := kind{src: Roo, ext: rooExt}
	if st := roo.storesA(mk("linux", nil)); has(st, filepath.Join("/h", ".cline", "data")) || len(st) != len(editors) {
		t.Errorf("roo stores %v", st)
	}
}
