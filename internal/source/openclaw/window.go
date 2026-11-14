package openclaw

import (
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// window is the state machine of one transcript window: the entries of a
// Pi session file (METRICS 4.7 and 4.13) with the differences of 4.14.
type window struct {
	id    string // agent id: database path # session id
	owner string // dedupe owner: the window itself, so forks dedupe against their parent
	dedup *source.Dedup
	sink  source.Sink

	// from session_windows, refreshed at every poll
	subagent  bool
	acpOwned  bool
	harness   string // trimmed, lower-cased
	delegated bool

	haveHeader bool
	headerMs   int64
	hasHeaderT bool
	pending    map[string]bool
	st         usage.AgentState
}

func newWindow(id string, d *source.Dedup) *window {
	return &window{id: id, owner: id, dedup: d, pending: map[string]bool{},
		st: usage.AgentState{Agent: id, Source: OpenClaw, Phase: usage.Idle}}
}

// setRow applies the window row's facts.
func (w *window) setRow(spawnedBy string, acpOwned bool, harness string) {
	w.subagent = spawnedBy != ""
	w.st.Subagent = w.subagent
	w.acpOwned = acpOwned
	w.harness = strings.ToLower(strings.TrimSpace(harness))
	switch w.harness {
	case "", "openclaw", "pi", "auto", "default":
		w.delegated = acpOwned
	default:
		w.delegated = true
	}
}

func (w *window) state() usage.AgentState { return w.st }

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

// entry feeds one transcript entry.
func (w *window) entry(b []byte) {
	if len(b) > source.MaxLine || !source.IsObject(b) {
		return
	}
	var typ, id, ts, cwd string
	var msg gjson.Result
	gjson.ParseBytes(b).ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "type":
			typ = v.Str
		case "id":
			if v.Type == gjson.String {
				id = strings.Clone(v.Str)
			}
		case "timestamp":
			ts = v.Str
		case "cwd":
			cwd = strings.Clone(v.Str)
		case "message":
			msg = v
		}
		return true
	})
	if typ == "session" {
		if w.haveHeader {
			return
		}
		w.haveHeader = true
		if ms, ok := source.ParseTime(ts); ok {
			w.headerMs, w.hasHeaderT = ms, true
			w.touch(ms)
		}
		w.st.Project = basename(cwd)
		return
	}
	if !w.haveHeader || id == "" {
		return
	}
	ms, ok := source.ParseTime(ts)
	if !ok {
		return
	}
	if w.hasHeaderT && ms < w.headerMs-2000 {
		return // copied history
	}
	w.touch(ms)
	if typ == "message" && msg.IsObject() {
		switch msg.Get("role").String() {
		case "user", "developer":
			clear(w.pending)
			w.generating(ms)
		case "assistant":
			w.assistant(id, msg, ms)
		case "toolResult":
			delete(w.pending, msg.Get("toolCallId").String())
			if w.st.Phase == usage.Tools && len(w.pending) == 0 {
				w.generating(ms)
			}
		}
	}
	if w.delegated { // its turns run in another program
		w.idle()
	}
}

func (w *window) touch(ms int64) {
	if ms > w.st.LastLineMs {
		w.st.LastLineMs = ms
	}
}

func (w *window) idle() {
	w.st.Phase, w.st.InFlightMs = usage.Idle, 0
	clear(w.pending)
}

func (w *window) generating(ms int64) {
	w.st.Phase, w.st.InFlightMs = usage.Generating, ms
}

func (w *window) mirror(m gjson.Result, api, prov string) bool {
	if o := m.Get("__openclaw.mirrorOrigin"); o.Type == gjson.String && o.Str != "" {
		return true
	}
	if api == "cli" && prov == "claude-cli" {
		return true
	}
	if strings.HasPrefix(prov, "acp-") || w.acpOwned {
		return true
	}
	return w.harness == "codex" || w.harness == "claude-cli" || strings.HasPrefix(w.harness, "acp")
}

func (w *window) assistant(id string, m gjson.Result, ts int64) {
	model, prov, api, u := m.Get("model"), m.Get("provider"), m.Get("api"), m.Get("usage")
	if model.Type != gjson.String || prov.Type != gjson.String || api.Type != gjson.String || !u.IsObject() {
		return
	}
	tsF, tsOK := num(m.Get("timestamp"))
	tsOK = tsOK && tsF > 0
	stamp, startMs := ts, int64(0)
	if tsOK {
		stamp = trunc64(tsF)
		startMs = stamp
	}
	key := "openclaw:" + id + ":" + strconv.FormatInt(stamp, 10)
	if w.dedup != nil {
		if mine, _ := w.dedup.Claim(key, w.owner); !mine {
			return
		}
	}
	stop := "aborted"
	switch s := m.Get("stopReason"); {
	case s.Type != gjson.String:
	case s.Str == "stop" || s.Str == "length" || s.Str == "toolUse" || s.Str == "error":
		stop = s.Str
	}
	failed := stop == "error" || stop == "aborted"

	in, out := tok(u.Get("input")), tok(u.Get("output"))
	cr, cw := tok(u.Get("cacheRead")), tok(u.Get("cacheWrite"))
	any := in > 0 || out > 0 || cr > 0 || cw > 0
	unavailable := u.Get("usageReport.state").String() == "unavailable"
	if any && !unavailable && !w.mirror(m, api.Str, prov.Str) {
		r := usage.Response{Key: key, Source: OpenClaw, Agent: w.id, Project: w.st.Project,
			Model: strings.Clone(model.Str), Provider: strings.Clone(prov.Str),
			StartMs: startMs, EndMs: ts, Final: true}
		r.Input = source.Sat(source.Sat(in, cr), cw)
		r.CachedInput, r.CacheWrite, r.Output = cr, cw, out
		r.Reasoning = tok(u.Get("reasoningTokens"))
		r.CacheWrite1h = min(tok(u.Get("cacheWrite1h")), cw)
		if failed || w.delegated || api.Str == "cli" {
			r.StartMs = 0 // totals only, or the usage of a whole turn
		}
		w.st.Model = r.Model
		w.st.Context = r.Input
		w.sink.Response(r)
	}

	if failed {
		w.idle()
		return
	}
	var ids []string
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
	if len(ids) > 0 {
		clear(w.pending)
		for _, id := range ids {
			w.pending[strings.Clone(id)] = true
		}
		w.st.Phase, w.st.InFlightMs = usage.Tools, 0
		return
	}
	w.idle()
}
