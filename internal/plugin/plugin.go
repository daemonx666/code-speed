// Package plugin lets users add agents code-speed doesn't know, without
// rebuilding it. A plugin is a folder in appdir/plugins with a
// plugin.json. Two kinds:
//
//   - "jsonl": declarative. Which files to read, which lines are model
//     replies, and where in each line the model, times and tokens are
//     (gjson paths). No code.
//   - "command": a program in any language that code-speed runs every few
//     seconds; it prints JSON lines (responses, quotas, sessions). Off
//     until turned on in Settings, since it runs code.
//
// See docs/plugins.md.
package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/appdir"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Manifest is a plugin.json.
type Manifest struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Color string `json:"color"`
	Type  string `json:"type"` // "jsonl" or "command"

	// jsonl
	Roots  []string `json:"roots"`  // folders, "~" for home; env vars like $MY_HOME expand
	Match  string   `json:"match"`  // glob on the file name, default "*.jsonl"
	Record Record   `json:"record"` // how a reply line is read

	// command
	Command []string `json:"command"` // program and arguments; a relative program is in the plugin's folder
	Every   int      `json:"every"`   // seconds between runs, default 30
	Online  bool     `json:"online"`  // the program talks to the network
	Timeout int      `json:"timeout"` // seconds a run may take, default 60

	// Dir is the plugin's folder (not read from the file).
	Dir string `json:"-"`
}

// Record maps a reply line's fields (gjson paths, see
// https://github.com/tidwall/gjson/blob/master/SYNTAX.md).
type Record struct {
	When     Cond     `json:"when"`  // which lines are replies; empty: every line with tokens
	Key      []string `json:"key"`   // first non-empty path is the reply's id; repeated ids merge
	Time     string   `json:"time"`  // when the reply ended: RFC 3339, Unix s or ms
	Start    string   `json:"start"` // optional: when the request started, on the same line
	Model    string   `json:"model"`
	Provider string   `json:"provider"`
	Project  string   `json:"project"` // a working folder; its last part is shown
	Tokens   struct {
		Input        string `json:"input"`
		CachedInput  string `json:"cachedInput"`
		CacheWrite   string `json:"cacheWrite"`
		CacheWrite1h string `json:"cacheWrite1h"`
		Output       string `json:"output"`
		Reasoning    string `json:"reasoning"`
	} `json:"tokens"`
	// InputExcludesCache: the input count leaves out cache reads and
	// writes (Anthropic's way), so they are added to it
	InputExcludesCache bool `json:"inputExcludesCache"`
	// ReasoningExcluded: output leaves reasoning out, so it is added
	ReasoningExcluded bool `json:"reasoningExcluded"`
	// RequestStart: lines that start a request (a user message, a tool
	// result); the next reply's start is the time of the latest one
	RequestStart Cond `json:"requestStart"`
	// Final: optional path that is non-empty/true on a reply's last line;
	// without it every reply line is final
	Final string `json:"final"`
}

// Cond picks lines: path equals one of values, or (no values) exists.
type Cond struct {
	Path   string   `json:"path"`
	Equals []string `json:"equals"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,40}$`)

// Validate checks a manifest and fills defaults.
func (m *Manifest) Validate() error {
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id %q: lowercase letters, digits, '.', '_' or '-', at most 41", m.ID)
	}
	if m.Label == "" {
		m.Label = m.ID
	}
	if m.Color == "" {
		m.Color = "#64748b"
	}
	switch m.Type {
	case "jsonl":
		if len(m.Roots) == 0 {
			return fmt.Errorf("jsonl plugin needs roots")
		}
		if m.Match == "" {
			m.Match = "*.jsonl"
		}
		if _, err := filepath.Match(m.Match, "x"); err != nil {
			return fmt.Errorf("match: %v", err)
		}
		if m.Record.Time == "" || m.Record.Tokens.Output == "" && m.Record.Tokens.Input == "" {
			return fmt.Errorf("record needs time and tokens.input or tokens.output")
		}
	case "command":
		if len(m.Command) == 0 {
			return fmt.Errorf("command plugin needs command")
		}
		if m.Every <= 0 {
			m.Every = 30
		}
		if m.Timeout <= 0 {
			m.Timeout = 60
		}
	default:
		return fmt.Errorf("type must be \"jsonl\" or \"command\", not %q", m.Type)
	}
	return nil
}

// Source is the agent id a plugin registers: "plugin.<id>", so a plugin
// can't take a built-in agent's place.
func (m *Manifest) Source() usage.Source { return usage.Source("plugin." + m.ID) }

// Dir is where plugins live.
func Dir() string { return filepath.Join(appdir.Dir(), "plugins") }

// Loaded is a plugin found in Dir, or why it couldn't be used.
type Loaded struct {
	Manifest
	Error string `json:"error,omitempty"`
}

// Read reads one plugin folder.
func Read(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("plugin.json: %v", err)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	m.Dir = dir
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

var registered []usage.Source

// LoadAll registers every plugin in Dir (replacing those registered
// before) and lists them, the broken ones with their error.
func LoadAll() []Loaded {
	for _, src := range registered {
		source.Unregister(src)
	}
	registered = nil
	entries, _ := os.ReadDir(Dir())
	var out []Loaded
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(Dir(), e.Name())
		m, err := Read(dir)
		if err != nil {
			out = append(out, Loaded{Manifest: Manifest{ID: e.Name(), Dir: dir}, Error: err.Error()})
			continue
		}
		if seen[m.ID] {
			out = append(out, Loaded{Manifest: *m, Error: "another plugin has this id"})
			continue
		}
		seen[m.ID] = true
		source.Register(m.Spec())
		registered = append(registered, m.Source())
		out = append(out, Loaded{Manifest: *m})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Spec is the plugin as an agent kind.
func (m *Manifest) Spec() source.Spec {
	s := source.Spec{Source: m.Source(), Label: m.Label, Color: m.Color, Plugin: true}
	switch m.Type {
	case "jsonl":
		s.Roots = func(home string) []string {
			var out []string
			for _, r := range m.Roots {
				out = append(out, expand(r, home))
			}
			return out
		}
		pattern := m.Match
		s.Match = func(p string) bool {
			ok, _ := filepath.Match(pattern, filepath.Base(p))
			return ok
		}
		s.New = func(path string, d *source.Dedup, sink source.Sink) source.Parser {
			return newJSONL(m, path, d, sink)
		}
	case "command":
		s.Poller = func() source.Poller { return &command{m: m} }
		s.Every = time.Duration(m.Every) * time.Second
		s.Online = m.Online
		s.OffByDefault = true
		s.Detect = func(string) bool { return true }
	}
	return s
}

// expand turns "~" into home and $VARS into their values.
func expand(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		p = filepath.Join(home, p[1:])
	}
	return filepath.Clean(os.ExpandEnv(p))
}
