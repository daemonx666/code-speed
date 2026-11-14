// Package source reads the agents' own logs. Each log file gets a parser,
// a state machine fed complete lines in order, which reports responses
// (upserts by key), the agent's phase and quota snapshots.
//
// The rules follow Agent Speed's specification (spec/METRICS.md §3–4 and
// spec/QUOTAS.md §3, MIT, github.com/zoefix/agent-speed).
package source

import (
	"bytes"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Sink receives what parsers find.
type Sink interface {
	// Response adds a response, or replaces the one with the same key.
	Response(r usage.Response)
	// Quota reports a limits snapshot.
	Quota(q usage.Quota)
	// Agent reports a session's latest state (polled sources; the monitor
	// reports tailed files' states itself).
	Agent(s usage.AgentState)
}

// Parser reads one log file's lines.
type Parser interface {
	Line(b []byte)
	State() usage.AgentState
}

// Tok is a token count: a JSON number truncated toward zero and clamped
// to 0…10^12. Anything else counts 0.
type Tok int64

const TokCap = 1_000_000_000_000

func (t *Tok) UnmarshalJSON(b []byte) error {
	*t = 0
	var f float64
	if len(b) == 0 || (b[0] != '-' && (b[0] < '0' || b[0] > '9')) {
		return nil
	}
	if err := json.Unmarshal(b, &f); err != nil {
		// an integer too long for float64 still counts as the cap
		if b[0] != '-' {
			*t = TokCap
		}
		return nil
	}
	switch {
	case math.IsNaN(f) || f <= 0:
		*t = 0
	case f >= TokCap:
		*t = TokCap
	default:
		*t = Tok(math.Trunc(f))
	}
	return nil
}

func Sat(a, b int64) int64 { return min(a+b, TokCap) }

// parseTime reads an RFC 3339 timestamp as Unix ms; ok is false for
// anything else, including a time without a zone.
func ParseTime(s string) (int64, bool) {
	if s == "" || len(s) > 64 {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

func Project(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(cwd))
}

// MaxLine is the longest line read; longer ones are skipped.
const MaxLine = 32 << 20

// isObject is a quick check that a line is a whole JSON object: it opens
// and closes with braces. A line cut short by a crash fails it. A full
// validation would cost a pass over every line, and gjson reads a
// malformed object as missing fields, never as a panic.
func IsObject(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) > 1 && b[0] == '{' && b[len(b)-1] == '}'
}

func HasPathDir(path, dir string) bool {
	for _, p := range strings.Split(filepath.ToSlash(path), "/") {
		if p == dir {
			return true
		}
	}
	return false
}
