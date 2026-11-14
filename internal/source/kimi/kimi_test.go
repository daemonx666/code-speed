package kimi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

const base = "testdata/sessions/wd_nova_0123456789ab/"

func feed(t *testing.T, d *source.Dedup, sink *sourcetest.Sink, rel string) *parser {
	t.Helper()
	path := base + rel + "/wire.jsonl"
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
	main := feed(t, d, sink, "sess-kimi/agents/main")
	child := feed(t, d, sink, "sess-kimi/agents/agent-0")
	cp := feed(t, d, sink, "sess-kimi-copy/agents/main")
	old := feed(t, d, sink, "sess-kimi-old/agents/main")

	type w struct {
		key, model, provider string
		start, end           int64
		tk                   usage.Tokens
	}
	want := []w{
		{"kimi:k-m1", "kimi-k2.5", "moonshot", 1792497630000, 1792497634000, usage.Tokens{Input: 130, CachedInput: 20, CacheWrite: 10, Output: 80}},
		{"kimi:k-m2", "kimi-for-coding-highspeed", "moonshot", 1792497640000, 1792497643200, usage.Tokens{Input: 300, CachedInput: 80, CacheWrite: 20, Output: 120}},
		{"kimi:k-m3", "kimi-k2.5", "moonshot", 1792497650000, 1792497652000, usage.Tokens{Input: 60, CachedInput: 10, Output: 60}},
		{"kimi:k-child", "kimi-k2.5", "moonshot", 1792497632000, 1792497635000, usage.Tokens{Input: 70, Output: 45}},
		{"kimi:k-old", "", "", 1792497660000, 1792497662000, usage.Tokens{Input: 40, Output: 30}},
	}
	got := sink.Responses()
	if len(got) != len(want) {
		t.Fatalf("got %d responses: %+v", len(got), got)
	}
	for i, e := range want {
		g := got[i]
		if g.Key != e.key || g.Model != e.model || g.Provider != e.provider || g.StartMs != e.start ||
			g.EndMs != e.end || g.Tokens != e.tk || !g.Final || g.Project != "nova" || g.Source != Source {
			t.Errorf("response %d = %+v, want %+v", i, g, e)
		}
	}
	if main.State().Subagent || !child.State().Subagent || cp.State().Subagent || old.State().Subagent {
		t.Error("subagent flags")
	}
	if main.State().Phase != usage.Idle || main.State().Project != "nova" {
		t.Errorf("%+v", main.State())
	}
}

func TestMatchAndRoots(t *testing.T) {
	if !match("/h/.kimi-code/sessions/wd/s/agents/main/wire.jsonl") ||
		match("/h/.kimi-code/sessions/wd/s/wire.jsonl") ||
		match("/h/.kimi-code/sessions/wd/s/agents/main/other.jsonl") {
		t.Error("match")
	}
	t.Setenv("KIMI_CODE_HOME", "/k")
	if r := roots("/h"); len(r) != 1 || r[0] != filepath.Join("/k", "sessions") {
		t.Error(r)
	}
}
