package engine

import (
	"sort"

	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Session is one agent session's totals: a log file, or a session in an
// agent's database.
type Session struct {
	Agent    string       `json:"agent"` // the session's id (a log file's path, or db#session)
	Source   usage.Source `json:"source"`
	Label    string       `json:"label"`
	Project  string       `json:"project,omitempty"`
	Model    string       `json:"model,omitempty"`
	Subagent bool         `json:"subagent,omitempty"`
	Phase    usage.Phase  `json:"phase"`
	FirstMs  int64        `json:"firstMs"`
	LastMs   int64        `json:"lastMs"`
	Agg
	AvgSpeed      float64 `json:"avgSpeed"`
	Context       int64   `json:"context,omitempty"`
	ContextWindow int64   `json:"contextWindow,omitempty"`
}

// Request is one reply of a session.
type Request struct {
	Key     string `json:"key"`
	StartMs int64  `json:"startMs,omitempty"`
	EndMs   int64  `json:"endMs"`
	Model   string `json:"model,omitempty"`
	usage.Tokens
	Speed     float64 `json:"speed,omitempty"`  // output tok/s, 0 when not timed
	TTFTMs    int64   `json:"ttftMs,omitempty"` // to the first content, where the log says
	CostMicro int64   `json:"costMicro"`
	Priced    bool    `json:"priced"`
	Fast      bool    `json:"fast,omitempty"`
	Effort    string  `json:"effort,omitempty"`
	Final     bool    `json:"final"`
}

// RequestOf turns a response into a request row, priced with table.
func RequestOf(r usage.Response, table *pricing.Table) Request {
	q := Request{Key: r.Key, StartMs: r.StartMs, EndMs: r.EndMs, Model: r.Model, Tokens: r.Tokens, Fast: r.Fast, Effort: r.Effort, Final: r.Final}
	if r.Measured() {
		q.Speed, _ = r.Speed()
	}
	if r.FirstMs > 0 && r.StartMs > 0 && r.FirstMs >= r.StartMs {
		q.TTFTMs = r.FirstMs - r.StartMs
	}
	c := table.ResponseCost(r)
	q.CostMicro, q.Priced = c.Micro, c.Known
	return q
}

// Prices is the table the engine prices with.
func (e *Engine) Prices() *pricing.Table { return e.prices }

// Sessions lists the sessions with a reply that ended at or after sinceMs,
// newest first, from the replies the engine holds.
func (e *Engine) Sessions(sinceMs int64) []Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	by := map[string]*Session{}
	for _, en := range e.known {
		r := en.r
		if r.EndMs < sinceMs {
			continue
		}
		s := by[r.Agent]
		if s == nil {
			s = &Session{Agent: r.Agent, Source: r.Source, Label: r.Source.Label(), Project: r.Project,
				FirstMs: r.EndMs, LastMs: r.EndMs, Phase: usage.Idle}
			by[r.Agent] = s
		}
		s.Agg.apply(en, 1)
		if r.StartMs > 0 && r.StartMs < s.FirstMs {
			s.FirstMs = r.StartMs
		}
		s.FirstMs = min(s.FirstMs, r.EndMs)
		if r.EndMs >= s.LastMs {
			s.LastMs = r.EndMs
			if r.Model != "" {
				s.Model = r.Model
			}
			if r.Project != "" {
				s.Project = r.Project
			}
		}
	}
	out := make([]Session, 0, len(by))
	for _, s := range by {
		if a, ok := e.agents[s.Agent]; ok {
			s.Phase, s.Subagent = a.Phase, a.Subagent
			s.Context, s.ContextWindow = a.Context, a.ContextWindow
			s.LastMs = max(s.LastMs, a.LastLineMs)
			if a.Project != "" {
				s.Project = a.Project
			}
		}
		s.AvgSpeed = s.Agg.AvgSpeed()
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastMs > out[j].LastMs })
	return out
}

// SessionRequests lists a session's replies the engine holds, oldest
// first.
func (e *Engine) SessionRequests(agent string) []Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Request
	for _, en := range e.known {
		if en.r.Agent == agent {
			out = append(out, RequestOf(en.r, e.prices))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndMs < out[j].EndMs })
	return out
}

// SessionState is the latest state of a session the engine has seen.
func (e *Engine) SessionState(agent string) (usage.AgentState, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.agents[agent]
	return a, ok
}
