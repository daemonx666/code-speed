// Package droid reads Factory Droid's sessions (Agent Speed spec
// METRICS.md §4.16, MIT). Each session is a transcript that times every
// model call but records no usage, plus a sidecar settings file whose
// cumulative token counters droid rewrites as the session runs. The calls
// come from the transcript; their tokens are the growth of the counters,
// allocated to the calls written since. That does not fit a line parser
// (the sidecar changes on its own), so this is a Poller.
package droid

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Droid is the source id.
const Droid usage.Source = "droid"

func init() {
	source.Register(source.Spec{
		Source: Droid,
		Label:  "Factory Droid",
		Color:  "#f97316",
		Detect: func(home string) bool {
			if home == "" {
				return false
			}
			_, err := os.Stat(filepath.Join(home, ".factory"))
			return err == nil
		},
		Poller: func() source.Poller { return &poller{} },
		Every:  2 * time.Second,
	})
}

// root is ~/.factory/sessions (Factory documents no override).
func root(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".factory", "sessions")
}

type poller struct {
	home string // set by tests; else the user's home folder
	sess map[string]*session
}

func (p *poller) root() string {
	h := p.home
	if h == "" {
		h, _ = os.UserHomeDir()
	}
	return root(h)
}

// discovered is a session transcript found on disk.
type discovered struct {
	path  string
	newer time.Time // the newer of the transcript's and the sidecar's times
}

// discover lists the sessions under dir: regular .jsonl files (not
// symlinks, not starting with ".") directly in it or in a folder directly
// in it (not a symlink, not starting with ".").
func discover(dir string) []discovered {
	var out []discovered
	var scan func(d string, deeper bool)
	scan = func(d string, deeper bool) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			switch {
			case e.IsDir() && deeper:
				scan(filepath.Join(d, name), false)
			case e.Type().IsRegular() && strings.HasSuffix(name, ".jsonl"):
				fi, err := e.Info()
				if err != nil || !fi.Mode().IsRegular() {
					continue
				}
				path := filepath.Join(d, name)
				newer := fi.ModTime()
				if sfi, err := os.Lstat(sidecarPath(path)); err == nil && sfi.ModTime().After(newer) {
					newer = sfi.ModTime()
				}
				out = append(out, discovered{path, newer})
			}
		}
	}
	scan(dir, true)
	return out
}

func sidecarPath(transcript string) string {
	return strings.TrimSuffix(transcript, ".jsonl") + ".settings.json"
}

func (p *poller) Poll(_ context.Context, since, _ time.Time, d *source.Dedup, sink source.Sink) error {
	dir := p.root()
	if dir == "" {
		return nil
	}
	if p.sess == nil {
		p.sess = map[string]*session{}
	}
	found := discover(dir)
	// startup order follows the transcripts: a fork's copied messages are
	// removed by deduplication when its parent is read first
	sort.SliceStable(found, func(i, j int) bool { return found[i].newer.Before(found[j].newer) })
	seen := make(map[string]bool, len(found))
	for _, f := range found {
		seen[f.path] = true
		s := p.sess[f.path]
		if s == nil {
			s = newSession(f.path, d, sink)
			s.atEnd = f.newer.Before(since)
			p.sess[f.path] = s
		}
		// a session untouched since before the window needs no look
		if s.cold && f.newer.Before(since) && f.newer.Equal(s.lastNewer) {
			continue
		}
		s.lastNewer = f.newer
		s.cycle()
		s.cold = f.newer.Before(since) && !s.retry()
	}
	for path := range p.sess {
		if !seen[path] {
			delete(p.sess, path)
		}
	}
	return nil
}
