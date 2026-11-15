package qwen

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

const (
	t0   = int64(1792490400000)
	s1   = "testdata/projects/myapp/chats/s1.jsonl"
	s2   = "testdata/projects/myapp/chats/s2.jsonl"
	winF = "testdata/projects/winproj/chats/w1.jsonl"
)

func run(t *testing.T, d *source.Dedup, s *sourcetest.Sink, path string) *parser {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p := newParser(path, d, s)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		p.Line(append([]byte(nil), sc.Bytes()...))
	}
	return p
}

func TestFixtures(t *testing.T) {
	d := source.NewDedup()
	s := sourcetest.NewSink()
	p1 := run(t, d, s, s1)
	run(t, d, s, s2)
	pw := run(t, d, s, winF)

	type want struct {
		key                   string
		start, first, end     int64
		in, cached, out, rsng int64
	}
	// a1: assistant first, api_response second, lifecycle after (priority 1)
	// resp-2: lifecycle, api_response, then assistant (priority 1, stashed)
	// a3: no telemetry (priority 3: previous tool_result)
	// a4: api_response timestamp minus duration (priority 2), no ttft
	ws := []want{
		{"qwen:a1", t0 + 200, t0 + 1100, t0 + 4050, 1000, 400, 200, 50},
		{"qwen:resp-2", t0 + 6100, t0 + 6600, t0 + 8950, 1500, 1000, 300, 100},
		{"qwen:a3", t0 + 10000, 0, t0 + 12000, 1800, 1500, 150, 0},
		{"qwen:a4", t0 + 20300, 0, t0 + 23100, 2000, 0, 400, 0},
		{"qwen:b1", 0, 0, t0 + 31000, 300, 0, 40, 0}, // no previous record in its window... see below
	}
	// s2 has a user record at 30000, so b1 is priority 3 from it
	ws[4].start = t0 + 30000
	var got []usage.Response
	for _, r := range s.Responses() {
		if r.Agent != winF {
			got = append(got, r)
		}
	}
	if len(got) != len(ws) {
		for _, r := range got {
			t.Logf("%+v", r)
		}
		t.Fatalf("%d responses, want %d", len(got), len(ws))
	}
	for i, w := range ws {
		r := got[i]
		if r.Key != w.key || r.StartMs != w.start || r.FirstMs != w.first || r.EndMs != w.end ||
			r.Input != w.in || r.CachedInput != w.cached || r.Output != w.out || r.Reasoning != w.rsng ||
			r.CacheWrite != 0 || !r.Final || r.Source != Source || r.Model != "qwen3-coder-plus" || r.Project != "myapp" {
			t.Errorf("[%d] = %+v, want %+v", i, r, w)
		}
	}
	if got[0].Agent != s1 || got[4].Agent != s2 {
		t.Errorf("agents %q %q", got[0].Agent, got[4].Agent)
	}
	// reasoning is part of the output, never added to it
	if got[0].Total() != 1200 {
		t.Errorf("total = %d", got[0].Total())
	}

	if st := p1.State(); st.Project != "myapp" || st.ContextWindow != 262144 || st.Context != 2000 || st.Source != Source {
		t.Errorf("state = %+v", st)
	}

	// Windows cwd
	var w []usage.Response
	for _, r := range s.Responses() {
		if r.Agent == winF {
			w = append(w, r)
		}
	}
	if len(w) != 1 || w[0].Project != "win-app" || w[0].StartMs != t0 || w[0].EndMs != t0+2000 || w[0].Output != 80 || w[0].Reasoning != 20 {
		t.Errorf("windows = %+v", w)
	}
	if pw.State().Project != "win-app" {
		t.Errorf("windows state project = %q", pw.State().Project)
	}
}

func TestForkedFromSkipped(t *testing.T) {
	d := source.NewDedup()
	s := sourcetest.NewSink()
	run(t, d, s, s1)
	if _, ok := s.Resp["qwen:a-fork"]; ok {
		t.Error("forked record counted")
	}
}

