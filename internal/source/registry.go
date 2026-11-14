package source

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Spec describes one kind of agent. Each agent lives in its own file or
// subpackage and registers itself from init:
//
//	func init() { source.Register(source.Spec{...}) }
//
// An agent that writes line-by-line logs (JSONL) sets Roots, Match and
// New: the monitor walks Roots, tails the files Match picks, and feeds each
// file's lines to its own parser. An agent whose records live in a
// database, in whole-file JSON or behind an API sets Poller instead.
type Spec struct {
	Source usage.Source
	Label  string // shown name, e.g. "Claude Code"
	Color  string // CSS colour that marks the agent in the panel

	// Roots lists the folders to walk, home being the user's home folder.
	Roots func(home string) []string
	// Match picks the files to tail; nil means every *.jsonl.
	Match func(path string) bool
	// New returns the parser of one file.
	New func(path string, d *Dedup, sink Sink) Parser

	// Poller is set for agents that are read whole, Every apart.
	Poller func() Poller
	Every  time.Duration

	// Online sources talk to the network (an API with the user's sign-in).
	// They stay off until the user turns them on in Settings.
	Online bool

	// Detect reports whether the agent is installed here (its folders or
	// files exist). nil: any of Roots exists.
	Detect func(home string) bool

	// Plugin marks an agent added by a user plugin, not built in.
	Plugin bool
	// OffByDefault keeps the agent off until the user turns it on, like an
	// online one: a plugin that runs a program.
	OffByDefault bool
}

// Detected reports whether an agent seems installed on this computer.
func (s Spec) Detected() bool {
	home := homeDir()
	if s.Detect != nil && s.Detect(home) {
		return true
	}
	if s.Roots == nil {
		return false
	}
	if s.Detect == nil {
		for _, d := range s.Roots(home) {
			if _, err := os.Stat(d); err == nil {
				return true
			}
		}
	}
	// or in a place the user added
	x := currentExtra()
	for _, h := range x.Homes {
		for _, d := range s.Roots(h) {
			if _, err := os.Stat(d); err == nil {
				return true
			}
		}
	}
	for _, d := range x.Paths[s.Source] {
		if _, err := os.Stat(d); err == nil {
			return true
		}
	}
	return false
}

// Poller reads an agent whose records aren't append-only lines.
type Poller interface {
	// Poll reports what is new since the last call to sink: responses
	// (upserts by key), quotas and agent states. The first call reads
	// everything that ended at or after since.
	Poll(ctx context.Context, since, now time.Time, d *Dedup, sink Sink) error
}

var (
	regMu sync.RWMutex
	specs = map[usage.Source]Spec{}
)

// Register adds an agent kind. Registering a source twice replaces it.
func Register(s Spec) {
	regMu.Lock()
	defer regMu.Unlock()
	if s.Match == nil && s.Roots != nil {
		s.Match = func(p string) bool { return strings.HasSuffix(p, ".jsonl") }
	}
	specs[s.Source] = s
	usage.SetLabel(s.Source, s.Label, s.Color)
}

// Specs lists every registered agent kind, by label.
func Specs() []Spec {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Spec, 0, len(specs))
	for _, s := range specs {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// Lookup returns a source's spec.
func Lookup(src usage.Source) (Spec, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	s, ok := specs[src]
	return s, ok
}

// Enabled reports whether a source is read; nil means every non-online one.
type Enabled func(usage.Source) bool

// DefaultEnabled reads every local source; online ones and plugins that
// run programs stay off until turned on.
func DefaultEnabled(src usage.Source) bool {
	s, ok := Lookup(src)
	return ok && !s.Online && !s.OffByDefault
}

// Unregister removes an agent kind (a plugin that was removed or changed).
func Unregister(src usage.Source) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(specs, src)
}

// Root is a folder a line-log source writes under.
type Root struct {
	Source usage.Source
	Dir    string
}

// Extra is where else to look for logs, from the user's settings.
type Extra struct {
	// Homes are other home folders (a WSL distribution's, a mounted
	// machine's), each looked in the way the user's own is
	Homes []string
	// Paths are more log folders of one agent
	Paths map[usage.Source][]string
}

var (
	extraMu sync.RWMutex
	extra   Extra
)

// SetExtra sets where else to look for logs.
func SetExtra(x Extra) {
	extraMu.Lock()
	defer extraMu.Unlock()
	extra = x
}

func currentExtra() Extra {
	extraMu.RLock()
	defer extraMu.RUnlock()
	return extra
}

// Roots lists the log folders of every enabled line-log agent, in the
// user's home, the extra homes and the extra paths.
func Roots(enabled Enabled) []Root { return RootsWith(enabled, currentExtra()) }

// RootsWith is Roots with x for the extra places.
func RootsWith(enabled Enabled, x Extra) []Root {
	if enabled == nil {
		enabled = DefaultEnabled
	}
	homes := append([]string{homeDir()}, x.Homes...)
	var roots []Root
	seen := map[Root]bool{}
	add := func(r Root) {
		if r.Dir != "" && r.Dir != "." && !seen[r] {
			seen[r] = true
			roots = append(roots, r)
		}
	}
	for _, s := range Specs() {
		if s.Roots == nil || !enabled(s.Source) {
			continue
		}
		for i, h := range homes {
			if h == "" && i > 0 {
				continue
			}
			for _, d := range s.Roots(h) {
				if d != "" {
					add(Root{s.Source, filepath.Clean(d)})
				}
			}
		}
		for _, d := range x.Paths[s.Source] {
			if d != "" {
				add(Root{s.Source, filepath.Clean(d)})
			}
		}
	}
	return roots
}

// New returns the parser for a file of a line-log source.
func New(src usage.Source, path string, d *Dedup, sink Sink) Parser {
	s, ok := Lookup(src)
	if !ok || s.New == nil {
		return nopParser{usage.AgentState{Agent: path, Source: src, Phase: usage.Idle}}
	}
	return s.New(path, d, sink)
}

type nopParser struct{ st usage.AgentState }

func (p nopParser) Line([]byte)             {}
func (p nopParser) State() usage.AgentState { return p.st }
