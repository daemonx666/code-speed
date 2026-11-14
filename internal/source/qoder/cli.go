package qoder

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// QoderCLI is the source id of Qoder CLI.
const QoderCLI usage.Source = "qoder-cli"

func init() {
	source.Register(source.Spec{
		Source: QoderCLI,
		Label:  "Qoder CLI",
		Color:  "#7c3aed",
		Roots:  func(home string) []string { return cliRoots(os.Getenv, home) },
		New: func(path string, d *source.Dedup, sink source.Sink) source.Parser {
			return newCLI(path, d, sink)
		},
	})
}

// cli parses a Qoder CLI transcript: Claude Code's format (METRICS §4.1)
// with the differences of §4.19.
type cli struct {
	path  string
	dedup *source.Dedup
	sink  source.Sink

	pendingStart int64
	precise      bool
	state        usage.AgentState

	open  map[string]*usage.Response
	order []string
}

const cliOpenMax = 512

func newCLI(path string, d *source.Dedup, sink source.Sink) *cli {
	return &cli{path: path, dedup: d, sink: sink, open: map[string]*usage.Response{},
		state: usage.AgentState{Agent: path, Source: QoderCLI, Phase: usage.Idle,
			Subagent: source.HasPathDir(path, "subagents")}}
}

func (c *cli) State() usage.AgentState { return c.state }

type cliUsage struct {
	Input         source.Tok `json:"input_tokens"`
	Output        source.Tok `json:"output_tokens"`
	CacheCreation source.Tok `json:"cache_creation_input_tokens"`
	CacheRead     source.Tok `json:"cache_read_input_tokens"`
	Creation      struct {
		OneHour source.Tok `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Details struct {
		Thinking source.Tok `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

type cliLine struct {
	typ, ts, cwd, uuid  string
	sidechain, endsTurn bool
	id, model           string
	stop                *string
	usage               *cliUsage
	content             json.RawMessage
	toolUse             bool
}

var interruptedPrefix = []byte("[Request interrupted by user")

func readCLILine(b []byte) (l cliLine) {
	var msg gjson.Result
	gjson.ParseBytes(b).ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "type":
			l.typ = v.Str
		case "timestamp":
			l.ts = v.Str
		case "cwd":
			l.cwd = strings.Clone(v.Str)
		case "isSidechain":
			l.sidechain = v.Type == gjson.True
		case "toolEndsTurn":
			l.endsTurn = v.Type == gjson.True
		case "uuid":
			l.uuid = strings.Clone(v.Str)
		case "message":
			msg = v
		}
		return true
	})
	if !msg.IsObject() {
		return l
	}
	switch l.typ {
	case "assistant":
		msg.ForEach(func(k, v gjson.Result) bool {
			switch k.Str {
			case "id":
				l.id = strings.Clone(v.Str)
			case "model":
				l.model = strings.Clone(v.Str)
			case "stop_reason":
				if v.Type != gjson.Null {
					s := strings.Clone(v.String())
					l.stop = &s
				}
			case "usage":
				if v.IsObject() {
					var u cliUsage
					if json.Unmarshal([]byte(v.Raw), &u) == nil {
						l.usage = &u
					}
				}
			case "content":
				l.toolUse = v.IsArray() && v.Get(`#(type=="tool_use")`).Exists()
			}
			return true
		})
	case "user":
		if bytes.Contains(b, interruptedPrefix) {
			l.content = json.RawMessage(msg.Get("content").Raw)
		}
	}
	return l
}

func (c *cli) Line(b []byte) {
	if !source.IsObject(b) {
		return
	}
	l := readCLILine(b)
	ts, ok := source.ParseTime(l.ts)
	if !ok {
		return
	}
	c.state.LastLineMs = max(c.state.LastLineMs, ts)
	if l.cwd != "" {
		c.state.Project = source.Project(l.cwd)
	}
	if l.sidechain {
		c.state.Subagent = true
	}
	switch l.typ {
	case "user":
		if l.endsTurn || interrupted(l.content) {
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

func (c *cli) setPhase(p usage.Phase, inFlight int64) {
	c.state.Phase = p
	c.state.InFlightMs = 0
	if p == usage.Generating {
		c.state.InFlightMs = inFlight
	}
}

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

// tokens: Qoder's input_tokens already includes the cache read for its own
// models; a custom Anthropic provider's does not (Claude's sum then).
func (u *cliUsage) tokens() usage.Tokens {
	cr, cw := int64(u.CacheRead), int64(u.CacheCreation)
	in := int64(u.Input)
	if in < source.Sat(cr, cw) {
		in = source.Sat(source.Sat(in, cw), cr)
	}
	return usage.Tokens{
		Input:        in,
		CachedInput:  cr,
		CacheWrite:   cw,
		CacheWrite1h: min(int64(u.Creation.OneHour), cw),
		Output:       int64(u.Output),
		Reasoning:    int64(u.Details.Thinking),
	}
}

func (c *cli) assistant(l *cliLine, ts int64) {
	if l.model == "<synthetic>" {
		return
	}
	id := l.id
	if id == "" {
		id = l.uuid
	}
	if id == "" {
		return
	}
	key := "qodercli:" + id
	if mine, _ := c.dedup.Claim(key, c.path); !mine {
		return
	}
	var t usage.Tokens
	hasUsage := false
	if l.usage != nil {
		t = l.usage.tokens()
		hasUsage = t.Input != 0 || t.Output != 0
	}
	r := c.open[key]
	var start int64
	if hasUsage {
		if r == nil {
			r = &usage.Response{Key: key, Source: QoderCLI, Agent: c.path, StartMs: c.pendingStart, EndMs: ts}
			c.remember(key, r)
		}
		r.EndMs = max(r.EndMs, ts)
		r.Tokens = r.Tokens.Max(t)
		if l.model != "" {
			r.Model = l.model
		}
		r.Project = c.state.Project
		r.Final = r.Final || l.stop != nil
		c.sink.Response(*r)
		c.state.Context = r.Input
		start = r.StartMs
	}
	// a line without usage consumes the pending start too
	c.pendingStart, c.precise = 0, false
	if l.model != "" {
		c.state.Model = l.model
	}
	if start == 0 {
		start = ts
	}
	switch {
	case l.stop == nil:
		if l.toolUse {
			c.setPhase(usage.Tools, 0)
		} else {
			c.setPhase(usage.Generating, start)
		}
	case *l.stop == "tool_use":
		c.setPhase(usage.Tools, 0)
	case *l.stop == "pause_turn":
		c.setPhase(usage.Generating, start)
	default:
		c.setPhase(usage.Idle, 0)
	}
}

func (c *cli) remember(key string, r *usage.Response) {
	c.open[key] = r
	c.order = append(c.order, key)
	if len(c.order) > cliOpenMax {
		delete(c.open, c.order[0])
		c.order = c.order[1:]
	}
}
