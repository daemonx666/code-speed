// Package workbuddy reads Tencent WorkBuddy's transcripts: one JSONL file
// per session written by the embedded CodeBuddy Code CLI. METRICS §4.9
// (Agent Speed, MIT).
//
// The transcript has no request start, so a call starts at the latest input
// line before it (the user line or the tool result that completed a step)
// and ends at the line that carries its usage.
package workbuddy

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// WorkBuddy is the China edition (data folder ~/.workbuddy); WorkBuddyAI
// the international one (~/.workbuddy-ai). Same format.
const (
	WorkBuddy   usage.Source = "workbuddy"
	WorkBuddyAI usage.Source = "workbuddy-ai"
)

func init() {
	for _, e := range []struct {
		src          usage.Source
		label, color string
		folder       string
	}{{WorkBuddy, "WorkBuddy", "#06b6d4", ".workbuddy"}, {WorkBuddyAI, "WorkBuddy AI", "#0891b2", ".workbuddy-ai"}} {
		src, folder := e.src, e.folder
		source.Register(source.Spec{
			Source: src, Label: e.label, Color: e.color,
			Roots:  func(home string) []string { return roots(home, folder) },
			Match:  match,
			Detect: func(home string) bool { return home != "" && isDir(filepath.Join(home, folder)) },
			New: func(path string, d *source.Dedup, sink source.Sink) source.Parser {
				return newParser(src, path, d, sink)
			},
		})
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

var (
	rootMu    sync.RWMutex
	knownRoot = map[string]bool{}
)

// roots is <data folder>/projects. The app's own config-dir variables are
// not honoured (§4.9).
func roots(home, folder string) []string {
	if home == "" {
		return nil
	}
	r := filepath.Join(home, folder, "projects")
	rootMu.Lock()
	knownRoot[r] = true
	rootMu.Unlock()
	return []string{r}
}

// relParts is the path below its root, split; ok is false when the root is
// unknown.
func relParts(path string) ([]string, bool) {
	slash := filepath.ToSlash(path)
	rootMu.RLock()
	var rs []string
	for r := range knownRoot {
		rs = append(rs, r)
	}
	rootMu.RUnlock()
	for _, r := range rs {
		rr := filepath.ToSlash(filepath.Clean(r)) + "/"
		if strings.HasPrefix(slash, rr) {
			return strings.Split(slash[len(rr):], "/"), true
		}
	}
	// root not resolved yet: look for .workbuddy/projects in the path
	parts := strings.Split(slash, "/")
	for i := 0; i+1 < len(parts); i++ {
		if (parts[i] == ".workbuddy" || parts[i] == ".workbuddy-ai") && parts[i+1] == "projects" {
			return parts[i+2:], true
		}
	}
	return nil, false
}

// match picks transcripts: regular *.jsonl files at <slug>/<file> or
// <slug>/<dir>/subagents/<file>, no component starting with ".", slug not
// "cloud".
func match(path string) bool {
	parts, ok := relParts(path)
	if !ok {
		return false
	}
	file := parts[len(parts)-1]
	if !strings.HasSuffix(file, ".jsonl") || strings.HasPrefix(file, ".") {
		return false
	}
	switch len(parts) {
	case 2:
	case 4:
		if parts[2] != "subagents" || strings.HasPrefix(parts[1], ".") {
			return false
		}
	default:
		return false
	}
	if strings.HasPrefix(parts[0], ".") || parts[0] == "cloud" {
		return false
	}
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular()
}

// pySpace is Python's str.isspace for one rune.
func pySpace(r rune) bool {
	switch {
	case r >= 0x09 && r <= 0x0d, r >= 0x1c && r <= 0x20:
		return true
	case r == 0x85, r == 0xa0, r == 0x1680, r >= 0x2000 && r <= 0x200a:
		return true
	case r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

func trim(s string) string { return strings.TrimFunc(s, pySpace) }

// ---- timestamps ----

func parseTimestamp(v gjson.Result) (int64, bool) {
	switch v.Type {
	case gjson.Number:
		f := v.Num
		if math.IsInf(f, 0) || math.IsNaN(f) || f <= 0 {
			return 0, false
		}
		return trunc64(f), true
	case gjson.String:
		return parseTimeString(v.Str)
	}
	return 0, false
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

func digits(s string, n int) (int, bool) {
	if len(s) < n {
		return 0, false
	}
	v := 0
	for i := 0; i < n; i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		v = v*10 + int(s[i]-'0')
	}
	return v, true
}

func leap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

// daysFromCivil is the day number since 1970-01-01 of a proleptic
// Gregorian date.
func daysFromCivil(y, m, d int) int64 {
	if m <= 2 {
		y--
	}
	era := y / 400
	if y < 0 {
		era = (y - 399) / 400
	}
	yoe := y - era*400
	mp := (m + 9) % 12
	doy := (153*mp+2)/5 + d - 1
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return int64(era)*146097 + int64(doe) - 719468
}

// parseTimeString reads METRICS 3.1: YYYY-MM-DD, T/t/space, hh:mm:ss,
// optional fraction, Z/z or +-hh:mm / +-hhmm; ASCII digits only.
func parseTimeString(s string) (int64, bool) {
	if len(s) > 64 || len(s) < 20 {
		return 0, false
	}
	y, ok1 := digits(s, 4)
	mo, ok2 := digits(s[5:], 2)
	d, ok3 := digits(s[8:], 2)
	h, ok4 := digits(s[11:], 2)
	mi, ok5 := digits(s[14:], 2)
	se, ok6 := digits(s[17:], 2)
	if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) || s[4] != '-' || s[7] != '-' ||
		(s[10] != 'T' && s[10] != 't' && s[10] != ' ') || s[13] != ':' || s[16] != ':' {
		return 0, false
	}
	if mo < 1 || mo > 12 || d < 1 || h > 23 || mi > 59 || se > 59 {
		return 0, false
	}
	dm := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}[mo-1]
	if mo == 2 && leap(y) {
		dm = 29
	}
	if d > dm {
		return 0, false
	}
	i := 19
	var ms int64
	if s[i] == '.' {
		i++
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return 0, false
		}
		f := s[start:i]
		if len(f) > 3 {
			f = f[:3]
		}
		for len(f) < 3 {
			f += "0"
		}
		v, _ := digits(f, 3)
		ms = int64(v)
	}
	if i >= len(s) {
		return 0, false
	}
	var off int64
	switch s[i] {
	case 'Z', 'z':
		if i != len(s)-1 {
			return 0, false
		}
	case '+', '-':
		rest := s[i+1:]
		var oh, om int
		var ok bool
		switch len(rest) {
		case 5:
			if rest[2] != ':' {
				return 0, false
			}
			oh, ok = digits(rest, 2)
			if !ok {
				return 0, false
			}
			om, ok = digits(rest[3:], 2)
		case 4:
			oh, ok = digits(rest, 2)
			if !ok {
				return 0, false
			}
			om, ok = digits(rest[2:], 2)
		default:
			return 0, false
		}
		if !ok || oh > 23 || om > 59 {
			return 0, false
		}
		off = int64(oh*3600 + om*60)
		if s[i] == '-' {
			off = -off
		}
	default:
		return 0, false
	}
	secs := daysFromCivil(y, mo, d)*86400 + int64(h*3600+mi*60+se) - off
	return secs*1000 + ms, true
}

// ---- line reading ----

// member returns the last member named key of an object (a repeated
// member takes its last value, as json.loads does).
func member(obj gjson.Result, key string) gjson.Result {
	var out gjson.Result
	if !obj.IsObject() {
		return out
	}
	obj.ForEach(func(k, v gjson.Result) bool {
		if k.Str == key {
			out = v
		}
		return true
	})
	return out
}

func sub(obj gjson.Result, keys ...string) gjson.Result {
	for _, k := range keys {
		obj = member(obj, k)
		if !obj.Exists() {
			break
		}
	}
	return obj
}

// tok reads a token value (3.2): a number truncated and clamped to
// 0 … 10^12, anything else 0.
func tok(v gjson.Result) int64 {
	if v.Type != gjson.Number {
		return 0
	}
	f := v.Num
	switch {
	case math.IsNaN(f) || f <= 0:
		return 0
	case f >= source.TokCap:
		return source.TokCap
	}
	return int64(f)
}

// firstPos is the first candidate that is a finite number > 0, truncated
// and clamped; later ones are not consulted, even when it truncates to 0.
func firstPos(vs ...gjson.Result) int64 {
	for _, v := range vs {
		if v.Type == gjson.Number && !math.IsInf(v.Num, 0) && !math.IsNaN(v.Num) && v.Num > 0 {
			return tok(v)
		}
	}
	return 0
}

// sumDetails adds field of every object of an array (or of one object).
func sumDetails(arr gjson.Result, field string) int64 {
	var s int64
	add := func(o gjson.Result) {
		if o.IsObject() {
			s = source.Sat(s, tok(member(o, field)))
		}
	}
	switch {
	case arr.IsArray():
		arr.ForEach(func(_, o gjson.Result) bool { add(o); return true })
	case arr.IsObject():
		add(arr)
	}
	return s
}

func tokens(pd gjson.Result) (usage.Tokens, bool) {
	u, raw := member(pd, "usage"), member(pd, "rawUsage")
	if !u.IsObject() && !raw.IsObject() {
		return usage.Tokens{}, false
	}
	var t usage.Tokens
	t.CachedInput = max(sumDetails(member(u, "inputTokensDetails"), "cached_tokens"),
		firstPos(member(raw, "cache_read_input_tokens"),
			sub(raw, "prompt_tokens_details", "cached_tokens"),
			member(raw, "prompt_cache_hit_tokens")))
	t.CacheWrite = firstPos(member(raw, "cache_creation_input_tokens"), member(raw, "prompt_cache_write_tokens"))
	t.Input = max(tok(member(u, "inputTokens")), tok(member(raw, "prompt_tokens")),
		source.Sat(t.CachedInput, t.CacheWrite))
	t.Output = max(tok(member(u, "outputTokens")), tok(member(raw, "completion_tokens")))
	t.Reasoning = max(sumDetails(member(u, "outputTokensDetails"), "reasoning_tokens"),
		tok(sub(raw, "completion_tokens_details", "reasoning_tokens")))
	return t, t.Input > 0 || t.Output > 0
}

// modelOf resolves the model and provider of a call (§4.9 Models).
func modelOf(pd gjson.Result) (model, provider string) {
	for _, k := range []string{"model", "requestModelId"} {
		v := member(pd, k)
		if v.Type == gjson.String && trim(v.Str) != "" {
			model = trim(v.Str)
			break
		}
	}
	low := strings.ToLower(model)
	for _, p := range []string{"custom-local:", "custom:"} {
		if strings.HasPrefix(low, p) {
			return trim(model[len(p):]), "custom"
		}
	}
	return model, ""
}

func basename(cwd string) string {
	cwd = strings.TrimRight(cwd, `/\`)
	if i := strings.LastIndexAny(cwd, `/\`); i >= 0 {
		cwd = cwd[i+1:]
	}
	return cwd
}

// ---- parser ----

type parser struct {
	src   usage.Source
	path  string
	dedup *source.Dedup
	sink  source.Sink

	pendingMs int64 // 0: none
	open      map[string]string
	resp      map[string]usage.Response
	st        usage.AgentState
}

func newParser(src usage.Source, path string, d *source.Dedup, sink source.Sink) *parser {
	p := &parser{src: src, path: path, dedup: d, sink: sink, open: map[string]string{}, resp: map[string]usage.Response{}}
	p.st = usage.AgentState{Agent: path, Source: src, Phase: usage.Idle,
		Subagent: filepath.Base(filepath.Dir(path)) == "subagents"}
	return p
}

func (p *parser) State() usage.AgentState { return p.st }

func (p *parser) idle() {
	p.st.Phase, p.st.InFlightMs = usage.Idle, 0
	p.pendingMs = 0
}

func (p *parser) generating(ts int64) {
	p.st.Phase, p.st.InFlightMs = usage.Generating, ts
}

func (p *parser) Line(b []byte) {
	if len(b) > source.MaxLine || !source.IsObject(b) || !gjson.ValidBytes(b) {
		return
	}
	var typ, role, status, id, cwd, callID string
	var haveCwd, haveCall bool
	var tsv, pd gjson.Result
	gjson.ParseBytes(b).ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "type":
			typ = ""
			if v.Type == gjson.String {
				typ = v.Str
			}
		case "role":
			role = ""
			if v.Type == gjson.String {
				role = v.Str
			}
		case "status":
			status = ""
			if v.Type == gjson.String {
				status = v.Str
			}
		case "id":
			id = ""
			if v.Type == gjson.String {
				id = strings.Clone(v.Str)
			}
		case "cwd":
			haveCwd = v.Type == gjson.String && trim(v.Str) != ""
			if haveCwd {
				cwd = strings.Clone(v.Str)
			}
		case "callId":
			haveCall = v.Type == gjson.String
			if haveCall {
				callID = strings.Clone(v.Str)
			}
		case "timestamp":
			tsv = v
		case "providerData":
			pd = v
		}
		return true
	})
	ts, ok := parseTimestamp(tsv)
	if !ok {
		return
	}
	if ts > p.st.LastLineMs {
		p.st.LastLineMs = ts
	}
	if haveCwd {
		p.st.Project = basename(cwd)
	}
	if !pd.IsObject() {
		pd = gjson.Result{}
	}
	skip := member(pd, "skipRun").Type == gjson.True

	switch {
	case typ == "message" && role == "user":
		if !skip {
			p.pendingMs = ts
			clear(p.open)
			p.generating(ts)
		}
	case typ == "function_call_result":
		if haveCall {
			delete(p.open, callID)
		}
		if !skip {
			p.pendingMs = ts
			if len(p.open) == 0 {
				p.generating(ts)
			}
		} else if len(p.open) == 0 {
			p.idle()
		}
	case typ == "message" && role == "assistant" && skip:
		if status == "incomplete" || member(pd, "error").IsObject() {
			p.idle()
			clear(p.open)
		}
	case typ == "reasoning", typ == "function_call", typ == "message" && role == "assistant", typ == "model-usage":
		if typ != "model-usage" && member(pd, "isPartialAborted").Type == gjson.True {
			return
		}
		p.output(typ, role, id, callID, haveCall, ts, pd)
	}
}

func (p *parser) output(typ, role, id, callID string, haveCall bool, ts int64, pd gjson.Result) {
	key := ""
	if m := member(pd, "messageId"); m.Type == gjson.String && m.Str != "" {
		key = strings.Clone(m.Str)
	} else if id != "" {
		key = "workbuddy:" + id
	}
	if key == "" {
		return
	}
	if p.dedup != nil {
		if mine, _ := p.dedup.Claim(key, p.path); !mine {
			return
		}
	}
	if t, ok := tokens(pd); ok {
		model, provider := modelOf(pd)
		r, known := p.resp[key]
		if !known {
			r = usage.Response{Key: key, Source: p.src, Agent: p.path, StartMs: p.pendingMs, EndMs: ts, Final: true}
			p.pendingMs = 0
		}
		r.Project = p.st.Project
		r.EndMs = max(r.EndMs, ts)
		r.Tokens = r.Tokens.Max(t)
		if model != "" {
			r.Model = model
		}
		if provider != "" {
			r.Provider = provider
		}
		p.resp[key] = r
		p.st.Model = r.Model
		p.st.Context = r.Input
		p.sink.Response(r)
	}

	if typ == "model-usage" {
		p.idle()
		clear(p.open)
		return
	}
	for c, k := range p.open {
		if k != key {
			delete(p.open, c)
		}
	}
	switch typ {
	case "function_call":
		if haveCall {
			p.open[callID] = key
		}
		p.st.Phase, p.st.InFlightMs = usage.Tools, 0
	case "message":
		if len(p.open) == 0 {
			if member(pd, "agent").Str == "compact" {
				p.pendingMs = ts
				p.generating(ts)
			} else {
				p.st.Phase, p.st.InFlightMs = usage.Idle, 0
			}
		}
	}
}
