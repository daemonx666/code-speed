// Package kimi parses Kimi Code CLI (TypeScript v2) wire journals
// (~/.kimi-code/sessions/<workDirKey>/<sessionId>/agents/<agentId>/wire.jsonl).
// METRICS §4.11. The old Python kimi-cli layout is not supported.
package kimi

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Source is Kimi Code's source id.
const Source usage.Source = "kimi"

func init() {
	source.Register(source.Spec{
		Source: Source,
		Label:  "Kimi Code",
		Color:  "#0ea5e9",
		Roots:  roots,
		Match:  match,
		New:    func(path string, d *source.Dedup, sink source.Sink) source.Parser { return newParser(path, d, sink) },
	})
}

func roots(home string) []string {
	if v := os.Getenv("KIMI_CODE_HOME"); v != "" {
		return []string{filepath.Join(v, "sessions")}
	}
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".kimi-code", "sessions")}
}

// match picks sessions/<workDirKey>/<sessionId>/agents/<agentId>/wire.jsonl only.
func match(path string) bool {
	p := strings.Split(filepath.ToSlash(path), "/")
	n := len(p)
	return n >= 6 && p[n-1] == "wire.jsonl" && p[n-3] == "agents" && p[n-6] == "sessions"
}

const openMax = 512

type parser struct {
	path  string
	dedup *source.Dedup
	sink  source.Sink
	state usage.AgentState

	// the latest llm.request not yet matched to a response
	pendingStart int64
	reqModel     string
	reqProvider  string

	open  map[string]usage.Response // this file's responses, so mirrors merge
	order []string

	stateTried bool
}

func newParser(path string, d *source.Dedup, sink source.Sink) *parser {
	id := filepath.Base(filepath.Dir(path))
	return &parser{path: path, dedup: d, sink: sink, open: map[string]usage.Response{},
		state: usage.AgentState{Agent: path, Source: Source, Phase: usage.Idle, Subagent: id != "main"}}
}

func (p *parser) State() usage.AgentState { return p.state }

// readStateJSON takes the working directory from the session's state.json
// when the wire journal named none.
func (p *parser) readStateJSON() {
	if p.stateTried || p.state.Project != "" {
		return
	}
	p.stateTried = true
	// agents/<id>/wire.jsonl → the session folder is two levels up
	b, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(p.path))), "state.json"))
	if err != nil {
		return
	}
	for _, k := range []string{"cwd", "workDir", "workDirectory", "workingDirectory", "environmentDisclosure.cwd"} {
		if v := gjson.GetBytes(b, k).String(); v != "" {
			p.state.Project = source.Project(v)
			return
		}
	}
}

func (p *parser) Line(b []byte) {
	if !source.IsObject(b) {
		return
	}
	r := gjson.ParseBytes(b)
	typ := r.Get("type").String()
	t := r.Get("time")
	if t.Type != gjson.Number || t.Int() <= 0 {
		return
	}
	ms := t.Int()
	p.state.LastLineMs = max(p.state.LastLineMs, ms)
	switch typ {
	case "config.update":
		if cwd := r.Get("environmentDisclosure.cwd").String(); cwd != "" {
			p.state.Project = source.Project(cwd)
		}
	case "turn.prompt":
		p.setPhase(usage.Generating, ms)
	case "llm.request":
		p.pendingStart = ms
		if m := r.Get("model").String(); m != "" {
			p.reqModel = strings.Clone(m)
			p.state.Model = p.reqModel
		}
		if pr := r.Get("provider").String(); pr != "" {
			p.reqProvider = strings.Clone(pr)
		}
		p.setPhase(usage.Generating, ms)
	case "turn.ended", "turn.cancelled", "turn.failed", "turn.aborted":
		p.setPhase(usage.Idle, 0)
	case "context.append_message", "agent.message.appended", "human.agent.message.appended":
		p.message(r.Get("message"), ms)
	case "context.append_loop_event":
		ev := r.Get("event")
		switch ev.Get("type").String() {
		case "step.begin":
			p.setPhase(usage.Generating, ms)
		case "step.end":
			p.stepEnd(ev, ms)
		}
	}
}

