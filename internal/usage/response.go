// Package usage holds the normalized record every source parser emits:
// one model response with its tokens and timing.
package usage

import "sync"

// Source names the agent that wrote a log.
type Source string

const (
	ClaudeCode Source = "claude-code"
	Codex      Source = "codex"
)

var (
	labelMu sync.RWMutex
	labels  = map[Source][2]string{}
)

// SetLabel records a source's shown name and colour (source.Register).
func SetLabel(s Source, label, color string) {
	labelMu.Lock()
	defer labelMu.Unlock()
	labels[s] = [2]string{label, color}
}

// Label is the name shown for a source.
func (s Source) Label() string {
	labelMu.RLock()
	defer labelMu.RUnlock()
	if l, ok := labels[s]; ok && l[0] != "" {
		return l[0]
	}
	return string(s)
}

// Color is the CSS colour that marks a source; "" when none was set.
func (s Source) Color() string {
	labelMu.RLock()
	defer labelMu.RUnlock()
	return labels[s][1]
}

// Tokens are a response's normalized token counts. Input is all input,
// cached reads and cache writes included; Output includes reasoning.
type Tokens struct {
	Input        int64 `json:"input"`
	CachedInput  int64 `json:"cachedInput"`
	CacheWrite   int64 `json:"cacheWrite"`
	CacheWrite1h int64 `json:"cacheWrite1h"`
	Output       int64 `json:"output"`
	Reasoning    int64 `json:"reasoning"`
}

// Add returns t + o.
func (t Tokens) Add(o Tokens) Tokens {
	return Tokens{
		Input:        t.Input + o.Input,
		CachedInput:  t.CachedInput + o.CachedInput,
		CacheWrite:   t.CacheWrite + o.CacheWrite,
		CacheWrite1h: t.CacheWrite1h + o.CacheWrite1h,
		Output:       t.Output + o.Output,
		Reasoning:    t.Reasoning + o.Reasoning,
	}
}

// Sub returns t - o.
func (t Tokens) Sub(o Tokens) Tokens {
	return Tokens{
		Input:        t.Input - o.Input,
		CachedInput:  t.CachedInput - o.CachedInput,
		CacheWrite:   t.CacheWrite - o.CacheWrite,
		CacheWrite1h: t.CacheWrite1h - o.CacheWrite1h,
		Output:       t.Output - o.Output,
		Reasoning:    t.Reasoning - o.Reasoning,
	}
}

// Total is input plus output.
func (t Tokens) Total() int64 { return t.Input + t.Output }

// Max returns the field-wise maximum, how repeated lines of one Claude
// message merge.
func (t Tokens) Max(o Tokens) Tokens {
	return Tokens{
		Input:        max(t.Input, o.Input),
		CachedInput:  max(t.CachedInput, o.CachedInput),
		CacheWrite:   max(t.CacheWrite, o.CacheWrite),
		CacheWrite1h: max(t.CacheWrite1h, o.CacheWrite1h),
		Output:       max(t.Output, o.Output),
		Reasoning:    max(t.Reasoning, o.Reasoning),
	}
}

const (
	// MaxInterval is the longest request-to-reply interval that still
	// counts as speed.
	MaxInterval = 15 * 60_000
	// MinDuration widens shorter intervals so a reply written in a few
	// milliseconds doesn't read as millions of tok/s.
	MinDuration = 500
)

// Response is one model reply.
type Response struct {
	Key      string `json:"key"`
	Source   Source `json:"source"`
	Agent    string `json:"agent"` // the log file it came from
	Project  string `json:"project,omitempty"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`

	// StartMs is when the request was sent (0 when unknown), EndMs when
	// the usage was written, FirstMs when the first content was written
	// (0 when unknown). Unix milliseconds.
	StartMs int64 `json:"startMs,omitempty"`
	EndMs   int64 `json:"endMs"`
	FirstMs int64 `json:"firstMs,omitempty"`

	Tokens
	Fast bool `json:"fast,omitempty"`
	// Effort is the reasoning effort the request asked for ("low",
	// "medium", "high", …), "" when the log doesn't say
	Effort string `json:"effort,omitempty"`
	Final  bool   `json:"final"` // the usage is the final one
}

// Interval is the response's timed span [start', end] with the minimum
// duration applied, and whether it counts as speed at all.
func (r Response) Interval() (start, end int64, ok bool) {
	if r.StartMs <= 0 || r.EndMs < r.StartMs || r.EndMs-r.StartMs > MaxInterval {
		return 0, 0, false
	}
	return min(r.StartMs, r.EndMs-MinDuration), r.EndMs, true
}

// Measured is a final response with a valid interval: the only kind whose
// speed is trusted.
func (r Response) Measured() bool {
	_, _, ok := r.Interval()
	return ok && r.Final
}

// Speed is output tokens per second over the interval, time to first
// token included.
func (r Response) Speed() (float64, bool) {
	s, e, ok := r.Interval()
	if !ok {
		return 0, false
	}
	return float64(r.Output) / (float64(e-s) / 1000), true
}

// Overlap is the length of [a0, a1] ∩ [b0, b1] in milliseconds.
func Overlap(a0, a1, b0, b1 int64) int64 {
	return max(0, min(a1, b1)-max(a0, b0))
}
