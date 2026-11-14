package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/claudebridge"
	"github.com/daemonx666/code-speed/internal/source/codextrace"

	"github.com/daemonx666/code-speed/internal/usage"
)

// The two agents built into this package; every other one is a subpackage.
func init() {
	Register(Spec{
		Source: usage.ClaudeCode,
		Label:  "Claude Code",
		Color:  "#d97757",
		Roots: func(home string) []string {
			if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
				var dirs []string
				for _, d := range strings.Split(v, ",") {
					if d = strings.TrimSpace(d); d != "" {
						dirs = append(dirs, filepath.Join(d, "projects"))
					}
				}
				return dirs
			}
			if home == "" {
				return nil
			}
			return []string{filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".config", "claude", "projects")}
		},
		New: func(path string, d *Dedup, sink Sink) Parser { return newClaude(path, d, sink) },
		// the subscription limits the status-line bridge keeps (claudebridge)
		Poller: func() Poller { return &claudeQuota{} },
		Every:  5 * time.Second,
	})
	Register(Spec{
		Source: usage.Codex,
		Label:  "Codex",
		Color:  "#4f7cff",
		Roots: func(home string) []string {
			dir := os.Getenv("CODEX_HOME")
			if dir == "" {
				if home == "" {
					return nil
				}
				dir = filepath.Join(home, ".codex")
			}
			return []string{filepath.Join(dir, "sessions"), filepath.Join(dir, "archived_sessions")}
		},
		Match: func(p string) bool {
			return strings.HasSuffix(p, ".jsonl") || strings.HasSuffix(p, ".jsonl.zst")
		},
		New: func(path string, d *Dedup, sink Sink) Parser {
			c := newCodex(path, d, sink)
			// requests sent at the priority tier show in Codex's trace database
			if dir := codexHome(); dir != "" {
				c.trace = codextrace.ForHome(dir)
			}
			return c
		},
	})
}

func codexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	if h := homeDir(); h != "" {
		return filepath.Join(h, ".codex")
	}
	return ""
}

// claudeQuota reports the limits the status-line bridge keeps, when the
// file changes.
type claudeQuota struct{ seen time.Time }

func (c *claudeQuota) Poll(_ context.Context, _, _ time.Time, _ *Dedup, sink Sink) error {
	q, mod, ok := claudebridge.Read()
	if ok && !mod.Equal(c.seen) {
		c.seen = mod
		sink.Quota(q)
	}
	return nil
}
