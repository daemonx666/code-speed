// Package pi reads Pi (Pi Coding Agent) and Oh My Pi (omp) session files.
// Pi is omp's upstream and writes the same JSONL family; one parser serves
// both. METRICS §4.7 (omp) and §4.13 (Pi).
package pi

import (
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

const (
	Pi  usage.Source = "pi"
	Omp usage.Source = "omp"
)

func init() {
	source.Register(source.Spec{
		Source: Pi, Label: "Pi", Color: "#8b5cf6",
		Roots: piRoots,
		Match: matcher(Pi, 1),
		New: func(path string, d *source.Dedup, sink source.Sink) source.Parser {
			return newParser(false, path, d, sink)
		},
	})
	source.Register(source.Spec{
		Source: Omp, Label: "Oh My Pi", Color: "#c084fc",
		Roots: ompRoots,
		Match: matcher(Omp, 2),
		New: func(path string, d *source.Dedup, sink source.Sink) source.Parser {
			return newParser(true, path, d, sink)
		},
	})
}

type parser struct {
	omp   bool
	path  string
	dedup *source.Dedup
	sink  source.Sink

	haveHeader bool
	headerMs   int64
	hasHeaderT bool
	pending    map[string]bool
	st         usage.AgentState
}

func newParser(omp bool, path string, d *source.Dedup, sink source.Sink) *parser {
	p := &parser{omp: omp, path: path, dedup: d, sink: sink, pending: map[string]bool{}}
	p.st = usage.AgentState{Agent: path, Source: Pi, Phase: usage.Idle}
	if omp {
		p.st.Source = Omp
		p.st.Subagent = ompSubagent(path)
	} else {
		p.st.Subagent = piSubagent(path)
	}
	return p
}

func (p *parser) State() usage.AgentState { return p.st }

// num reads a finite JSON number.
func num(v gjson.Result) (float64, bool) {
	if v.Type != gjson.Number || math.IsInf(v.Num, 0) || math.IsNaN(v.Num) {
		return 0, false
	}
	return v.Num, true
}

func trunc64(f float64) int64 {
	f = math.Trunc(f)
	if f >= math.MaxInt64 {
		return math.MaxInt64
	}
	if f <= math.MinInt64 {
		return math.MinInt64
	}
	return int64(f)
}

func tok(v gjson.Result) int64 {
	if v.Type != gjson.Number {
		return 0
	}
	var t source.Tok
	_ = t.UnmarshalJSON([]byte(v.Raw))
	return int64(t)
}

func basename(cwd string) string {
	cwd = strings.TrimRight(cwd, `/\`)
	if i := strings.LastIndexAny(cwd, `/\`); i >= 0 {
		cwd = cwd[i+1:]
	}
	return cwd
}

type entry struct {
	typ, id, ts           string
	cwd, customType, kind string
	// side-call fields of the entry itself
	provider, model   string
	hasProv, hasModel bool
	hasAPI            bool
	usage             gjson.Result
	message           gjson.Result
}

func readEntry(b []byte) (e entry) {
	gjson.ParseBytes(b).ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "type":
			e.typ = v.Str
		case "id":
			if v.Type == gjson.String {
				e.id = strings.Clone(v.Str)
			}
		case "timestamp":
			e.ts = v.Str
		case "cwd":
			e.cwd = strings.Clone(v.Str)
		case "customType":
			e.customType = v.Str
		case "kind":
			e.kind = v.Str
		case "provider":
			if v.Type == gjson.String {
				e.provider, e.hasProv = strings.Clone(v.Str), true
			}
		case "model":
			if v.Type == gjson.String {
				e.model, e.hasModel = strings.Clone(v.Str), true
			}
		case "api":
			e.hasAPI = v.Type == gjson.String
		case "usage":
			e.usage = v
		case "message":
			e.message = v
		}
		return true
	})
	return e
}

func (p *parser) Line(b []byte) {
	if len(b) > source.MaxLine || !source.IsObject(b) {
		return
	}
	e := readEntry(b)
	if e.typ == "title" {
		return
	}
	if e.typ == "session" {
		if p.haveHeader {
			return
		}
		p.haveHeader = true
		if ms, ok := source.ParseTime(e.ts); ok {
			p.headerMs, p.hasHeaderT = ms, true
			p.touch(ms)
		}
		p.st.Project = basename(e.cwd)
		return
	}
	if !p.haveHeader || e.id == "" {
		return
	}
	ts, ok := source.ParseTime(e.ts)
	if !ok {
		return
	}
	if p.hasHeaderT && ts < p.headerMs-2000 {
		return // copied history
	}
	p.touch(ts)

	switch e.typ {
	case "message":
		p.message(e, ts)
	case "model_usage":
		if p.omp {
			p.sideCall(e, ts, true, true)
		}
	case "usage":
		if !p.omp {
			p.sideCall(e, ts, false, true)
		}
	case "compaction", "branch_summary":
		if !p.omp {
			p.sideCall(e, ts, false, false)
		}
	case "custom":
		if p.omp && e.customType == "session_exit" {
			p.idle()
		}
	}
}

func (p *parser) touch(ms int64) {
	if ms > p.st.LastLineMs {
		p.st.LastLineMs = ms
	}
}

func (p *parser) idle() {
	p.st.Phase = usage.Idle
	p.st.InFlightMs = 0
	clear(p.pending)
}

func (p *parser) generating(ms int64) {
	p.st.Phase = usage.Generating
	p.st.InFlightMs = ms
}

func (p *parser) key(id string, stamp int64) string {
	pre := "pi:"
	if p.omp {
		pre = "omp:"
	}
	return pre + id + ":" + strconv.FormatInt(stamp, 10)
}

// claim reports whether the key belongs to this file.
func (p *parser) claim(key string) bool {
	if p.dedup == nil {
		return true
	}
	mine, _ := p.dedup.Claim(key, p.path)
	return mine
}

func (p *parser) tokens(u gjson.Result) (t usage.Tokens, any bool) {
	in, out := tok(u.Get("input")), tok(u.Get("output"))
	cr, cw := tok(u.Get("cacheRead")), tok(u.Get("cacheWrite"))
	any = in > 0 || out > 0 || cr > 0 || cw > 0
	t.Input = source.Sat(source.Sat(in, cr), cw)
	t.CachedInput, t.CacheWrite, t.Output = cr, cw, out
	if p.omp {
		t.Reasoning = tok(u.Get("reasoningTokens"))
	} else {
		t.Reasoning = tok(u.Get("reasoning"))
		t.CacheWrite1h = min(tok(u.Get("cacheWrite1h")), cw)
	}
	return
}

// sideCall handles omp model_usage and Pi usage/compaction/branch_summary
// entries: a response without timing.
func (p *parser) sideCall(e entry, ts int64, guardAPI, hasModel bool) {
	u := e.usage
	if !u.IsObject() {
		return
	}
	if hasModel && (!e.hasModel || !e.hasProv || (guardAPI && !e.hasAPI)) {
		return
	}
	key := p.key(e.id, ts)
	if !p.claim(key) {
		return
	}
	t, any := p.tokens(u)
	if !any {
		return
	}
	r := usage.Response{Key: key, Source: p.st.Source, Agent: p.path, Project: p.st.Project,
		EndMs: ts, Tokens: t, Final: true}
	if hasModel {
		r.Model, r.Provider = e.model, e.provider
	}
	p.sink.Response(r)
}

func (p *parser) message(e entry, ts int64) {
	m := e.message
	if !m.IsObject() {
		return
	}
	role := m.Get("role").String()
	switch role {
	case "user", "developer":
		clear(p.pending)
		p.generating(ts)
	case "assistant":
		p.assistant(e, m, ts)
	case "toolResult":
		delete(p.pending, m.Get("toolCallId").String())
		if p.omp && m.Get("toolName").String() == "yield" && m.Get("isError").Type != gjson.True {
			p.idle()
		} else if p.st.Phase == usage.Tools && len(p.pending) == 0 {
			p.generating(ts)
		}
		if !p.omp {
			if u := m.Get("usage"); u.IsObject() {
				e.usage = u
				p.sideCall(e, ts, false, false)
			}
		}
	}
}

func (p *parser) assistant(e entry, m gjson.Result, ts int64) {
	model, prov, api, u := m.Get("model"), m.Get("provider"), m.Get("api"), m.Get("usage")
	if model.Type != gjson.String || prov.Type != gjson.String || api.Type != gjson.String || !u.IsObject() {
		return
	}
	tsF, tsOK := num(m.Get("timestamp"))
	tsOK = tsOK && tsF > 0
	stamp := ts
	var startMs int64
	if tsOK {
		stamp = trunc64(tsF)
		startMs = stamp
	}
	key := p.key(e.id, stamp)
	if !p.claim(key) {
		return
	}
	stop := ""
	if s := m.Get("stopReason"); s.Type == gjson.String {
		stop = s.Str
	} else {
		stop = "aborted"
	}
	failed := stop == "error" || stop == "aborted" || (!p.omp && (stop == "pending" || stop == "deferred"))

	endMs := ts
	if p.omp {
		if d, ok := num(m.Get("duration")); ok && d >= 0 && tsOK {
			endMs = trunc64(float64(startMs) + d)
		} else if c, ok := num(m.Get("completedAt")); ok && c > 0 {
			endMs = trunc64(c)
		}
	} else if d, ok := num(m.Get("durationMs")); ok && d >= 0 && tsOK {
		endMs = trunc64(float64(startMs) + d)
	}

	t, any := p.tokens(u)
	if any {
		r := usage.Response{Key: key, Source: p.st.Source, Agent: p.path, Project: p.st.Project,
			Model: strings.Clone(model.Str), Provider: strings.Clone(prov.Str),
			StartMs: startMs, EndMs: endMs, Tokens: t, Final: true}
		if failed {
			r.StartMs = 0
		}
		p.st.Model = r.Model
		p.st.Context = t.Input
		p.sink.Response(r)
	}

	// phase
	if failed {
		p.idle()
		return
	}
	var ids []string
	if stop == "toolUse" || stop == "stop" || stop == "length" {
		if c := m.Get("content"); c.IsArray() {
			c.ForEach(func(_, blk gjson.Result) bool {
				if blk.Get("type").String() == "toolCall" {
					if id := blk.Get("id"); id.Type == gjson.String {
						ids = append(ids, id.Str)
					}
				}
				return true
			})
		}
	}
	switch {
	case len(ids) > 0:
		clear(p.pending)
		for _, id := range ids {
			p.pending[strings.Clone(id)] = true
		}
		p.st.Phase, p.st.InFlightMs = usage.Tools, 0
	case p.omp && stop == "stop" && m.Get("stopDetails.type").String() == "pause_turn":
		p.generating(endMs)
	default:
		p.idle()
	}
}
