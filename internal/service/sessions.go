package service

import (
	"os"
	"sort"

	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// SessionDetail is one session's replies, request by request.
type SessionDetail struct {
	Session  engine.Session   `json:"session"`
	Requests []engine.Request `json:"requests"`
	// From says where the requests came from: "log" (the session's log
	// read again, its whole history) or "memory" (what code-speed read
	// since it started, for agents kept in databases)
	From string `json:"from"`
}

// collector keeps the responses a parser reports.
type collector struct {
	order []string
	resp  map[string]usage.Response
}

func (c *collector) Response(r usage.Response) {
	if _, ok := c.resp[r.Key]; !ok {
		c.order = append(c.order, r.Key)
	}
	c.resp[r.Key] = r
}
func (c *collector) Quota(usage.Quota)      {}
func (c *collector) Agent(usage.AgentState) {}

// Session returns a session's detail, or false for a session the engine
// doesn't know: only sessions code-speed found itself are read, never an
// arbitrary path.
func (s *Service) Session(agent string) (SessionDetail, bool) {
	e := s.Monitor().Engine
	st, known := e.SessionState(agent)
	mem := e.SessionRequests(agent)
	if !known && len(mem) == 0 {
		return SessionDetail{}, false
	}
	d := SessionDetail{From: "memory", Requests: mem}
	src := st.Source
	if spec, ok := source.Lookup(src); ok && spec.New != nil && spec.Match != nil && spec.Match(agent) {
		if fi, err := os.Stat(agent); err == nil && fi.Mode().IsRegular() {
			c := &collector{resp: map[string]usage.Response{}}
			dedup := source.NewDedup()
			t := source.NewTail(source.Found{Source: src, Path: agent}, dedup, c)
			if t.Read(dedup, c) == nil && len(c.order) > 0 {
				d.From, d.Requests = "log", nil
				for _, k := range c.order {
					d.Requests = append(d.Requests, engine.RequestOf(c.resp[k], e.Prices()))
				}
			}
		}
	}
	sort.Slice(d.Requests, func(i, j int) bool { return d.Requests[i].EndMs < d.Requests[j].EndMs })
	d.Session = summarize(agent, st, d.Requests)
	return d, true
}

// summarize totals a session's requests.
func summarize(agent string, st usage.AgentState, rq []engine.Request) engine.Session {
	s := engine.Session{Agent: agent, Source: st.Source, Label: st.Source.Label(), Project: st.Project, Model: st.Model,
		Subagent: st.Subagent, Phase: st.Phase, LastMs: st.LastLineMs, Context: st.Context, ContextWindow: st.ContextWindow}
	if s.Phase == "" {
		s.Phase = usage.Idle
	}
	for _, q := range rq {
		s.Requests++
		s.Tokens = s.Tokens.Add(q.Tokens)
		if q.Priced {
			s.CostMicro += q.CostMicro
		} else {
			s.Unpriced++
		}
		if q.Speed > 0 {
			s.MeasuredOut += q.Output
			s.MeasuredMs += int64(float64(q.Output) / q.Speed * 1000)
		}
		first := q.EndMs
		if q.StartMs > 0 {
			first = q.StartMs
		}
		if s.FirstMs == 0 || first < s.FirstMs {
			s.FirstMs = first
		}
		s.LastMs = max(s.LastMs, q.EndMs)
		if q.Model != "" {
			s.Model = q.Model
		}
	}
	s.AvgSpeed = s.Agg.AvgSpeed()
	return s
}
