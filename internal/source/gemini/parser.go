package gemini

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

type parser struct {
	path  string
	dedup *source.Dedup
	sink  source.Sink

	pendingStart  int64 // 0: none
	pendingMarker string
	seenCalls     map[string]bool
	resp          map[string]usage.Response
	projectDone   bool
	state         usage.AgentState
}

func newParser(path string, d *source.Dedup, sink source.Sink) *parser {
	return &parser{path: path, dedup: d, sink: sink,
		seenCalls: map[string]bool{}, resp: map[string]usage.Response{},
		state: usage.AgentState{Agent: path, Source: Gemini, Phase: usage.Idle,
			Subagent: isSubagentPath(path)}}
}

func isSubagentPath(path string) bool {
	parts := splitPath(path)
	i := chatsIndex(parts)
	return i >= 0 && i == len(parts)-3
}

func (p *parser) State() usage.AgentState { return p.state }

func (p *parser) idle() {
	p.state.Phase = usage.Idle
	p.state.InFlightMs = 0
	p.pendingStart = 0
	p.pendingMarker = ""
}

func (p *parser) generating(start int64, marker string) {
	p.pendingStart = start
	p.pendingMarker = marker
	p.state.Phase = usage.Generating
	p.state.InFlightMs = start
}

func (p *parser) seen(ms int64) {
	if ms > p.state.LastLineMs {
		p.state.LastLineMs = ms
	}
}

func (p *parser) seenStr(r gjson.Result) (int64, bool) {
	if r.Type != gjson.String {
		return 0, false
	}
	ms, ok := source.ParseTime(r.String())
	if ok {
		p.seen(ms)
	}
	return ms, ok
}

func (p *parser) Line(b []byte) {
	if len(b) > source.MaxLine || !source.IsObject(b) || !gjson.ValidBytes(b) {
		return
	}
	if !p.projectDone {
		p.projectDone = true
		p.state.Project = projectOf(p.path)
	}
	p.record(gjson.ParseBytes(b))
}

// record classifies one object (METRICS 4.6, Record classification).
func (p *parser) record(r gjson.Result) {
	switch {
	case r.Get("$rewindTo").Type == gjson.String:
		p.idle()
	case r.Get("$patch").IsObject():
		if p.state.Phase == usage.Generating && p.pendingMarker != "" {
			for _, id := range r.Get("$patch.removeIds").Array() {
				if id.Type == gjson.String && id.String() == p.pendingMarker {
					p.idle()
					break
				}
			}
		}
	case r.Get("id").Type == gjson.String:
		p.message(r)
	case r.Get("$set").IsObject():
		p.meta(r.Get("$set"), true)
	case r.Get("sessionId").Type == gjson.String && r.Get("projectHash").Type == gjson.String:
		p.meta(r, false)
		// a legacy object written on one line
		if m := r.Get("messages"); m.IsArray() {
			p.idle()
			for _, msg := range m.Array() {
				if msg.Get("id").Type == gjson.String {
					p.message(msg)
				}
			}
		}
	}
}

func (p *parser) meta(o gjson.Result, set bool) {
	p.seenStr(o.Get("startTime"))
	p.seenStr(o.Get("lastUpdated"))
	if o.Get("kind").Type == gjson.String && o.Get("kind").String() == "subagent" {
		p.state.Subagent = true
	}
	if !set {
		return
	}
	if s := o.Get("summary"); s.Type == gjson.String && s.String() != "" {
		p.idle()
	}
	if m := o.Get("messages"); m.IsArray() {
		p.checkpoint(m)
	}
}

func (p *parser) checkpoint(msgs gjson.Result) {
	found := false
	for _, m := range msgs.Array() {
		id := m.Get("id")
		if id.Type != gjson.String {
			continue
		}
		if p.pendingMarker != "" && id.String() == p.pendingMarker {
			found = true
		}
		if m.Get("type").String() != "gemini" || !m.Get("tokens").IsObject() {
			continue
		}
		ts, ok := source.ParseTime(m.Get("timestamp").String())
		if !ok {
			continue
		}
		key := strings.Clone(id.String())
		if mine, _ := p.dedup.Claim(key, p.path); !mine {
			continue
		}
		p.upsert(key, m, ts, 0)
	}
	if p.state.Phase == usage.Generating && !found {
		p.idle()
	}
}

