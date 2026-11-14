package plugin

import (
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// jsonlParser reads a declarative plugin's log file, one line at a time.
type jsonlParser struct {
	m     *Manifest
	path  string
	dedup *source.Dedup
	sink  source.Sink
	state usage.AgentState
	start int64 // the latest request start seen
	open  map[string]*usage.Response
}

func newJSONL(m *Manifest, path string, d *source.Dedup, sink source.Sink) *jsonlParser {
	return &jsonlParser{m: m, path: path, dedup: d, sink: sink, open: map[string]*usage.Response{},
		state: usage.AgentState{Agent: path, Source: m.Source(), Phase: usage.Idle}}
}

func (p *jsonlParser) State() usage.AgentState { return p.state }

func (c Cond) match(line []byte) bool {
	if c.Path == "" {
		return false
	}
	v := gjson.GetBytes(line, c.Path)
	if len(c.Equals) == 0 {
		return v.Exists() && v.Type != gjson.Null && v.String() != "" && v.Type != gjson.False
	}
	for _, e := range c.Equals {
		if v.String() == e {
			return true
		}
	}
	return false
}

// timeOf reads a time: RFC 3339, or a number of seconds or milliseconds.
func timeOf(v gjson.Result) (int64, bool) {
	switch v.Type {
	case gjson.String:
		if ms, ok := source.ParseTime(v.Str); ok {
			return ms, true
		}
		if f, err := strconv.ParseFloat(v.Str, 64); err == nil {
			return unixMs(f), true
		}
	case gjson.Number:
		return unixMs(v.Num), true
	}
	return 0, false
}

func unixMs(f float64) int64 {
	if f > 1e11 { // milliseconds since 1973
		return int64(f)
	}
	return int64(math.Round(f * 1000))
}

func tok(line []byte, path string) int64 {
	if path == "" {
		return 0
	}
	v := gjson.GetBytes(line, path)
	var f float64
	switch v.Type {
	case gjson.Number:
		f = v.Num
	case gjson.String:
		f, _ = strconv.ParseFloat(v.Str, 64)
	}
	if !(f > 0) {
		return 0
	}
	return min(int64(f), source.TokCap)
}

func (p *jsonlParser) Line(line []byte) {
	if !source.IsObject(line) {
		return
	}
	r := &p.m.Record
	if ts, ok := timeOf(gjson.GetBytes(line, r.Time)); ok {
		p.state.LastLineMs = max(p.state.LastLineMs, ts)
		if r.RequestStart.match(line) {
			p.start = ts
			p.state.Phase, p.state.InFlightMs = usage.Generating, ts
			return
		}
	}
	if r.When.Path != "" && !r.When.match(line) {
		return
	}
	t := r.Tokens
	tk := usage.Tokens{
		Input: tok(line, t.Input), CachedInput: tok(line, t.CachedInput), CacheWrite: tok(line, t.CacheWrite),
		CacheWrite1h: tok(line, t.CacheWrite1h), Output: tok(line, t.Output), Reasoning: tok(line, t.Reasoning),
	}
	if tk.Input == 0 && tk.Output == 0 {
		return
	}
	if r.InputExcludesCache {
		tk.Input = source.Sat(source.Sat(tk.Input, tk.CachedInput), tk.CacheWrite)
	}
	if r.ReasoningExcluded {
		tk.Output = source.Sat(tk.Output, tk.Reasoning)
	}
	end, ok := timeOf(gjson.GetBytes(line, r.Time))
	if !ok {
		return
	}
	key := ""
	for _, k := range r.Key {
		if v := gjson.GetBytes(line, k).String(); v != "" {
			key = v
			break
		}
	}
	if key == "" {
		key = strconv.FormatInt(end, 10) + ":" + strconv.FormatInt(tk.Input, 10) + ":" + strconv.FormatInt(tk.Output, 10)
	}
	key = string(p.m.Source()) + ":" + key
	if mine, _ := p.dedup.Claim(key, p.path); !mine {
		return
	}
	resp, seen := p.open[key]
	if !seen {
		resp = &usage.Response{Key: key, Source: p.m.Source(), Agent: p.path, EndMs: end}
		if r.Start != "" {
			if s, ok := timeOf(gjson.GetBytes(line, r.Start)); ok {
				resp.StartMs = s
			}
		} else {
			resp.StartMs = p.start
		}
		p.start = 0
		if len(p.open) > 512 {
			p.open = map[string]*usage.Response{}
		}
		p.open[key] = resp
	}
	resp.EndMs = max(resp.EndMs, end)
	resp.Tokens = resp.Tokens.Max(tk)
	if m := gjson.GetBytes(line, r.Model).String(); r.Model != "" && m != "" {
		resp.Model = strings.Clone(m)
		p.state.Model = resp.Model
	}
	if v := gjson.GetBytes(line, r.Provider).String(); r.Provider != "" && v != "" {
		resp.Provider = strings.Clone(v)
	}
	if v := gjson.GetBytes(line, r.Project).String(); r.Project != "" && v != "" {
		p.state.Project = source.Project(v)
	}
	resp.Project = p.state.Project
	resp.Final = r.Final == "" || (Cond{Path: r.Final}).match(line)
	p.state.Context = resp.Input
	if resp.Final {
		p.state.Phase, p.state.InFlightMs = usage.Idle, 0
	}
	p.sink.Response(*resp)
}
