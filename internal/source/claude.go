package source

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/usage"
)

// claude parses a Claude Code transcript (~/.claude/projects/**/*.jsonl).
// METRICS §4.1.
type claude struct {
	path  string
	dedup *Dedup
	sink  Sink

	pendingStart int64
	precise      bool   // pendingStart came from an api-request line
	effort       string // the last request's reasoning effort
	state        usage.AgentState

	// the file's responses by key, so repeated lines of one message merge;
	// bounded, oldest out
	open  map[string]*usage.Response
	order []string
}

const claudeOpenMax = 512

func newClaude(path string, d *Dedup, sink Sink) *claude {
	return &claude{
		path: path, dedup: d, sink: sink, open: map[string]*usage.Response{},
		state: usage.AgentState{Agent: path, Source: usage.ClaudeCode, Phase: usage.Idle,
			Subagent: HasPathDir(path, "subagents")},
	}
}

func (c *claude) State() usage.AgentState { return c.state }

type claudeUsage struct {
	Input         Tok `json:"input_tokens"`
	Output        Tok `json:"output_tokens"`
	CacheCreation Tok `json:"cache_creation_input_tokens"`
	CacheRead     Tok `json:"cache_read_input_tokens"`
	Creation      struct {
		OneHour Tok `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Details struct {
		Thinking Tok `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
	Speed string `json:"speed"`
}

type claudeLine struct {
	Type         string `json:"type"`
	Timestamp    string `json:"timestamp"`
	Cwd          string `json:"cwd"`
	IsSidechain  bool   `json:"isSidechain"`
	ToolEndsTurn bool   `json:"toolEndsTurn"`
	RequestID    string `json:"requestId"`
	Effort       string // an api-request line's params.output_config.effort
	UUID         string `json:"uuid"`
	Message      struct {
		ID         string          `json:"id"`
		Model      string          `json:"model"`
		StopReason *string         `json:"stop_reason"`
		Usage      *claudeUsage    `json:"usage"`
		Content    json.RawMessage `json:"content"` // a user line's, when it may be an interruption
		toolUse    bool            // an assistant line's content has a tool_use block
	} `json:"message"`
}

var interruptedPrefix = []byte("[Request interrupted by user")

// readClaudeLine picks the fields the parser reads in one pass over the
// line's top level. Most of a transcript is tool output and message text,
// which gjson skips over instead of decoding; only an assistant message's
// small parts are decoded.
//
// Every string kept past the line is cloned: gjson's strings point into
// the line, and keeping one would keep the whole line, tool output and all.
func readClaudeLine(b []byte) (l claudeLine, ok bool) {
	var msg, params gjson.Result
	gjson.ParseBytes(b).ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "type":
			l.Type = v.Str
		case "timestamp":
			l.Timestamp = v.Str
		case "cwd":
			l.Cwd = strings.Clone(v.Str)
		case "isSidechain":
			l.IsSidechain = v.Type == gjson.True
		case "toolEndsTurn":
			l.ToolEndsTurn = v.Type == gjson.True
		case "requestId":
			l.RequestID = strings.Clone(v.Str)
		case "uuid":
			l.UUID = strings.Clone(v.Str)
		case "message":
			msg = v
		case "params":
			params = v
		}
		return true
	})
	if l.Type == "api-request" && params.IsObject() {
		l.Effort = strings.Clone(params.Get("output_config.effort").Str)
	}
	if !msg.IsObject() {
		return l, true
	}
	switch l.Type {
	case "assistant":
		m := &l.Message
		msg.ForEach(func(k, v gjson.Result) bool {
			switch k.Str {
			case "id":
				m.ID = strings.Clone(v.Str)
			case "model":
				m.Model = strings.Clone(v.Str)
			case "stop_reason":
				if v.Type != gjson.Null {
					s := strings.Clone(v.String())
					m.StopReason = &s
				}
			case "usage":
				if v.IsObject() {
					var u claudeUsage
					if json.Unmarshal([]byte(v.Raw), &u) == nil {
						m.Usage = &u
					}
				}
			case "content":
				m.toolUse = v.IsArray() && v.Get(`#(type=="tool_use")`).Exists()
			}
			return true
		})
	case "user":
		// the marker is looked for only when the line holds it at all
		if bytes.Contains(b, interruptedPrefix) {
			l.Message.Content = json.RawMessage(msg.Get("content").Raw)
		}
	}
	return l, true
}

