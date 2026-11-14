package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

func assistant(id string, at time.Time, out int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"id":%q,"model":"claude-opus-5-5","stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":%d}}}`+"\n",
		at.UTC().Format(time.RFC3339Nano), id, out)
}

func TestSeedThenFollowAppends(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "s.jsonl")
	now := time.Now()
	os.WriteFile(path, []byte(assistant("m1", now.Add(-time.Minute), 50)), 0o600)

	m := NewWith(engine.New(pricing.Bundled, time.Local), []source.Root{{Source: usage.ClaudeCode, Dir: root}}, nil)
	if n := m.Seed(now); n != 1 {
		t.Fatalf("seeded %d files, want 1", n)
	}
	if s := m.Engine.Snapshot(now.UnixMilli()); s.Today.Requests != 1 || s.Today.Output != 50 {
		t.Fatalf("after seed: %+v", s.Today.Agg)
	}

	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(assistant("m2", now, 70))
	f.Close()
	m.Poll(time.Now())
	if s := m.Engine.Snapshot(time.Now().UnixMilli()); s.Today.Requests != 2 || s.Today.Output != 120 {
		t.Fatalf("after append: %+v", s.Today.Agg)
	}

	// a new file is found by the next walk
	other := filepath.Join(dir, "t.jsonl")
	os.WriteFile(other, []byte(assistant("m3", time.Now(), 30)), 0o600)
	m.Rediscover(time.Now())
	if s := m.Engine.Snapshot(time.Now().UnixMilli()); s.Today.Requests != 3 || m.Files() != 2 {
		t.Fatalf("after new file: %d requests, %d files", s.Today.Requests, m.Files())
	}
}

func TestSeedReadsEverythingOnceThenOnlyWhatTheHistoryLacks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "old.jsonl")
	old := time.Now().AddDate(0, 0, -20)
	os.WriteFile(path, []byte(assistant("old", old, 10)), 0o600)
	os.Chtimes(path, old, old)
	hist := filepath.Join(t.TempDir(), "history.json")
	roots := []source.Root{{Source: usage.ClaudeCode, Dir: root}}

	// no history yet: the whole log is read, and the finished days saved
	m := NewWith(engine.New(pricing.Bundled, time.Local), roots, nil)
	m.LoadHistory(hist)
	if n := m.Seed(time.Now()); n != 1 {
		t.Fatalf("first start read %d files, want 1", n)
	}
	if done, total := m.SeedProgress(); done != 1 || total != 1 {
		t.Fatalf("progress %d/%d", done, total)
	}

	// the next start takes the old day from the history, not the log
	m2 := NewWith(engine.New(pricing.Bundled, time.Local), roots, nil)
	m2.LoadHistory(hist)
	if n := m2.Seed(time.Now()); n != 0 {
		t.Fatalf("second start read %d files, want 0", n)
	}
	if v := m2.Engine.Period(time.Now().UnixMilli(), engine.PeriodMonth); v.Requests != 1 || v.Output != 10 {
		t.Fatalf("month from history: %d requests, %d output", v.Requests, v.Output)
	}
}
