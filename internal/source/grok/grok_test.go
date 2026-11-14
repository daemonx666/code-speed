package grok

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

const sess = "testdata/sessions/%2Fprojects%2Fcomet/"

func feed(t *testing.T, d *source.Dedup, sink *sourcetest.Sink, name string) *parser {
	t.Helper()
	path := sess + name + "/updates.jsonl"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p := newParser(path, d, sink)
	sourcetest.Feed(p, string(b))
	return p
}

func TestFixtures(t *testing.T) {
	d, sink := source.NewDedup(), sourcetest.NewSink()
	main := feed(t, d, sink, "g-main")
	child := feed(t, d, sink, "g-child")
	cp := feed(t, d, sink, "g-copy")

	want := []usage.Response{
		{Key: "grok:g-p1:grok-4.5", Model: "grok-4.5", StartMs: 1792497659000, EndMs: 1792497665000,
			Tokens: usage.Tokens{Input: 1500, CachedInput: 400, Output: 300, Reasoning: 30}},
		{Key: "grok:g-p2:grok-4.5", Model: "grok-4.5", StartMs: 1792497672000, EndMs: 1792497675000,
			Tokens: usage.Tokens{Input: 1200, CachedInput: 300, Output: 250, Reasoning: 25}},
		{Key: "grok:g-p2:grok-4.6", Model: "grok-4.6", StartMs: 1792497673000, EndMs: 1792497675000,
			Tokens: usage.Tokens{Input: 800, Output: 750, Reasoning: 600}},
	}
	got := sink.Responses()
	if len(got) != len(want) {
		t.Fatalf("got %d responses, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Key != w.Key || g.Model != w.Model || g.StartMs != w.StartMs || g.EndMs != w.EndMs ||
			g.Tokens != w.Tokens || !g.Final || g.Source != Source || g.Project != "comet" || g.Provider != "" {
			t.Errorf("response %d = %+v, want %+v", i, g, w)
		}
	}
	if main.State().Project != "comet" || main.State().Subagent || main.State().Phase != usage.Idle {
		t.Errorf("main state %+v", main.State())
	}
	if !child.State().Subagent || cp.State().Subagent {
		t.Errorf("child %+v copy %+v", child.State(), cp.State())
	}
}

func TestPendingUntilSummary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "x", "s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "updates.jsonl")
	sink := sourcetest.NewSink()
	p := newParser(path, source.NewDedup(), sink)
	line := `{"timestamp":10,"method":"_x.ai/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"turn_completed","prompt_id":"p","usage":{"inputTokens":5,"outputTokens":2,"apiDurationMs":1000}},"_meta":{"agentTimestampMs":10000}}}`
	p.Line([]byte(line))
	if len(sink.Responses()) != 0 {
		t.Fatal("batch must wait for the summary")
	}
	os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"info":{"cwd":"/a/b"},"current_model_id":"grok-4.7-build"}`), 0o644)
	p.Line([]byte(`{"timestamp":11,"params":{"update":{"sessionUpdate":"agent_message_chunk"},"_meta":{"agentTimestampMs":11000}}}`))
	r := sink.Responses()
	if len(r) != 1 || r[0].Model != "grok-4.7-build" || r[0].Project != "b" || r[0].StartMs != 9000 {
		t.Fatalf("%+v", r)
	}
}

func TestMatchAndRoots(t *testing.T) {
	if !match("/h/.grok/sessions/c/s/updates.jsonl") || match("/h/.grok/sessions/c/s/chat_history.jsonl") || match("/h/x/c/s/updates.jsonl") {
		t.Error("match")
	}
	t.Setenv("GROK_HOME", "/g")
	if r := roots("/h"); len(r) != 1 || r[0] != filepath.Join("/g", "sessions") {
		t.Error(r)
	}
}

func TestQuota(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "logs"), 0o755)
	os.MkdirAll(filepath.Join(root, "sessions", "c", "s"), 0o755)
	log := `{"ts":"2026-10-20T10:00:00Z","msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":10,"currentPeriod":{"type":"MONTHLY","start":"2026-10-01T00:00:00Z","end":"2026-10-31T00:00:00Z"}}}}
{"ts":"2026-10-20T11:00:00Z","msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":42.5,"currentPeriod":{"type":"MONTHLY","start":"2026-10-01T00:00:00Z","end":"2026-10-31T00:00:00Z"}}}}
`
	os.WriteFile(filepath.Join(root, "logs", "unified.jsonl"), []byte(log), 0o644)
	sink := sourcetest.NewSink()
	newParser(filepath.Join(root, "sessions", "c", "s", "updates.jsonl"), source.NewDedup(), sink)
	if len(sink.Quotas) != 1 {
		t.Fatalf("%+v", sink.Quotas)
	}
	w := sink.Quotas[0].Windows[0]
	if w.Kind != "month" || w.UsedPercent != 42.5 || w.WindowMinutes != 30*24*60 {
		t.Errorf("%+v", w)
	}
}