func (c *claude) Line(b []byte) {
	if !IsObject(b) {
		return
	}
	l, ok := readClaudeLine(b)
	if !ok {
		return
	}
	ts, ok := ParseTime(l.Timestamp)
	if !ok {
		return
	}
	c.state.LastLineMs = max(c.state.LastLineMs, ts)
	if l.Cwd != "" {
		c.state.Project = Project(l.Cwd)
	}
	if l.IsSidechain {
		c.state.Subagent = true
	}
	switch l.Type {
	case "api-request":
		c.pendingStart, c.precise = ts, true
		c.effort = l.Effort
		c.setPhase(usage.Generating, ts)
	case "user":
		if l.ToolEndsTurn || interrupted(l.Message.Content) {
			c.pendingStart, c.precise = 0, false
			c.setPhase(usage.Idle, 0)
			return
		}
		if !c.precise {
			c.pendingStart = ts
		}
		c.setPhase(usage.Generating, c.pendingStart)
	case "assistant":
		c.assistant(&l, ts)
	}
}

func (c *claude) setPhase(p usage.Phase, inFlight int64) {
	c.state.Phase = p
	c.state.InFlightMs = 0
	if p == usage.Generating {
		c.state.InFlightMs = inFlight
	}
}

// interrupted reports a user message that starts with Claude Code's
// "[Request interrupted by user" marker, as a string or a text block.
func interrupted(content json.RawMessage) bool {
	if !bytes.Contains(content, interruptedPrefix) {
		return false
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.HasPrefix(s, string(interruptedPrefix))
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, bl := range blocks {
		if bl.Type == "text" && strings.HasPrefix(bl.Text, string(interruptedPrefix)) {
			return true
		}
	}
	return false
}

func (c *claude) assistant(l *claudeLine, ts int64) {
	m := &l.Message
	if m.Model == "<synthetic>" {
		return
	}
	key := m.ID
	if key == "" {
		key = l.RequestID
	}
	if key == "" {
		key = l.UUID
	}
	if key == "" {
		return
	}
	if mine, _ := c.dedup.Claim(key, c.path); !mine {
		return
	}
	r, ok := c.open[key]
	if !ok {
		r = &usage.Response{Key: key, Source: usage.ClaudeCode, Agent: c.path, StartMs: c.pendingStart,
			FirstMs: ts, EndMs: ts, Effort: c.effort}
		c.remember(key, r)
		c.pendingStart, c.precise = 0, false
	}
	r.EndMs = max(r.EndMs, ts)
	r.FirstMs = min(r.FirstMs, ts)
	if u := m.Usage; u != nil {
		t := usage.Tokens{
			Input:        Sat(Sat(int64(u.Input), int64(u.CacheCreation)), int64(u.CacheRead)),
			CachedInput:  int64(u.CacheRead),
			CacheWrite:   int64(u.CacheCreation),
			CacheWrite1h: min(int64(u.Creation.OneHour), int64(u.CacheCreation)),
			Output:       int64(u.Output),
			Reasoning:    int64(u.Details.Thinking),
		}
		r.Tokens = r.Tokens.Max(t)
		r.Fast = r.Fast || u.Speed == "fast"
	}
	if m.Model != "" {
		r.Model = m.Model
		c.state.Model = m.Model
	}
	r.Project = c.state.Project
	r.Final = r.Final || m.StopReason != nil
	c.sink.Response(*r)
	if m.Usage != nil && r.Input > 0 {
		c.state.Context = r.Input
		c.state.ContextWindow = ClaudeWindow(r.Model, r.Input)
	}

	switch {
	case m.StopReason == nil:
		if m.toolUse {
			c.setPhase(usage.Tools, 0)
		} else {
			start := r.StartMs
			if start == 0 {
				start = ts
			}
			c.setPhase(usage.Generating, start)
		}
	case *m.StopReason == "tool_use":
		c.setPhase(usage.Tools, 0)
	case *m.StopReason == "pause_turn":
		start := r.StartMs
		if start == 0 {
			start = ts
		}
		c.setPhase(usage.Generating, start)
	default:
		c.setPhase(usage.Idle, 0)
	}
}

func (c *claude) remember(key string, r *usage.Response) {
	c.open[key] = r
	c.order = append(c.order, key)
	if len(c.order) > claudeOpenMax {
		delete(c.open, c.order[0])
		c.order = c.order[1:]
	}
}

// ClaudeWindow is a Claude model's context window. Claude Code's logs
// don't say it: models since Opus and Sonnet 4.6 and Haiku 5.5 have 1M
// tokens; earlier ones 200K, unless a context already went past that (the
// 1M beta). Another vendor's model behind Claude Code is unknown (0).
func ClaudeWindow(model string, context int64) int64 {
	m := strings.ToLower(model)
	switch {
	case !strings.HasPrefix(m, "claude-"):
		return 0
	case hasAnyPrefixStr(m, "claude-fable-", "claude-mythos-", "claude-opus-5", "claude-sonnet-5",
		"claude-haiku-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-4-6"):
		return 1_000_000
	case context > 200_000:
		return 1_000_000
	}
	return 200_000
}

func hasAnyPrefixStr(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
