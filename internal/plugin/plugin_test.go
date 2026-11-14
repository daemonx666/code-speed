package plugin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

// install copies an example plugin into a fresh plugins folder.
func install(t *testing.T, example string) string {
	t.Helper()
	t.Setenv("CODE_SPEED_HOME", t.TempDir())
	dst := filepath.Join(Dir(), filepath.Base(example))
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("..", "..", "examples", "plugins", example)
	entries, _ := os.ReadDir(src)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(src, e.Name()))
		fi, _ := os.Stat(filepath.Join(src, e.Name()))
		os.WriteFile(filepath.Join(dst, e.Name()), b, fi.Mode())
	}
	return dst
}

func TestJSONLPlugin(t *testing.T) {
	dir := install(t, "acme-jsonl")
	loaded := LoadAll()
	if len(loaded) != 1 || loaded[0].Error != "" || loaded[0].ID != "acme" {
		t.Fatalf("loaded %+v", loaded)
	}
	spec, ok := source.Lookup("plugin.acme")
	if !ok || !spec.Plugin || spec.Label != "Acme Agent" || !source.DefaultEnabled("plugin.acme") {
		t.Fatalf("spec %+v", spec)
	}
	if !spec.Match("/x/2026-11-14.jsonl") || spec.Match("/x/notes.txt") {
		t.Fatal("match")
	}
	home, _ := os.UserHomeDir()
	if roots := spec.Roots(home); roots[0] != filepath.Join(home, ".acme", "sessions") {
		t.Fatalf("roots %v", roots)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "sample.jsonl"))
	sink := sourcetest.NewSink()
	p := spec.New("/x/s.jsonl", source.NewDedup(), sink)
	sourcetest.Feed(p, string(b))
	rs := sink.Responses()
	if len(rs) != 2 {
		t.Fatalf("responses %+v", rs)
	}
	r := rs[0]
	if r.Key != "plugin.acme:r1" || r.Model != "claude-sonnet-5-5" || r.Project != "shop" || !r.Final {
		t.Fatalf("r1 %+v", r)
	}
	if r.Input != 9312 || r.CachedInput != 9000 || r.CacheWrite != 300 || r.Output != 400 {
		t.Fatalf("r1 tokens %+v", r.Tokens)
	}
	if sp, _ := r.Speed(); sp != 100 { // 400 tokens from the user line to the reply, 4 s
		t.Fatalf("r1 speed %v", sp)
	}
	if sp, _ := rs[1].Speed(); sp != 100 { // 250 tokens in 2.5 s after the tool result
		t.Fatalf("r2 speed %v", sp)
	}
	if st := p.State(); st.Project != "shop" || st.Context != 9720 {
		t.Fatalf("state %+v", st)
	}
}

func TestCommandPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the example is a shell script")
	}
	install(t, "usage-command")
	LoadAll()
	spec, ok := source.Lookup("plugin.my-usage")
	if !ok || spec.Poller == nil || spec.Every != time.Minute || source.DefaultEnabled("plugin.my-usage") {
		t.Fatalf("spec %+v", spec)
	}
	sink := sourcetest.NewSink()
	if err := spec.Poller().Poll(context.Background(), time.Now().Add(-time.Hour), time.Now(), source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	rs := sink.Responses()
	if len(rs) != 1 || rs[0].Output != 300 || rs[0].Agent != "plugin.my-usage#s1" {
		t.Fatalf("responses %+v", rs)
	}
	if len(sink.Quotas) != 1 || sink.Quotas[0].Windows[0].UsedPercent != 42 {
		t.Fatalf("quotas %+v", sink.Quotas)
	}
	if a, ok := sink.Agents["plugin.my-usage#s1"]; !ok || a.ContextWindow != 272000 || a.Phase != usage.Idle {
		t.Fatalf("session %+v", sink.Agents)
	}
}

func TestBrokenPluginsAreListedNotLoaded(t *testing.T) {
	t.Setenv("CODE_SPEED_HOME", t.TempDir())
	for name, body := range map[string]string{
		"bad-json": "{",
		"bad-type": `{"id":"x","type":"python"}`,
		"bad-id":   `{"id":"Has Spaces","type":"command","command":["x"]}`,
		"no-roots": `{"id":"y","type":"jsonl","record":{"time":"t","tokens":{"output":"o"}}}`,
	} {
		os.MkdirAll(filepath.Join(Dir(), name), 0o755)
		os.WriteFile(filepath.Join(Dir(), name, "plugin.json"), []byte(body), 0o600)
	}
	loaded := LoadAll()
	if len(loaded) != 4 {
		t.Fatalf("loaded %d", len(loaded))
	}
	for _, l := range loaded {
		if l.Error == "" {
			t.Errorf("%s loaded without error", l.ID)
		}
	}
	if _, ok := source.Lookup("plugin.x"); ok {
		t.Fatal("a broken plugin was registered")
	}
}