// upsert emits the response of a gemini record: new keys with startMs,
// known keys updated in place.
func (p *parser) upsert(key string, m gjson.Result, ts, start int64) {
	model := strings.Clone(m.Get("model").String())
	t := m.Get("tokens")
	tok := func(name string) int64 { return tokOf(t.Get(name)) }
	toks := usage.Tokens{
		Input:       source.Sat(tok("input"), tok("tool")),
		CachedInput: tok("cached"),
		Output:      source.Sat(tok("output"), tok("thoughts")),
		Reasoning:   tok("thoughts"),
	}
	r, known := p.resp[key]
	if known {
		r.Tokens = toks
		if model != "" {
			r.Model = model
		}
		r.EndMs = max(r.EndMs, ts)
	} else {
		r = usage.Response{Key: key, Source: Gemini, Agent: p.path, Project: p.state.Project,
			Model: model, StartMs: start, EndMs: ts, Tokens: toks, Final: true}
	}
	p.resp[key] = r
	p.sink.Response(r)
	if r.Model != "" {
		p.state.Model = r.Model
	}
	p.state.Context = r.Input
}

func tokOf(r gjson.Result) int64 {
	if r.Type != gjson.Number {
		return 0
	}
	var t source.Tok
	_ = t.UnmarshalJSON([]byte(r.Raw))
	return int64(t)
}

func (p *parser) message(m gjson.Result) {
	// timestamps of every record count for lastLineMs, deduped or not
	ts, ok := p.seenStr(m.Get("timestamp"))
	m.Get("toolCalls").ForEach(func(_, c gjson.Result) bool { p.seenStr(c.Get("timestamp")); return true })
	m.Get("thoughts").ForEach(func(_, c gjson.Result) bool { p.seenStr(c.Get("timestamp")); return true })
	if !ok {
		return
	}
	id := strings.Clone(m.Get("id").String())
	switch m.Get("type").String() {
	case "user":
		p.generating(ts, id)
	case "error":
		p.idle()
	case "info":
		if c := m.Get("content"); c.Type == gjson.String && c.String() == "Request cancelled." {
			p.idle()
		}
	case "gemini":
		p.gemini(m, id, ts)
	}
}

func (p *parser) gemini(m gjson.Result, id string, ts int64) {
	if mine, _ := p.dedup.Claim(id, p.path); !mine {
		return
	}
	// tool calls with parseable timestamps
	type call struct {
		id, name, status string
		ts               int64
		hasTS            bool
	}
	var calls []call
	earliest := int64(0)
	haveEarliest := false
	m.Get("toolCalls").ForEach(func(_, c gjson.Result) bool {
		var k call
		k.id = c.Get("id").String()
		k.name = c.Get("name").String()
		k.status = c.Get("status").String()
		if c.Get("id").Type != gjson.String {
			k.id = ""
		}
		if t := c.Get("timestamp"); t.Type == gjson.String {
			if ms, ok := source.ParseTime(t.String()); ok {
				k.ts, k.hasTS = ms, true
				if !haveEarliest || ms < earliest {
					earliest, haveEarliest = ms, true
				}
			}
		}
		calls = append(calls, k)
		return true
	})

	if m.Get("tokens").IsObject() {
		if _, known := p.resp[id]; known {
			p.upsert(id, m, ts, 0)
		} else {
			start := p.pendingStart
			if haveEarliest && ts >= earliest {
				start = 0
			}
			p.upsert(id, m, ts, start)
			p.pendingStart = 0
			p.pendingMarker = ""
			if !hasVisibleText(m.Get("content")) || hasFunctionCall(m.Get("content")) {
				p.state.Phase = usage.Tools
			} else {
				p.state.Phase = usage.Idle
			}
			p.state.InFlightMs = 0
		}
	}

	var fresh []call
	for _, c := range calls {
		if c.id == "" || p.seenCalls[c.id] {
			continue
		}
		p.seenCalls[strings.Clone(c.id)] = true
		fresh = append(fresh, c)
	}
	if len(fresh) == 0 {
		return
	}
	done, allCancelled := false, true
	latest := int64(0)
	for _, c := range fresh {
		if c.name == "complete_task" && c.status == "success" {
			done = true
		}
		if c.status != "cancelled" {
			allCancelled = false
		}
		if c.hasTS && c.ts > latest {
			latest = c.ts
		}
	}
	switch {
	case done, allCancelled:
		p.idle()
	default:
		if latest == 0 {
			latest = ts
		}
		p.generating(latest, id)
	}
}

func hasVisibleText(c gjson.Result) bool {
	switch {
	case c.Type == gjson.String:
		return strings.TrimSpace(c.String()) != ""
	case c.IsArray():
		for _, part := range c.Array() {
			if part.Get("text").Type == gjson.String && !part.Get("thought").Bool() {
				return true
			}
		}
	}
	return false
}

func hasFunctionCall(c gjson.Result) bool {
	if !c.IsArray() {
		return false
	}
	for _, part := range c.Array() {
		if part.Get("functionCall").Exists() {
			return true
		}
	}
	return false
}
