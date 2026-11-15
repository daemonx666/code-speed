// Package qwen reads Qwen Code (QwenLM/qwen-code) chat recordings
// (<runtime dir>/projects/<slug>/chats/<sessionId>.jsonl, v0.27+).
//
// Not verified against real logs:
//   - whether a request_lifecycle record is written before or after the
//     api_response of the same request (both orders are handled);
//   - whether thoughts_token_count is part of candidates/output outside the
//     OpenAI-compatible path (it is treated as a part of the output, never
//     added to it);
//   - whether a retried request writes several "started" lifecycle records
//     (only an "ended"+"success" one is used);
//   - whether /branch copies ui_telemetry records (every record with
//     forkedFrom is skipped anyway, and Dedup guards the rest).
//
// The old tmp/<hash>/chats layout is not read.
package qwen

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Source is Qwen Code's source id.
const Source usage.Source = "qwen"

func init() {
	source.Register(source.Spec{
		Source: Source,
		Label:  "Qwen Code",
		Color:  "#84cc16",
		Roots:  roots,
		Match:  match,
		New:    func(path string, d *source.Dedup, sink source.Sink) source.Parser { return newParser(path, d, sink) },
	})
}

// roots is $QWEN_RUNTIME_DIR, else $QWEN_HOME, else <home>/.qwen, plus /projects.
func roots(home string) []string {
	base := os.Getenv("QWEN_RUNTIME_DIR")
	if base == "" {
		base = os.Getenv("QWEN_HOME")
	}
	if base == "" {
		if home == "" {
			return nil
		}
		base = filepath.Join(home, ".qwen")
	}
	return []string{filepath.Join(base, "projects")}
}

// match picks projects/<slug>/chats/<file>.jsonl.
func match(path string) bool {
	p := strings.Split(filepath.ToSlash(path), "/")
	n := len(p)
	return n >= 4 && strings.HasSuffix(p[n-1], ".jsonl") && p[n-2] == "chats" && p[n-4] == "projects"
}
