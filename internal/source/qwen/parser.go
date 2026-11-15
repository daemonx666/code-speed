package qwen

import (
	"math"
	"path"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

const (
	openMax = 256
	lifeMax = 256
	// a lifecycle matched by promptId only must end this close to the response
	lifeSlackMs = 60_000
)

// timing tiers, a lower number wins
const (
	tierLife  = 1
	tierEvent = 2
	tierPrev  = 3
	tierNone  = 9
)

type entry struct {
	r      usage.Response
	prompt string
	exec   string
	tier   int
	telem  bool // merged with an api_response
	asst   bool // merged with an assistant record
	ttft   int64
	prevMs int64
}

type life struct {
	exec, prompt string
	start, end   int64
}

type parser struct {
	path  string
	dedup *source.Dedup
	sink  source.Sink
	state usage.AgentState

	prevMs int64 // timestamp of the last user / tool_result record

	open []*entry
	// ended successful lifecycles not yet used by a response
	lives []life
}

func newParser(path string, d *source.Dedup, sink source.Sink) *parser {
	return &parser{path: path, dedup: d, sink: sink,
		state: usage.AgentState{Agent: path, Source: Source, Phase: usage.Idle}}
}

func (p *parser) State() usage.AgentState { return p.state }

func (p *parser) Line(b []byte) {
	if len(b) > source.MaxLine || !source.IsObject(b) || !gjson.ValidBytes(b) {
		return
	}
	r := gjson.ParseBytes(b)
	if r.Get("forkedFrom").Exists() && r.Get("forkedFrom").Type != gjson.Null {
		return // a /branch copy
	}
	ms, _ := source.ParseTime(r.Get("timestamp").String())
	if ms > 0 {
		p.state.LastLineMs = max(p.state.LastLineMs, ms)
	}
	if cwd := r.Get("cwd").String(); cwd != "" {
		p.state.Project = project(cwd)
	}
	switch r.Get("type").String() {
	case "user", "tool_result":
		if ms > 0 {
			p.prevMs = ms
			p.state.Phase, p.state.InFlightMs = usage.Generating, ms
		}
	case "assistant":
		p.assistant(r, ms)
	case "system":
		if r.Get("subtype").String() == "ui_telemetry" {
			p.telemetry(r.Get("systemPayload.uiEvent"), ms)
		}
	}
}

// project is the last element of cwd, with either kind of separator.
func project(cwd string) string {
	cwd = strings.TrimRight(strings.ReplaceAll(cwd, `\`, "/"), "/")
	if cwd == "" {
		return ""
	}
	return path.Base(cwd)
}

func (p *parser) assistant(r gjson.Result, ms int64) {
	if ms <= 0 {
		return
	}
	hasCall := false
	for _, part := range r.Get("message.parts").Array() {
		if part.Get("functionCall").Exists() {
			hasCall = true
		}
	}
	p.state.Phase, p.state.InFlightMs = usage.Idle, 0
	if hasCall {
		p.state.Phase = usage.Tools
	}
	if m := r.Get("model").String(); m != "" {
		p.state.Model = strings.Clone(m)
	}
	if w := tokVal(r.Get("contextWindowSize")); w > 0 {
		p.state.ContextWindow = w
	}
	u := r.Get("usageMetadata")
	if !u.IsObject() {
		return
	}
	out := tokVal(u.Get("candidatesTokenCount"))
	if out <= 0 {
		return
	}
	prompt := r.Get("promptId").String()
	in := tokVal(u.Get("promptTokenCount"))
	// the api_response of this response came first
	for _, e := range p.open {
		if e.telem && !e.asst && e.prompt == prompt && prompt != "" && e.r.Output == out {
			e.asst = true
			if e.r.Model == "" {
				e.r.Model = strings.Clone(r.Get("model").String())
			}
			p.finish(e, in)
			return
		}
	}
	id := r.Get("uuid").String()
	if id == "" {
		return
	}
	key := "qwen:" + id
	if mine, _ := p.dedup.Claim(key, p.path); !mine {
		return
	}
	e := &entry{prompt: prompt, asst: true, tier: tierNone, prevMs: p.prevMs}
	e.r = usage.Response{Key: key, Source: Source, Agent: p.path, Project: p.state.Project,
		Model: strings.Clone(r.Get("model").String()), EndMs: ms, Final: true,
		Tokens: tokens(in, tokVal(u.Get("cachedContentTokenCount")), out, tokVal(u.Get("thoughtsTokenCount")))}
	if e.prevMs > 0 && e.prevMs <= ms {
		e.r.StartMs, e.tier = e.prevMs, tierPrev
	}
	p.applyPendingLife(e)
	p.add(e, in)
}

func (p *parser) telemetry(ev gjson.Result, ms int64) {
	if !ev.IsObject() {
		return
	}
	switch ev.Get("event\\.name").String() {
	case "api_response":
		p.apiResponse(ev, ms)
	case "request_lifecycle":
		p.lifecycle(ev)
	}
}

func okStatus(s gjson.Result) bool {
	switch s.Type {
	case gjson.Null:
		return !s.Exists()
	case gjson.Number:
		return s.Int() >= 200 && s.Int() < 300
	case gjson.String:
		v := s.String()
		return len(v) == 3 && v[0] == '2'
	}
	return false
}

func (p *parser) apiResponse(ev gjson.Result, ms int64) {
	if !okStatus(ev.Get("status_code")) {
		return
	}
	out := tokVal(ev.Get("output_token_count"))
	if out <= 0 {
		return
	}
	if t, ok := source.ParseTime(ev.Get("event\\.timestamp").String()); ok {
		ms = t
	}
	if ms <= 0 {
		return
	}
	if ev.Get("subagent_name").String() != "" {
		p.state.Subagent = true
	}
	prompt := ev.Get("prompt_id").String()
	exec := ev.Get("execution_id").String()
	respID := ev.Get("response_id").String()
	in := tokVal(ev.Get("input_token_count"))
	cached := tokVal(ev.Get("cached_content_token_count"))
	rs := tokVal(ev.Get("thoughts_token_count"))
	dur := tokVal(ev.Get("duration_ms"))
	ttft := tokVal(ev.Get("ttft_ms"))
	model := strings.Clone(ev.Get("model").String())

	apply := func(e *entry) {
		e.telem = true
		e.exec = exec
		e.ttft = ttft
		e.r.Tokens = tokens(in, cached, out, rs)
		if model != "" {
			e.r.Model = model
		}
		if e.tier > tierLife { // lifecycle timing is better
			if dur > 0 && ms-dur > 0 {
				e.r.StartMs, e.r.EndMs, e.tier = ms-dur, ms, tierEvent
			} else if e.tier == tierNone {
				e.r.EndMs = ms
			}
		}
		p.applyPendingLife(e)
		p.finish(e, in)
	}
	// the assistant record of this response came first
	for _, e := range p.open {
		if e.asst && !e.telem && prompt != "" && e.prompt == prompt && e.r.Output == out {
			if respID != "" {
				if mine, _ := p.dedup.Claim("qwen:"+respID, p.path); !mine {
					return
				}
			}
			apply(e)
			return
		}
	}
	id := respID
	if id == "" {
		return // nothing to count it by; the assistant record carries it
	}
	key := "qwen:" + id
	if mine, _ := p.dedup.Claim(key, p.path); !mine {
		return
	}
	e := &entry{prompt: prompt, tier: tierNone}
	e.r = usage.Response{Key: key, Source: Source, Agent: p.path, Project: p.state.Project, EndMs: ms, Final: true}
	apply(e)
	p.track(e)
}

func (p *parser) lifecycle(ev gjson.Result) {
	if ev.Get("phase").String() != "ended" || ev.Get("outcome").String() != "success" {
		return
	}
	l := life{exec: ev.Get("executionId").String(), prompt: ev.Get("promptId").String(),
		start: epochMs(ev.Get("startedAt")), end: epochMs(ev.Get("endedAt"))}
	if l.start <= 0 || l.end < l.start || (l.exec == "" && l.prompt == "") {
		return
	}
	if ev.Get("subagentId").String() != "" {
		p.state.Subagent = true
	}
	// a response already written, oldest first
	for _, e := range p.open {
		if e.tier > tierLife && e.telem && p.lifeFits(l, e) {
			p.setLife(e, l)
			p.finish(e, 0)
			return
		}
	}
	p.lives = append(p.lives, l)
	if len(p.lives) > lifeMax {
		p.lives = p.lives[1:]
	}
}

func (p *parser) lifeFits(l life, e *entry) bool {
	if l.exec != "" && e.exec == l.exec {
		return true
	}
	if l.prompt == "" || e.prompt != l.prompt {
		return false
	}
	if l.exec != "" && e.exec != "" {
		return false // another request
	}
	d := e.r.EndMs - l.end
	return d >= -lifeSlackMs && d <= lifeSlackMs
}

func (p *parser) setLife(e *entry, l life) {
	e.r.StartMs, e.r.EndMs, e.tier = l.start, l.end, tierLife
}

func (p *parser) applyPendingLife(e *entry) {
	if !e.telem || e.tier <= tierLife {
		return
	}
	for i, l := range p.lives {
		if p.lifeFits(l, e) {
			p.setLife(e, l)
			p.lives = append(p.lives[:i], p.lives[i+1:]...)
			return
		}
	}
}

func (p *parser) add(e *entry, in int64) {
	p.track(e)
	p.finish(e, in)
}

func (p *parser) track(e *entry) {
	p.open = append(p.open, e)
	if len(p.open) > openMax {
		p.open = p.open[1:]
	}
}

// finish sends e; the first token time follows the start.
func (p *parser) finish(e *entry, in int64) {
	e.r.FirstMs = 0
	if e.ttft > 0 && e.r.StartMs > 0 && e.r.StartMs+e.ttft <= e.r.EndMs {
		e.r.FirstMs = e.r.StartMs + e.ttft
	}
	if e.r.Project == "" {
		e.r.Project = p.state.Project
	}
	p.sink.Response(e.r)
	if e.r.Model != "" {
		p.state.Model = e.r.Model
	}
	p.state.Context = e.r.Input
}

// tokens normalises the counts: input includes cached, output includes reasoning.
func tokens(in, cached, out, reasoning int64) usage.Tokens {
	return usage.Tokens{Input: in, CachedInput: min(cached, in), Output: out, Reasoning: min(reasoning, out)}
}

// tokVal reads a number truncated toward zero and clamped to 0…TokCap.
func tokVal(r gjson.Result) int64 {
	if r.Type != gjson.Number {
		return 0
	}
	f := r.Float()
	switch {
	case math.IsNaN(f) || f <= 0:
		return 0
	case f >= source.TokCap:
		return source.TokCap
	}
	return int64(f)
}

// epochMs reads a Unix-millisecond time; 0 for anything else.
func epochMs(r gjson.Result) int64 {
	if r.Type != gjson.Number {
		return 0
	}
	f := r.Float()
	if math.IsNaN(f) || f <= 0 || f > 1e14 {
		return 0
	}
	return int64(f)
}
