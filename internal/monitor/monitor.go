// Package monitor keeps the engine fed: it reads the history once at
// start, then follows the logs as the agents write them.
//
// It polls instead of using file-system events, the same on every OS: a
// stat of the few files changed in the last minutes each second, and a
// walk for new files every ten seconds.
package monitor

import (
	"context"
	"log"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

const (
	hotPoll     = time.Second
	discover    = 10 * time.Second
	hotFor      = 15 * time.Minute
	forgetAfter = (engine.HistoryDays + 1) * 24 * time.Hour
	saveEvery   = 10 * time.Minute
	// seedWorkers read the history in parallel; more buy little speed
	// for a much higher peak of memory
	seedWorkers = 4
)

// Monitor follows every log under its roots and runs the pollers.
type Monitor struct {
	Engine  *engine.Engine
	roots   []source.Root
	pollers []*polled
	dedup   *source.Dedup

	mu    sync.Mutex
	tails map[string]*source.Tail

	// HistoryPath is where finished days are kept; "" keeps none
	HistoryPath string
	seedTotal   atomic.Int64
	seedDone    atomic.Int64
}

// SeedProgress is how many of the files the seed found it has read.
func (m *Monitor) SeedProgress() (done, total int64) {
	return m.seedDone.Load(), m.seedTotal.Load()
}

// SaveHistory writes the finished days, when there is a history file.
func (m *Monitor) SaveHistory(now time.Time) {
	if m.HistoryPath == "" {
		return
	}
	if err := m.Engine.SaveHistory(m.HistoryPath, now.UnixMilli()); err != nil {
		log.Printf("history: %v", err)
	}
}

// LoadHistory reads the history file, before Seed.
func (m *Monitor) LoadHistory(path string) {
	m.HistoryPath = path
	if err := m.Engine.LoadHistory(path); err != nil {
		log.Printf("history: %v", err)
	}
}

// polled is one source read by a Poller, every so often.
type polled struct {
	src     usage.Source
	p       source.Poller
	every   time.Duration
	next    time.Time
	running atomic.Bool
}

// New returns a monitor for every registered agent that enabled lets
// through (nil: the local ones).
func New(e *engine.Engine, enabled source.Enabled) *Monitor {
	if enabled == nil {
		enabled = source.DefaultEnabled
	}
	pollers := map[usage.Source]source.Poller{}
	every := map[usage.Source]time.Duration{}
	var active []usage.Source
	for _, s := range source.Specs() {
		if enabled(s.Source) {
			active = append(active, s.Source)
		}
		if s.Poller != nil && enabled(s.Source) {
			pollers[s.Source] = s.Poller()
			every[s.Source] = s.Every
		}
	}
	e.SetSources(active)
	m := NewWith(e, source.Roots(enabled), pollers)
	for _, p := range m.pollers {
		if d := every[p.src]; d > 0 {
			p.every = d
		}
	}
	return m
}

// NewWith returns a monitor for the given roots and pollers.
func NewWith(e *engine.Engine, roots []source.Root, pollers map[usage.Source]source.Poller) *Monitor {
	m := &Monitor{Engine: e, roots: roots, dedup: source.NewDedup(), tails: map[string]*source.Tail{}}
	for src, p := range pollers {
		m.pollers = append(m.pollers, &polled{src: src, p: p, every: 5 * time.Second})
	}
	return m
}

// historyStart is local midnight of the first day of the week view, the
// least that is read even with a history file.
func historyStart(now time.Time) time.Time {
	y, m, d := now.AddDate(0, 0, -(engine.HistoryDays - 1)).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
}

// Seed reads every log changed within the kept days. Files are read in
// parallel, oldest first: a response copied into another file (a resumed
// or forked session) still counts once, through the shared dedup set,
// though which of the two files it is credited to may vary.
func (m *Monitor) Seed(now time.Time) int {
	since := m.Engine.SeedSince(now)
	found := source.Discover(m.roots, since)
	m.seedTotal.Store(int64(len(found)))
	m.seedDone.Store(0)
	sort.Slice(found, func(i, j int) bool { return found[i].ModTime.Before(found[j].ModTime) })
	m.mu.Lock()
	defer m.mu.Unlock()
	work := make(chan *source.Tail)
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), seedWorkers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				if t.Read(m.dedup, m.Engine) == nil {
					m.Engine.Agent(t.Parser.State())
				}
				m.seedDone.Add(1)
			}
		}()
	}
	for _, f := range found {
		if _, ok := m.tails[f.Path]; ok {
			continue
		}
		t := source.NewTail(f, m.dedup, m.Engine)
		m.tails[f.Path] = t
		work <- t
	}
	close(work)
	wg.Wait()
	for _, p := range m.pollers {
		m.poll(context.Background(), p, now, since)
	}
	m.SaveHistory(now)
	// the history's lines are garbage now: give the memory back at once
	// rather than when the scavenger gets to it
	debug.FreeOSMemory()
	return len(found)
}

// follow starts or continues reading one file. Called with mu held.
func (m *Monitor) follow(f source.Found) {
	t, ok := m.tails[f.Path]
	if !ok {
		t = source.NewTail(f, m.dedup, m.Engine)
		m.tails[f.Path] = t
	} else if t.ModTime.Equal(f.ModTime) && t.Size == f.Size {
		return
	}
	if t.Read(m.dedup, m.Engine) == nil {
		m.Engine.Agent(t.Parser.State())
	}
}

// poll runs one poller, unless its last run is still going.
func (m *Monitor) poll(ctx context.Context, p *polled, now, since time.Time) {
	if !p.running.CompareAndSwap(false, true) {
		return
	}
	defer p.running.Store(false)
	p.next = now.Add(p.every)
	if err := p.p.Poll(ctx, since, now, m.dedup, m.Engine); err != nil {
		log.Printf("%s: %v", p.src, err)
	}
}

// Poll reads the files changed recently.
func (m *Monitor) Poll(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tails {
		if now.Sub(t.ModTime) > hotFor {
			continue
		}
		if t.Read(m.dedup, m.Engine) == nil {
			m.Engine.Agent(t.Parser.State())
		}
	}
}

// Rediscover finds new and newly changed files, and forgets the ones no
// longer in the kept days.
func (m *Monitor) Rediscover(now time.Time) {
	found := source.Discover(m.roots, now.Add(-48*time.Hour))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range found {
		m.follow(f)
	}
	for p, t := range m.tails {
		if now.Sub(t.ModTime) > forgetAfter {
			delete(m.tails, p)
		}
	}
}

// Run follows the logs until ctx ends. Seed first.
func (m *Monitor) Run(ctx context.Context) {
	hot := time.NewTicker(hotPoll)
	disc := time.NewTicker(discover)
	save := time.NewTicker(saveEvery)
	defer save.Stop()
	defer hot.Stop()
	defer disc.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-hot.C:
			m.Poll(now)
			for _, p := range m.pollers {
				if !now.Before(p.next) {
					go m.poll(ctx, p, now, m.Engine.SeedSince(now))
				}
			}
		case now := <-save.C:
			m.SaveHistory(now)
		case now := <-disc.C:
			m.Rediscover(now)
		}
	}
}

// Files is how many logs are followed.
func (m *Monitor) Files() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tails)
}
