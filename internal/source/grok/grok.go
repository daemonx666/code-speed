// Package grok parses Grok Build's session journal
// (~/.grok/sessions/<encoded-cwd>/<sessionId>/updates.jsonl).
// METRICS §4.12; its billing quota is QUOTAS §9.2 (log route only).
package grok

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Source is Grok Build's source id.
const Source usage.Source = "grok"

func init() {
	source.Register(source.Spec{
		Source: Source,
		Label:  "Grok Build",
		Color:  "#64748b",
		Roots:  roots,
		Match:  match,
		New:    func(path string, d *source.Dedup, sink source.Sink) source.Parser { return newParser(path, d, sink) },
	})
}

func roots(home string) []string {
	if v := os.Getenv("GROK_HOME"); v != "" {
		return []string{filepath.Join(v, "sessions")}
	}
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".grok", "sessions")}
}

// match picks sessions/<encoded-cwd>/<sessionId>/updates.jsonl only.
func match(path string) bool {
	p := strings.Split(filepath.ToSlash(path), "/")
	n := len(p)
	return n >= 4 && p[n-1] == "updates.jsonl" && p[n-4] == "sessions"
}

// maxPending is how many numeric batches wait for the native summary.
const maxPending = 256

type batch struct {
	endMs    int64
	promptID string
	eventID  string
	usage    string // raw JSON of the update's usage object
}

type parser struct {
	path  string
	dedup *source.Dedup
	sink  source.Sink

	state usage.AgentState

	metaLoaded bool
	curModel   string
	ignore     bool // a subagent: its tokens live in the parent's batch
	pending    []batch

	quotaMs int64 // observedMs of the last quota reported
}

func newParser(path string, d *source.Dedup, sink source.Sink) *parser {
	p := &parser{path: path, dedup: d, sink: sink,
		state: usage.AgentState{Agent: path, Source: Source, Phase: usage.Idle}}
	p.loadMeta()
	p.reportQuota()
	return p
}

func (p *parser) State() usage.AgentState { return p.state }

// loadMeta reads the session's summary.json when it exists.
func (p *parser) loadMeta() {
	if p.metaLoaded {
		return
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(p.path), "summary.json"))
	if err != nil || !gjson.ValidBytes(b) {
		return
	}
	r := gjson.ParseBytes(b)
	p.metaLoaded = true
	p.curModel = strings.Clone(r.Get("current_model_id").String())
	if cwd := r.Get("info.cwd").String(); cwd != "" {
		p.state.Project = source.Project(cwd)
	}
	kind := r.Get("session_kind").String()
	parent := r.Get("parent_session_id").String()
	if (kind == "subagent" || kind == "subagent_fork") && parent != "" {
		p.ignore = true
		p.state.Subagent = true
	}
	if p.state.Model == "" {
		p.state.Model = p.curModel
	}
}

func (p *parser) Line(b []byte) {
	if !source.IsObject(b) {
		return
	}
	r := gjson.ParseBytes(b)
	up := r.Get("params.update")
	if !up.IsObject() {
		return
	}
	ts := r.Get("params._meta.agentTimestampMs")
	var ms int64
	if ts.Type == gjson.Number && ts.Int() > 0 {
		ms = ts.Int()
	} else if s := r.Get("timestamp"); s.Type == gjson.Number && s.Int() > 0 {
		ms = s.Int() * 1000
	} else {
		return
	}
	p.state.LastLineMs = max(p.state.LastLineMs, ms)
	kind := up.Get("sessionUpdate").String()
	switch kind {
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk", "response_started":
		if p.state.Phase != usage.Generating {
			p.setPhase(usage.Generating, ms)
		}
	case "tool_call", "tool_call_update":
		st := up.Get("status").String()
		if st == "completed" || st == "failed" {
			p.setPhase(usage.Generating, ms)
		} else {
			p.setPhase(usage.Tools, 0)
		}
	case "turn_completed":
		p.turnCompleted(up, r, ms)
		p.setPhase(usage.Idle, 0)
		return
	case "turn_cancelled", "turn_failed":
		p.setPhase(usage.Idle, 0)
		return
	}
	if len(p.pending) > 0 {
		p.flush()
	}
}

func (p *parser) setPhase(ph usage.Phase, inFlight int64) {
	p.state.Phase = ph
	p.state.InFlightMs = 0
	if ph == usage.Generating {
		p.state.InFlightMs = inFlight
	}
}

func (p *parser) turnCompleted(up, line gjson.Result, ms int64) {
	u := up.Get("usage")
	if !u.IsObject() {
		return
	}
	bt := batch{
		endMs:    ms,
		promptID: strings.Clone(up.Get("prompt_id").String()),
		eventID:  strings.Clone(line.Get("params._meta.eventId").String()),
		usage:    strings.Clone(u.Raw),
	}
	p.loadMeta()
	if !p.metaLoaded {
		if len(p.pending) >= maxPending {
			p.pending = p.pending[1:]
		}
		p.pending = append(p.pending, bt)
		return
	}
	p.flush()
	p.emit(bt)
	p.reportQuota()
}

func (p *parser) flush() {
	p.loadMeta()
	if !p.metaLoaded {
		return
	}
	pend := p.pending
	p.pending = nil
	for _, bt := range pend {
		p.emit(bt)
	}
}

type bucket struct {
	model string
	u     gjson.Result
}

func (p *parser) emit(bt batch) {
	if p.ignore {
		return
	}
	u := gjson.Parse(bt.usage)
	var buckets []bucket
	if mu := u.Get("modelUsage"); mu.IsObject() && len(mu.Map()) > 0 {
		mu.ForEach(func(k, v gjson.Result) bool {
			buckets = append(buckets, bucket{strings.Clone(k.String()), v})
			return true
		})
	} else {
		flat := u
		if !flat.Get("inputTokens").Exists() && !flat.Get("outputTokens").Exists() {
			if t := u.Get("totals"); t.IsObject() {
				flat = t
			}
		}
		buckets = append(buckets, bucket{p.curModel, flat})
	}
	id := bt.promptID
	if id == "" {
		id = bt.eventID
	}
	for _, bk := range buckets {
		tok := func(path string) int64 { return tokVal(bk.u.Get(path)) }
		in, out, reas := tok("inputTokens"), tok("outputTokens"), tok("reasoningTokens")
		if reas > out {
			out = source.Sat(out, reas)
		}
		var start int64
		if d := tok("apiDurationMs"); d > 0 && d <= usage.MaxInterval {
			start = bt.endMs - d
		}
		key := "grok:" + id + ":" + bk.model
		if mine, _ := p.dedup.Claim(key, p.path); !mine {
			continue
		}
		p.sink.Response(usage.Response{
			Key: key, Source: Source, Agent: p.path,
			Project: p.state.Project, Model: bk.model,
			StartMs: start, EndMs: bt.endMs,
			Tokens: usage.Tokens{
				Input:       in,
				CachedInput: tok("cachedReadTokens"),
				CacheWrite:  tok("cacheCreationTokens"),
				Output:      out,
				Reasoning:   min(reas, out),
			},
			Final: true,
		})
		p.state.Model = bk.model
		p.state.Context = in
	}
}

// tokVal reads a token value: truncated toward zero, clamped to 0…10^12.
func tokVal(r gjson.Result) int64 {
	if r.Type != gjson.Number {
		return 0
	}
	f := r.Float()
	switch {
	case f != f || f <= 0:
		return 0
	case f >= source.TokCap:
		return source.TokCap
	}
	return int64(f)
}
