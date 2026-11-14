// Package sourcetest helps test a source's parser or poller: a Sink that
// records what it is given, and a way to feed lines.
package sourcetest

import (
	"strings"
	"sync"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Sink records responses (by key, the latest upsert kept), quotas and
// agent states.
type Sink struct {
	mu     sync.Mutex
	Resp   map[string]usage.Response
	Order  []string // keys in the order first seen
	Quotas []usage.Quota
	Agents map[string]usage.AgentState
}

func NewSink() *Sink {
	return &Sink{Resp: map[string]usage.Response{}, Agents: map[string]usage.AgentState{}}
}

func (s *Sink) Response(r usage.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Resp[r.Key]; !ok {
		s.Order = append(s.Order, r.Key)
	}
	s.Resp[r.Key] = r
}

func (s *Sink) Quota(q usage.Quota) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Quotas = append(s.Quotas, q)
}

func (s *Sink) Agent(a usage.AgentState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Agents[a.Agent] = a
}

// Responses lists the responses in the order first seen.
func (s *Sink) Responses() []usage.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]usage.Response, 0, len(s.Order))
	for _, k := range s.Order {
		out = append(out, s.Resp[k])
	}
	return out
}

// Feed gives p every line of text, in order.
func Feed(p source.Parser, text string) {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			p.Line([]byte(l))
		}
	}
}

var _ source.Sink = (*Sink)(nil)
