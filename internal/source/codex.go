package source

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source/codextrace"
	"github.com/daemonx666/code-speed/internal/usage"
)

// codex parses a Codex rollout (~/.codex/sessions/**/rollout-*.jsonl).
// METRICS §4.2, quotas QUOTAS §3.
type codex struct {
	path  string
	dedup *Dedup
	sink  Sink

	metaSeen       bool
	fileCreatedMs  int64
	subagentOrd    int64 // -1: none
	myAgentPath    string
	threadID       string
	provider       string
	pendingStart   int64
	streaming      bool
	firstOutputMs  int64
	sawModernUsage bool
	prevLegacy     *codexUsage
	state          usage.AgentState

	// priority (fast mode) evidence: the rollout's own settings, and
	// Codex's trace database
	trace      *codextrace.Trace
	threadPrio int8 // thread_settings_applied: 1 priority, 0 not, -1 never seen
	curTurn    string
	effort     string // the current turn's reasoning effort
	prioTurns  map[string]bool
	recent     []codexPending // latest standard responses, rechecked against the trace
}

type codexPending struct {
	usage.Response
	Turn string
}

// codexRecent is how many standard responses wait for a late trace row.
const codexRecent = 64

func newCodex(path string, d *Dedup, sink Sink) *codex {
	return &codex{path: path, dedup: d, sink: sink, subagentOrd: -1, threadPrio: -1,
		state: usage.AgentState{Agent: path, Source: usage.Codex, Phase: usage.Idle}}
}

func (c *codex) State() usage.AgentState { return c.state }

type codexUsage struct {
	Input      Tok `json:"input_tokens"`
	Cached     Tok `json:"cached_input_tokens"`
	CacheWrite Tok `json:"cache_write_input_tokens"`
	Output     Tok `json:"output_tokens"`
	Reasoning  Tok `json:"reasoning_output_tokens"`
	Total      Tok `json:"total_tokens"`
}

func (u codexUsage) tokens() usage.Tokens {
	return usage.Tokens{Input: int64(u.Input), CachedInput: int64(u.Cached), CacheWrite: int64(u.CacheWrite),
		Output: int64(u.Output), Reasoning: int64(u.Reasoning)}
}