func (p *parser) setPhase(ph usage.Phase, inFlight int64) {
	p.state.Phase = ph
	p.state.InFlightMs = 0
	if ph == usage.Generating {
		p.state.InFlightMs = inFlight
	}
}

func (p *parser) stepEnd(ev gjson.Result, ms int64) {
	id := ev.Get("messageId").String()
	if id == "" {
		id = ev.Get("uuid").String()
	}
	u := ev.Get("usage")
	lat := tokVal(ev.Get("llmFirstTokenLatencyMs")) + tokVal(ev.Get("llmStreamDurationMs"))
	p.record(id, u, "", "", lat, ms)
	if ev.Get("finishReason").String() == "tool_calls" {
		p.setPhase(usage.Tools, 0)
	} else {
		p.setPhase(usage.Idle, 0)
	}
}

// message handles an appended message: flat ({role, id, usage}) or
// wrapped ({message: {role}, meta: {messageId, usage, model}}).
func (p *parser) message(m gjson.Result, ms int64) {
	if !m.IsObject() {
		return
	}
	meta := m.Get("meta")
	inner := m
	if w := m.Get("message"); w.IsObject() {
		inner = w
	}
	if inner.Get("role").String() != "assistant" {
		return
	}
	if inner.Get("partial").Bool() || inner.Get("streaming").Bool() || m.Get("partial").Bool() {
		return
	}
	id := inner.Get("id").String()
	if id == "" {
		id = meta.Get("messageId").String()
	}
	u := inner.Get("usage")
	if !u.IsObject() {
		u = meta.Get("usage")
	}
	model := meta.Get("model.model").String()
	if model == "" {
		model = inner.Get("model").String()
	}
	provider := meta.Get("model.provider").String()
	t := inner.Get("llmTiming")
	lat := tokVal(t.Get("llmFirstTokenLatencyMs")) + tokVal(t.Get("llmStreamDurationMs"))
	p.record(id, u, model, provider, lat, ms)
}

func (p *parser) record(id string, u gjson.Result, model, provider string, latMs, ms int64) {
	if id == "" || !u.IsObject() {
		return
	}
	key := "kimi:" + id
	if mine, _ := p.dedup.Claim(key, p.path); !mine {
		return
	}
	p.readStateJSON()
	prev, seen := p.open[key]

	other, cr, cw := tokVal(u.Get("inputOther")), tokVal(u.Get("inputCacheRead")), tokVal(u.Get("inputCacheCreation"))
	tk := usage.Tokens{
		Input:       source.Sat(source.Sat(other, cr), cw),
		CachedInput: cr,
		CacheWrite:  cw,
		Output:      tokVal(u.Get("output")),
	}
	r := usage.Response{Key: key, Source: Source, Agent: p.path, Project: p.state.Project,
		Model: model, Provider: provider, EndMs: ms, Tokens: tk, Final: true}
	if seen { // a mirror of the same response updates it
		if r.Model == "" {
			r.Model = prev.Model
		}
		if r.Provider == "" {
			r.Provider = prev.Provider
		}
		r.StartMs, r.EndMs = prev.StartMs, max(prev.EndMs, ms)
	} else {
		if r.Model == "" {
			r.Model = p.reqModel
		}
		if r.Provider == "" {
			r.Provider = p.reqProvider
		}
		switch {
		case p.pendingStart > 0:
			r.StartMs = p.pendingStart
		case latMs > 0:
			r.StartMs = ms - latMs
		}
		p.pendingStart = 0
		p.order = append(p.order, key)
		if len(p.order) > openMax {
			delete(p.open, p.order[0])
			p.order = p.order[1:]
		}
	}
	p.open[key] = r
	p.sink.Response(r)
	if r.Model != "" {
		p.state.Model = r.Model
	}
	p.state.Context = r.Input
}

// tokVal reads a number truncated toward zero and clamped to 0…10^12.
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
