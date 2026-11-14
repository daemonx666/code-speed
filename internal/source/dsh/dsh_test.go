package dsh

import (
	"os"

	"context"
	"github.com/klauspost/compress/zstd"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

func TestFixtures(t *testing.T) {
	p := &poller{roots: []string{"testdata/sessions"}}
	s := sourcetest.NewSink()
	d := source.NewDedup()
	if err := p.Poll(context.Background(), time.Unix(0, 0), time.Now(), d, s); err != nil {
		t.Fatal(err)
	}
	const t0 = 1792490640000
	want := map[string]usage.Response{
		"dsm-1": {StartMs: 1792490641000, EndMs: 1792490645000, Tokens: usage.Tokens{Input: 9000, Output: 400}, Model: "deepseek-flash", Provider: "deepseek-account"},
		"dsm-2": {StartMs: 1792490646010, EndMs: 1792490648010, Tokens: usage.Tokens{Input: 9400, CachedInput: 9216, CacheWrite: 64, Output: 300}, Model: "deepseek-flash", Provider: "deepseek-account"},
		"dss-1": {StartMs: 1792490651000, EndMs: 1792490654000, Tokens: usage.Tokens{Input: 4500, CachedInput: 4000, Output: 240}, Model: "deepseek-pro", Provider: "deepseek-account"},
	}
	_ = t0
	if len(s.Resp) != len(want) {
		t.Fatalf("got %d responses: %v", len(s.Resp), s.Order)
	}
	for k, w := range want {
		g, ok := s.Resp[k]
		if !ok {
			t.Fatalf("missing %s", k)
		}
		if g.Key != k || g.Source != DSH || g.Model != w.Model || g.Provider != w.Provider ||
			g.StartMs != w.StartMs || g.EndMs != w.EndMs || g.Tokens != w.Tokens || !g.Final || g.Project != "quasar" {
			t.Errorf("%s: got %+v want %+v", k, g, w)
		}
	}
	if len(s.Agents) != 2 {
		t.Fatalf("agents: %d", len(s.Agents))
	}
	for path, a := range s.Agents {
		switch {
		case a.Subagent:
			if a.Phase != usage.Generating || a.InFlightMs != 1792490655010 || a.LastLineMs != 1792490655010 || a.Model != "deepseek-pro" || a.Context != 4500 {
				t.Errorf("sub %s: %+v", path, a)
			}
		default:
			if a.Phase != usage.Idle || a.LastLineMs != 1792490648030 || a.Model != "deepseek-flash" || a.Context != 9400 {
				t.Errorf("main %s: %+v", path, a)
			}
		}
	}
	// a second poll changes nothing
	s2 := sourcetest.NewSink()
	_ = p.Poll(context.Background(), time.Unix(0, 0), time.Now(), d, s2)
	if len(s2.Resp) != 0 {
		t.Errorf("repoll emitted %d", len(s2.Resp))
	}
}

func TestFrameSplit(t *testing.T) {
	// a frame cut short is not consumed
	for _, f := range [][]byte{{0x28, 0xB5}, {0x28, 0xB5, 0x2F, 0xFD, 0x20}} {
		if _, st := frameSize(f); st != frameIncomplete {
			t.Errorf("%x: %d", f, st)
		}
	}
	if _, st := frameSize([]byte("hello")); st != frameInvalid {
		t.Error("garbage should be invalid")
	}
}

func TestIncremental(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/s1/session.v4.jsonl.zstd"
	if err := os.MkdirAll(dir+"/s1", 0o755); err != nil {
		t.Fatal(err)
	}
	enc, _ := zstd.NewWriter(nil)
	frame := func(s string) []byte { return enc.EncodeAll([]byte(s), nil) }
	var all []byte
	all = append(all, frame(`{"type":"session","id":"x","createdAt":1000,"cwd":"/a/proj"}`+"\n")...)
	all = append(all, frame(`{"type":"step/start","seq":1,"time":2000}`+"\n")...)
	last := frame(`{"type":"assistant/message","seq":2,"time":5000,"data":{"message":{"content":[]},"usage":{"inputTokens":1,"outputTokens":2}}}` + "\n")
	cut := len(all) + len(last)/2
	all = append(all, last...)
	if err := os.WriteFile(path, all[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	p := &poller{roots: []string{dir}}
	d := source.NewDedup()
	s := sourcetest.NewSink()
	poll := func() {
		if err := p.Poll(context.Background(), time.Unix(0, 0), time.Now(), d, s); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	if len(s.Resp) != 0 {
		t.Fatal("incomplete frame was read")
	}
	if err := os.WriteFile(path, all, 0o644); err != nil {
		t.Fatal(err)
	}
	poll()
	r, ok := s.Resp["dsh:x:2"]
	if !ok || r.StartMs != 2000 || r.EndMs != 5000 || r.Tokens.Output != 2 {
		t.Fatalf("%+v", s.Resp)
	}
}
