package zcode

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

const base = 1792497600000 // 2026-10-20T12:00:00Z

type want struct {
	key, model, provider string
	start, end           int64
	in, cached, write    int64
	out, reasoning       int64
}

func check(t *testing.T, got usage.Response, w want, agent string) {
	t.Helper()
	exp := usage.Response{Key: w.key, Source: ZCode, Agent: agent, Project: "quasar", Model: w.model, Provider: w.provider,
		StartMs: base + w.start, EndMs: base + w.end,
		Tokens: usage.Tokens{Input: w.in, CachedInput: w.cached, CacheWrite: w.write, Output: w.out, Reasoning: w.reasoning},
		Final:  true}
	if got != exp {
		t.Errorf("\n got %+v\nwant %+v", got, exp)
	}
}

func poll(t *testing.T, path string) *sourcetest.Sink {
	t.Helper()
	sink := sourcetest.NewSink()
	p := &poller{path: path}
	defer p.close()
	if err := p.Poll(context.Background(), time.UnixMilli(base-86_400_000), time.UnixMilli(base+60_000), source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	return sink
}

func TestFixture(t *testing.T) {
	path, _ := filepath.Abs("testdata/zcode/cli/db/db.sqlite")
	sink := poll(t, path)
	hi := "account:zai-highspeed-card"
	main, sub := agentID(path, "sess-z-main"), agentID(path, "sess-z-sub")
	exp := []want{
		{"zcode:z-r1", "glm-5.3", "zai", 10000, 14000, 1200, 500, 100, 200, 50},
		{"zcode:z-title", "glm-5.3", "zai", 15000, 16000, 80, 0, 0, 30, 0},
		{"zcode:z-r2", "glm-5.3", hi, 17000, 18500, 2000, 1000, 0, 150, 0},
		{"zcode:z-r4", "glm-5.3", "zai", 19000, 19500, 2100, 1000, 0, 160, 0},
		{"zcode:z-r5", "glm-5.3", hi, 20000, 20400, 2200, 1000, 0, 170, 0},
		{"zcode:z-compact", "glm-5.3", "zai", 20500, 20900, 300, 0, 0, 40, 0},
		{"zcode:z-r3", "GLM-5.1-Highspeed", "zai", 12000, 15000, 400, 100, 20, 90, 10},
	}
	if len(sink.Resp) != len(exp) {
		t.Fatalf("%d responses, want %d", len(sink.Resp), len(exp))
	}
	for _, w := range exp {
		g, ok := sink.Resp[w.key]
		if !ok {
			t.Errorf("missing %s", w.key)
			continue
		}
		agent := main
		if w.key == "zcode:z-r3" {
			agent = sub
		}
		check(t, g, w, agent)
	}
	if a := sink.Agents[sub]; !a.Subagent || a.Project != "quasar" {
		t.Errorf("sub agent %+v", a)
	}
	if a := sink.Agents[main]; a.Subagent || a.Phase != usage.Idle {
		t.Errorf("main agent %+v", a)
	}
	run := sink.Agents[agentID(path, "sess-z-running")]
	if run.Phase != usage.Generating || run.InFlightMs != base+22000 {
		t.Errorf("running agent %+v", run)
	}
}

func TestOldSchema(t *testing.T) {
	path, _ := filepath.Abs("testdata/zcode-old/cli/db/db.sqlite")
	sink := poll(t, path)
	g, ok := sink.Resp["zcode:z-old"]
	if !ok || len(sink.Resp) != 1 {
		t.Fatalf("responses %v", sink.Resp)
	}
	check(t, g, want{"zcode:z-old", "glm-5.3", "zai", 1000, 2000, 100, 0, 0, 20, 0}, agentID(path, "sess-z-old"))
}

func TestSpecAndPath(t *testing.T) {
	s, ok := source.Lookup(ZCode)
	if !ok || s.Label != "ZCode" || s.Color != "#6366f1" || s.Poller == nil || s.Every != 2*time.Second {
		t.Errorf("spec %+v", s)
	}
	t.Setenv("ZCODE_DATA_BASE_DIR", "/x")
	if got := DBPath(); got != filepath.Join("/x", ".zcode", "cli", "db", "db.sqlite") {
		t.Errorf("DBPath %s", got)
	}
}

func TestMissingAndUnsupported(t *testing.T) {
	sink := poll(t, filepath.Join(t.TempDir(), "none.sqlite"))
	if len(sink.Resp) != 0 {
		t.Error("responses from a missing database")
	}
}