type codexWindow struct {
	UsedPercent     *float64 `json:"used_percent"`
	WindowMinutes   *float64 `json:"window_minutes"`
	ResetsAt        *float64 `json:"resets_at"`
	ResetsInSeconds *float64 `json:"resets_in_seconds"`
}

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Ordinal   json.RawMessage `json:"ordinal"`
	Payload   struct {
		Type      string `json:"type"`
		Role      string `json:"role"`
		Recipient string `json:"recipient"`
		Cwd       string `json:"cwd"`
		Model     string `json:"model"`
		Effort    string `json:"effort"` // turn_context: the reasoning effort
		// session_meta
		ID            string          `json:"id"`
		AgentPath     string          `json:"agent_path"`
		ThreadSource  string          `json:"thread_source"`
		ModelProvider string          `json:"model_provider"`
		SubagentStart json.RawMessage `json:"subagent_history_start_ordinal"`
		Source        json.RawMessage `json:"source"`
		ResponseID    string          `json:"response_id"`
		ThreadID      string          `json:"thread_id"`
		TurnID        string          `json:"turn_id"`
		// thread_settings_applied
		ThreadSettings *struct {
			ServiceTier *string `json:"service_tier"`
		} `json:"thread_settings"`
		Usage *codexUsage `json:"usage"`
		Info  *struct {
			Total  *codexUsage `json:"total_token_usage"`
			Last   *codexUsage `json:"last_token_usage"`
			Window *float64    `json:"model_context_window"`
		} `json:"info"`
		RateLimits *struct {
			LimitID   *string      `json:"limit_id"`
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
			PlanType  string       `json:"plan_type"`
			Reached   *string      `json:"rate_limit_reached_type"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

// ordinal reads a JSON integer 0…2^63−1; anything else is -1.
func ordinal(raw json.RawMessage) int64 {
	s := string(raw)
	if s == "" || strings.ContainsAny(s, ".eE-+\"") {
		return -1
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// readCodexLine reads the envelope in one pass over the top level and
// decodes the payload only for the few small records the parser uses;
// tool calls and their output, most of a rollout, are only classified.
func readCodexLine(b []byte) (l codexLine, ok bool) {
	var payload gjson.Result
	gjson.ParseBytes(b).ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "timestamp":
			l.Timestamp = v.Str
		case "type":
			l.Type = v.Str
		case "ordinal":
			l.Ordinal = json.RawMessage(v.Raw)
		case "payload":
			payload = v
		}
		return true
	})
	if !payload.IsObject() {
		return l, true
	}
	full := false
	switch l.Type {
	case "session_meta", "turn_context", "token_usage_record":
		full = true
	case "event_msg":
		switch payload.Get("type").Str {
		case "token_count", "task_started", "thread_settings_applied":
			full = true
		}
	}
	if full {
		if json.Unmarshal([]byte(payload.Raw), &l.Payload) != nil {
			return l, false
		}
		return l, true
	}
	p := &l.Payload
	payload.ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "type":
			p.Type = v.Str
		case "role":
			p.Role = v.Str
		case "recipient":
			p.Recipient = v.Str
		}
		return true
	})
	return l, true
}

func (c *codex) Line(b []byte) {
	if !IsObject(b) {
		return
	}
	l, ok := readCodexLine(b)
	if !ok {
		return
	}
	ts, ok := ParseTime(l.Timestamp)
	if !ok {
		return
	}
	p := &l.Payload
	if l.Type == "session_meta" {
		if c.metaSeen {
			return // a sub-agent copies its parent's
		}
		c.metaSeen = true
		c.fileCreatedMs = ts
		c.subagentOrd = ordinal(p.SubagentStart)
		c.state.Project = Project(p.Cwd)
		c.myAgentPath = p.AgentPath
		if c.myAgentPath == "" {
			c.myAgentPath = "/root"
		}
		c.state.Subagent = p.ThreadSource == "subagent" || strings.Contains(string(p.Source), `"subagent"`)
		c.threadID = p.ID
		c.provider = p.ModelProvider
		c.state.LastLineMs = max(c.state.LastLineMs, ts)
		return
	}
	// copied history: forks and sub-agents copy their parent's records
	if c.metaSeen && ts < c.fileCreatedMs-2000 {
		return
	}
	if c.subagentOrd >= 0 {
		if o := ordinal(l.Ordinal); o >= 0 && o < c.subagentOrd {
			return
		}
	}
	c.state.LastLineMs = max(c.state.LastLineMs, ts)

	switch {
	case l.Type == "turn_context":
		if p.TurnID != "" && (c.curTurn == "" || c.curTurn == p.TurnID) {
			c.curTurn = p.TurnID
			c.markTurn(p.TurnID)
		}
		if p.Model != "" {
			c.state.Model = p.Model
		}
		c.effort = strings.Clone(p.Effort)
		if p.Cwd != "" {
			c.state.Project = Project(p.Cwd)
		}
	case l.Type == "token_usage_record":
		c.modern(&l, ts)
	case l.Type == "event_msg" && p.Type == "token_count":
		if p.RateLimits != nil {
			c.quota(&l, ts)
		}
		if in := p.Info; in != nil {
			if in.Window != nil && *in.Window > 0 {
				c.state.ContextWindow = int64(*in.Window)
			}
			if in.Last != nil && in.Last.Input > 0 {
				c.state.Context = int64(in.Last.Input)
			}
		}
		if c.sawModernUsage && c.streaming {
			c.streaming, c.pendingStart = false, ts
			c.setPhase(usage.Tools, 0)
			return
		}
		c.legacy(&l, ts)
	case l.Type == "event_msg" && (p.Type == "task_complete" || p.Type == "turn_aborted"):
		c.pendingStart, c.streaming = 0, false
		c.setPhase(usage.Idle, 0)
		c.recheckFast()
	case l.Type == "event_msg" && p.Type == "thread_settings_applied":
		if p.ThreadSettings != nil {
			c.threadPrio = 0
			if t := p.ThreadSettings.ServiceTier; t != nil && strings.EqualFold(strings.TrimSpace(*t), "priority") {
				c.threadPrio = 1
			}
		}
	case isCodexInput(&l):
		if l.Type == "event_msg" && p.Type == "task_started" && p.TurnID != "" {
			c.curTurn = p.TurnID
			c.markTurn(p.TurnID)
		}
		if !c.streaming {
			c.pendingStart = ts
		}
		c.setPhase(usage.Generating, c.pendingStart)
	case l.Type == "response_item" && p.Type == "agent_message" && p.Recipient == c.myAgentPath:
		if !c.streaming {
			c.pendingStart = ts
		}
	case isCodexOutput(&l):
		if !c.streaming {
			c.firstOutputMs = ts
		}
		c.streaming = true
		start := c.pendingStart
		if start == 0 {
			start = ts
		}
		c.setPhase(usage.Generating, start)
	}
}

func isCodexInput(l *codexLine) bool {
	p := &l.Payload
	switch l.Type {
	case "event_msg":
		return p.Type == "task_started" || p.Type == "user_message"
	case "response_item":
		if strings.HasSuffix(p.Type, "_output") {
			return true
		}
		return p.Type == "message" && (p.Role == "user" || p.Role == "developer" || p.Role == "system")
	}
	return false
}

func isCodexOutput(l *codexLine) bool {
	p := &l.Payload
	if l.Type != "response_item" {
		return false
	}
	return p.Type == "reasoning" || strings.HasSuffix(p.Type, "_call") || (p.Type == "message" && p.Role == "assistant")
}

func (c *codex) setPhase(p usage.Phase, inFlight int64) {
	c.state.Phase = p
	c.state.InFlightMs = 0
	if p == usage.Generating {
		c.state.InFlightMs = inFlight
	}
}

func (c *codex) thread(l *codexLine) string {
	switch {
	case l.Payload.ThreadID != "":
		return l.Payload.ThreadID
	case c.threadID != "":
		return c.threadID
	}
	return c.path
}

// markTurn records whether the thread's settings put this turn at the
// priority tier when it began (CodexBar: observeThreadPriority).
func (c *codex) markTurn(turn string) {
	if c.threadPrio == 1 {
		if c.prioTurns == nil {
			c.prioTurns = map[string]bool{}
		}
		c.prioTurns[turn] = true
	} else {
		delete(c.prioTurns, turn)
	}
}

func (c *codex) isFast(thread, turn string) bool {
	if turn == "" {
		return false
	}
	return c.prioTurns[turn] || (c.trace != nil && c.trace.Priority(thread, turn))
}

// recheckFast re-emits the recent standard responses whose turn the trace
// database has since shown to be priority (its rows can land after the
// rollout's).
func (c *codex) recheckFast() {
	if c.trace == nil || len(c.recent) == 0 {
		return
	}
	c.trace.Refresh()
	kept := c.recent[:0]
	for _, r := range c.recent {
		if c.isFast(c.threadID, r.Turn) {
			r.Fast = true
			c.sink.Response(r.Response)
			continue
		}
		kept = append(kept, r)
	}
	c.recent = kept
}

func (c *codex) emit(key string, t usage.Tokens, ts int64, turn string) {
	if turn == "" {
		turn = c.curTurn
	}
	r := usage.Response{Key: key, Source: usage.Codex, Agent: c.path, Project: c.state.Project,
		Model: c.state.Model, Provider: c.provider, StartMs: c.pendingStart, EndMs: ts, Tokens: t, Final: true,
		Effort: c.effort}
	if c.streaming {
		r.FirstMs = c.firstOutputMs
	}
	c.recheckFast()
	if r.Fast = c.isFast(c.threadID, turn); !r.Fast && c.trace != nil && turn != "" {
		if len(c.recent) >= codexRecent {
			c.recent = append(c.recent[:0], c.recent[1:]...)
		}
		c.recent = append(c.recent, codexPending{r, turn})
	}
	c.sink.Response(r)
	c.pendingStart, c.streaming = ts, false
	c.setPhase(usage.Tools, 0)
}

func (c *codex) modern(l *codexLine, ts int64) {
	c.sawModernUsage = true
	key := l.Payload.ResponseID
	if key == "" {
		key = "codex:" + c.thread(l) + ":" + l.Timestamp
	}
	if _, seen := c.dedup.Claim(key, c.path); seen {
		return
	}
	var t usage.Tokens
	if l.Payload.Usage != nil {
		t = l.Payload.Usage.tokens()
		if t.Input > 0 {
			c.state.Context = t.Input
		}
	}
	c.emit(key, t, ts, l.Payload.TurnID)
}

func (c *codex) legacy(l *codexLine, ts int64) {
	if c.sawModernUsage || l.Payload.Info == nil || l.Payload.Info.Total == nil {
		return
	}
	total := *l.Payload.Info.Total
	var t usage.Tokens
	switch {
	case c.prevLegacy == nil:
		if l.Payload.Info.Last != nil {
			t = l.Payload.Info.Last.tokens()
		} else {
			t = total.tokens()
		}
	default:
		prev := *c.prevLegacy
		if total.Total-prev.Total == 0 {
			return // the same count again
		}
		d := total.tokens().Sub(prev.tokens())
		if total.Total < prev.Total || d.Input < 0 || d.CachedInput < 0 || d.CacheWrite < 0 || d.Output < 0 || d.Reasoning < 0 {
			if l.Payload.Info.Last != nil {
				t = l.Payload.Info.Last.tokens()
			}
		} else {
			t = d
		}
	}
	c.prevLegacy = &total
	key := "codex-legacy:" + c.thread(l) + ":" + strconv.FormatInt(int64(total.Total), 10)
	if _, seen := c.dedup.Claim(key, c.path); seen {
		return
	}
	c.emit(key, t, ts, "")
}

func (c *codex) quota(l *codexLine, ts int64) {
	rl := l.Payload.RateLimits
	if rl.LimitID != nil && *rl.LimitID != "codex" {
		return
	}
	q := usage.Quota{Source: usage.Codex, Plan: rl.PlanType, ObservedMs: ts}
	if rl.Reached != nil {
		q.Reached = *rl.Reached
	}
	kinds := map[string]bool{}
	for _, w := range []*codexWindow{rl.Primary, rl.Secondary} {
		if w == nil || w.UsedPercent == nil || w.WindowMinutes == nil || *w.WindowMinutes <= 0 {
			continue
		}
		mins := int64(*w.WindowMinutes)
		kind := "other"
		switch {
		case mins >= 290 && mins <= 310:
			kind = "fiveHour"
		case mins >= 10000 && mins <= 10160:
			kind = "sevenDay"
		}
		if kind != "other" && kinds[kind] {
			continue
		}
		kinds[kind] = true
		qw := usage.QuotaWindow{Kind: kind, UsedPercent: *w.UsedPercent, WindowMinutes: mins}
		switch {
		case w.ResetsAt != nil:
			qw.ResetsAtMs = int64(*w.ResetsAt) * 1000
		case w.ResetsInSeconds != nil:
			qw.ResetsAtMs = ts + int64(*w.ResetsInSeconds*1000)
		}
		q.Windows = append(q.Windows, qw)
	}
	if len(q.Windows) > 0 {
		c.sink.Quota(q)
	}
}