func TestDedupAcrossFiles(t *testing.T) {
	d := source.NewDedup()
	s := sourcetest.NewSink()
	run(t, d, s, s1)
	n := len(s.Responses())
	run(t, d, s, s2) // repeats a1 and resp-2, adds b1
	if got := len(s.Responses()); got != n+1 {
		t.Errorf("%d responses after s2, want %d", got, n+1)
	}
	if r := s.Resp["qwen:a1"]; r.Agent != s1 {
		t.Errorf("a1 owned by %q", r.Agent)
	}
}

func TestAssistantOnlyAndTelemetryOnly(t *testing.T) {
	s := sourcetest.NewSink()
	p := newParser("x.jsonl", source.NewDedup(), s)
	sourcetest.Feed(p, `{"uuid":"u1","timestamp":"2026-10-20T10:00:00.000Z","type":"user","cwd":"/a/b"}
{"uuid":"a1","timestamp":"2026-10-20T10:00:02.000Z","type":"assistant","promptId":"p","model":"m","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"cachedContentTokenCount":4,"thoughtsTokenCount":2}}
{"uuid":"a2","timestamp":"2026-10-20T10:00:03.000Z","type":"assistant","promptId":"p","model":"m","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0}}
{"uuid":"s1","timestamp":"2026-10-20T10:00:09.000Z","type":"system","subtype":"ui_telemetry","systemPayload":{"uiEvent":{"event.name":"api_response","event.timestamp":"2026-10-20T10:00:09.000Z","response_id":"r9","status_code":200,"duration_ms":4000,"input_token_count":7,"output_token_count":3,"prompt_id":"q","model":"m","subagent_name":"general"}}}
`)
	rs := s.Responses()
	if len(rs) != 2 {
		t.Fatalf("%+v", rs)
	}
	a := rs[0]
	if a.Key != "qwen:a1" || a.Input != 10 || a.CachedInput != 4 || a.Output != 5 || a.Reasoning != 2 || a.StartMs != t0 || a.EndMs != t0+2000 {
		t.Errorf("assistant-only = %+v", a)
	}
	b := rs[1]
	if b.Key != "qwen:r9" || b.StartMs != t0+5000 || b.EndMs != t0+9000 || b.Output != 3 || b.Project != "b" {
		t.Errorf("telemetry-only = %+v", b)
	}
	if !p.State().Subagent {
		t.Error("subagent not flagged")
	}
}

func TestRoots(t *testing.T) {
	t.Setenv("QWEN_RUNTIME_DIR", "")
	t.Setenv("QWEN_HOME", "")
	home := filepath.Join("h", "me")
	if got := roots(home); len(got) != 1 || got[0] != filepath.Join(home, ".qwen", "projects") {
		t.Errorf("default = %v", got)
	}
	t.Setenv("QWEN_HOME", filepath.Join("q", "home"))
	if got := roots(home); got[0] != filepath.Join("q", "home", "projects") {
		t.Errorf("QWEN_HOME = %v", got)
	}
	t.Setenv("QWEN_RUNTIME_DIR", filepath.Join("q", "run"))
	if got := roots(home); got[0] != filepath.Join("q", "run", "projects") {
		t.Errorf("QWEN_RUNTIME_DIR = %v", got)
	}
	t.Setenv("QWEN_RUNTIME_DIR", "")
	t.Setenv("QWEN_HOME", "")
	if got := roots(""); got != nil {
		t.Errorf("no home = %v", got)
	}
}

func TestMatch(t *testing.T) {
	yes := []string{
		filepath.FromSlash("/h/.qwen/projects/myapp/chats/s1.jsonl"),
		filepath.FromSlash("projects/-Users-me-app/chats/x.jsonl"),
	}
	no := []string{
		filepath.FromSlash("/h/.qwen/tmp/abc123/chats/s1.jsonl"),
		filepath.FromSlash("/h/.qwen/projects/myapp/chats/s1.json"),
		filepath.FromSlash("/h/.qwen/projects/myapp/s1.jsonl"),
		filepath.FromSlash("/h/.qwen/projects/myapp/chats/sub/s1.jsonl"),
		filepath.FromSlash("/h/.qwen/projects/chats/s1.jsonl"),
	}
	for _, p := range yes {
		if !match(p) {
			t.Errorf("match(%q) = false", p)
		}
	}
	for _, p := range no {
		if match(p) {
			t.Errorf("match(%q) = true", p)
		}
	}
}
