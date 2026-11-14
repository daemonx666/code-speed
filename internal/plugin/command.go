package plugin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// command runs a command plugin's program and reads what it prints: one
// JSON object per line, each a "response", "quota" or "session".
//
// The program gets, in its environment:
//
//	CODE_SPEED_SINCE_MS   report what ended at or after this (Unix ms)
//	CODE_SPEED_STATE_DIR  a folder of its own to keep state between runs
//	CODE_SPEED_PLUGIN_DIR the plugin's folder
//
// and runs in its plugin's folder.
type command struct {
	m *Manifest
}

// Line is one line a command plugin prints.
type Line struct {
	Type string `json:"type"` // response, quota or session

	// response
	Key          string `json:"key"`
	StartMs      int64  `json:"startMs"`
	EndMs        int64  `json:"endMs"`
	Model        string `json:"model"`
	Provider     string `json:"provider"`
	Project      string `json:"project"`
	Session      string `json:"session"`
	Input        int64  `json:"input"` // all input, cache included
	CachedInput  int64  `json:"cachedInput"`
	CacheWrite   int64  `json:"cacheWrite"`
	CacheWrite1h int64  `json:"cacheWrite1h"`
	Output       int64  `json:"output"` // reasoning included
	Reasoning    int64  `json:"reasoning"`
	Fast         bool   `json:"fast"`
	Effort       string `json:"effort"` // the reasoning effort asked for
	Final        *bool  `json:"final"`  // default true

	// quota
	Plan    string              `json:"plan"`
	Windows []usage.QuotaWindow `json:"windows"`

	// session (also: model, project, context)
	Phase         string `json:"phase"` // generating, tools or idle
	InFlightMs    int64  `json:"inFlightMs"`
	LastMs        int64  `json:"lastMs"`
	Context       int64  `json:"context"`
	ContextWindow int64  `json:"contextWindow"`
}

func clamp(v int64) int64 { return min(max(v, 0), source.TokCap) }

// Apply hands one printed line to the sink.
func (l Line) Apply(m *Manifest, sink source.Sink) error {
	src := m.Source()
	switch l.Type {
	case "response":
		if l.Key == "" || l.EndMs <= 0 {
			return fmt.Errorf("a response needs key and endMs")
		}
		final := l.Final == nil || *l.Final
		agent := string(src)
		if l.Session != "" {
			agent += "#" + l.Session
		}
		sink.Response(usage.Response{Key: string(src) + ":" + l.Key, Source: src, Agent: agent,
			Project: l.Project, Model: l.Model, Provider: l.Provider, StartMs: l.StartMs, EndMs: l.EndMs,
			Tokens: usage.Tokens{Input: clamp(l.Input), CachedInput: clamp(l.CachedInput), CacheWrite: clamp(l.CacheWrite),
				CacheWrite1h: clamp(l.CacheWrite1h), Output: clamp(l.Output), Reasoning: clamp(l.Reasoning)},
			Fast: l.Fast, Effort: l.Effort, Final: final})
	case "quota":
		if len(l.Windows) == 0 {
			return fmt.Errorf("a quota needs windows")
		}
		for i := range l.Windows {
			l.Windows[i].Pace = nil
		}
		sink.Quota(usage.Quota{Source: src, Plan: l.Plan, ObservedMs: time.Now().UnixMilli(), Windows: l.Windows})
	case "session":
		if l.Session == "" {
			return fmt.Errorf("a session needs session")
		}
		ph := usage.Phase(l.Phase)
		if ph != usage.Generating && ph != usage.Tools {
			ph = usage.Idle
		}
		last := l.LastMs
		if last == 0 {
			last = time.Now().UnixMilli()
		}
		sink.Agent(usage.AgentState{Agent: string(src) + "#" + l.Session, Source: src, Project: l.Project,
			Model: l.Model, Phase: ph, InFlightMs: l.InFlightMs, LastLineMs: last,
			Context: l.Context, ContextWindow: l.ContextWindow})
	default:
		return fmt.Errorf("unknown type %q", l.Type)
	}
	return nil
}

// Run runs the program once and returns its lines (and their errors).
func Run(ctx context.Context, m *Manifest, since time.Time) ([]Line, []error, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.Timeout)*time.Second)
	defer cancel()
	prog := m.Command[0]
	if !filepath.IsAbs(prog) && (filepath.Base(prog) != prog || fileExists(filepath.Join(m.Dir, prog))) {
		prog = filepath.Join(m.Dir, prog)
	}
	state := filepath.Join(m.Dir, ".state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, prog, m.Command[1:]...)
	cmd.Dir = m.Dir
	cmd.Env = append(os.Environ(),
		"CODE_SPEED_SINCE_MS="+strconv.FormatInt(since.UnixMilli(), 10),
		"CODE_SPEED_STATE_DIR="+state,
		"CODE_SPEED_PLUGIN_DIR="+m.Dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := bytes.TrimSpace(stderr.Bytes())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, nil, fmt.Errorf("%v: %s", err, msg)
	}
	var lines []Line
	var errs []error
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	n := 0
	for sc.Scan() {
		n++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var l Line
		if err := json.Unmarshal(b, &l); err != nil {
			errs = append(errs, fmt.Errorf("line %d: %v", n, err))
			continue
		}
		lines = append(lines, l)
	}
	return lines, errs, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (c *command) Poll(ctx context.Context, since, _ time.Time, _ *source.Dedup, sink source.Sink) error {
	lines, errs, err := Run(ctx, c.m, since)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if err := l.Apply(c.m, sink); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%d bad lines, first: %v", len(errs), errs[0])
	}
	return nil
}
