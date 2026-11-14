package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// historyVersion changes when a saved day would be read differently, or
// its numbers made differently (2: Codex fast-tier prices; 3: speed and
// first-content spread per model; 4: reasoning effort), so the history
// is rebuilt from the logs once.
const historyVersion = 4

type historyFile struct {
	Version int `json:"version"`
	// Closed is, per agent, the last day counted here
	Closed map[usage.Source]string `json:"closed"`
	Days   []*day                  `json:"days"`
}

// SetSources tells the engine which agents are being read, so the history
// closes their days and the logs are reread for an agent turned on.
func (e *Engine) SetSources(srcs []usage.Source) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active = srcs
}

// activeSources lists the agents read: the ones set, else every one seen.
// Called with mu held.
func (e *Engine) activeSources() []usage.Source {
	if e.active != nil {
		return e.active
	}
	seen := map[usage.Source]bool{}
	for src := range e.closed {
		seen[src] = true
	}
	for _, d := range e.days {
		for src := range d.bySource {
			seen[src] = true
		}
	}
	var out []usage.Source
	for src := range seen {
		out = append(out, src)
	}
	return out
}

// LoadHistory reads the days kept by SaveHistory. A missing file is no
// history; a file that can't be read is set aside, not overwritten, and
// the history is rebuilt from the logs.
func (e *Engine) LoadHistory(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var h historyFile
	if err := json.Unmarshal(b, &h); err != nil {
		_ = os.Rename(path, path+".unreadable")
		return nil
	}
	if h.Version != historyVersion {
		// kept, not deleted: an older build reads it if the user goes back
		_ = os.Rename(path, fmt.Sprintf("%s.v%d", path, h.Version))
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range h.Days {
		if d != nil {
			e.days[d.date] = d
		}
	}
	for src, c := range h.Closed {
		e.closed[src] = c
	}
	return nil
}

// SaveHistory writes every day before today to path and closes those days
// for the agents being read: a response of theirs that ends on one is not
// counted again. Written to a temporary file and renamed over the old.
func (e *Engine) SaveHistory(path string, nowMs int64) error {
	e.mu.Lock()
	today := e.dayOf(nowMs)
	yesterday := time.UnixMilli(nowMs).In(e.loc).AddDate(0, 0, -1).Format("2006-01-02")
	h := historyFile{Version: historyVersion, Closed: map[usage.Source]string{}}
	for src, c := range e.closed {
		h.Closed[src] = c
	}
	for _, src := range e.activeSources() {
		if h.Closed[src] < yesterday {
			h.Closed[src] = yesterday
		}
	}
	for date, d := range e.days {
		if date < today {
			h.Days = append(h.Days, d)
		}
	}
	sort.Slice(h.Days, func(i, j int) bool { return h.Days[i].date < h.Days[j].date })
	b, err := json.Marshal(h)
	e.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	e.mu.Lock()
	for src, c := range h.Closed {
		if c > e.closed[src] {
			e.closed[src] = c
		}
	}
	e.mu.Unlock()
	return nil
}

// SeedSince is where reading the logs starts: the day after the earliest
// day closed for an agent being read, or the beginning when one of them
// has no history yet.
func (e *Engine) SeedSince(now time.Time) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	srcs := e.activeSources()
	if len(srcs) == 0 {
		return time.Time{}
	}
	earliest := ""
	for _, src := range srcs {
		c := e.closed[src]
		if c == "" {
			return time.Time{}
		}
		if earliest == "" || c < earliest {
			earliest = c
		}
	}
	d, err := time.ParseInLocation("2006-01-02", earliest, e.loc)
	if err != nil {
		return time.Time{}
	}
	return d.AddDate(0, 0, 1)
}

// Closed is the earliest day the history file holds for the agents being
// read, "" when one has none.
func (e *Engine) Closed() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := ""
	for _, src := range e.activeSources() {
		c := e.closed[src]
		if c == "" {
			return ""
		}
		if out == "" || c < out {
			out = c
		}
	}
	return out
}

// Forget drops what the history holds of src, every day, and reopens its
// days, so the next seed reads all of its logs again: its log folders
// changed, and old days of a new folder would otherwise be skipped.
func (e *Engine) Forget(src usage.Source) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.closed, src)
	for _, d := range e.days {
		if a := d.bySource[src]; a != nil {
			d.total.sub(a)
			delete(d.bySource, src)
		}
		for k := range d.byModel {
			if k.Source == src {
				delete(d.byModel, k)
			}
		}
		for k := range d.byProject {
			if k.Source == src {
				delete(d.byProject, k)
			}
		}
		for k := range d.byEffort {
			if k.Source == src {
				delete(d.byEffort, k)
			}
		}
		for k := range d.dists {
			if k.Source == src {
				delete(d.dists, k)
			}
		}
		delete(d.hours, src)
	}
	for k, en := range e.known {
		if en.r.Source == src {
			delete(e.known, k)
		}
	}
	for k, en := range e.recent {
		if en.r.Source == src {
			delete(e.recent, k)
		}
	}
}
