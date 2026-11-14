// Package engine turns parsed responses into what code-speed shows: the
// live output rate, recent speeds, per-day usage and cost, and quotas.
//
// The live rate follows Agent Speed's specification (spec/METRICS.md §6,
// MIT, github.com/zoefix/agent-speed). Every function takes the time
// explicitly, so it is deterministic under test.
package engine

import (
	"sort"
	"sync"
	"time"

	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/usage"
)

const (
	LiveWindowMs   = 3_000
	recentMs       = 10 * 60_000
	fallbackMs     = 30 * 60_000
	retentionMs    = fallbackMs
	inFlightIdleMs = 300_000
	agentListMs    = 10 * 60_000
	// HistoryDays is the span of the week view, today included.
	HistoryDays = 7
	knownMax    = 65_536
)

type entry struct {
	r    usage.Response
	cost pricing.Cost
}

// Engine holds the state. It is safe for concurrent use.
type Engine struct {
	mu     sync.Mutex
	prices *pricing.Table
	loc    *time.Location

	known  map[string]*entry // every response still upsertable
	order  []string
	next   int
	recent map[string]*entry // ended within the retention window
	days   map[string]*day
	// closed is, per agent, the last day kept in the history file:
	// responses that end on or before it are already counted there
	closed map[usage.Source]string
	// active lists the agents being read; nil: every agent seen
	active []usage.Source
	agents map[string]usage.AgentState
	quotas map[usage.Source]usage.Quota
	last   *entry // the newest measured response
}

// New returns an engine pricing with table and counting days in loc.
func New(table *pricing.Table, loc *time.Location) *Engine {
	return &Engine{prices: table, loc: loc, known: map[string]*entry{}, recent: map[string]*entry{},
		closed: map[usage.Source]string{},
		days:   map[string]*day{}, agents: map[string]usage.AgentState{}, quotas: map[usage.Source]usage.Quota{}}
}

func (e *Engine) dayOf(ms int64) string { return time.UnixMilli(ms).In(e.loc).Format("2006-01-02") }

func (e *Engine) dayFor(date string) *day {
	d := e.days[date]
	if d == nil {
		d = newDay(date)
		e.days[date] = d
	}
	return d
}

// Response adds a response or replaces the one with its key.
func (e *Engine) Response(r usage.Response) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c := e.closed[r.Source]; c != "" && e.dayOf(r.EndMs) <= c {
		return // counted in the history file already
	}
	if old, ok := e.known[r.Key]; ok {
		if d := e.days[e.dayOf(old.r.EndMs)]; d != nil {
			d.apply(old, -1, e.loc)
		}
	} else {
		if len(e.order) < knownMax {
			e.order = append(e.order, r.Key)
		} else {
			delete(e.known, e.order[e.next])
			e.order[e.next] = r.Key
			e.next = (e.next + 1) % knownMax
		}
	}
	en := &entry{r: r, cost: e.prices.ResponseCost(r)}
	e.known[r.Key] = en
	e.dayFor(e.dayOf(r.EndMs)).apply(en, 1, e.loc)
	e.recent[r.Key] = en
	if r.Measured() && (e.last == nil || r.EndMs >= e.last.r.EndMs) {
		e.last = en
	}
}

// Quota keeps the newest snapshot per source.
func (e *Engine) Quota(q usage.Quota) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if old, ok := e.quotas[q.Source]; !ok || q.ObservedMs >= old.ObservedMs {
		e.quotas[q.Source] = q
	}
}

// Agent records a log file's latest state.
func (e *Engine) Agent(s usage.AgentState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.agents[s.Agent] = s
}

// prune drops what fell out of the windows. Called with mu held.
func (e *Engine) prune(nowMs int64) {
	for k, en := range e.recent {
		if en.r.EndMs < nowMs-retentionMs {
			delete(e.recent, k)
		}
	}
	for k, a := range e.agents {
		if a.LastLineMs < nowMs-24*3600_000 {
			delete(e.agents, k)
		}
	}
}

// refSet lists up to n responses, newest first, that ended after since and
// pass keep.
func refSet(rs []*entry, n int, since int64, keep func(*entry) bool) []*entry {
	var out []*entry
	for _, en := range rs {
		if len(out) == n || en.r.EndMs < since {
			break
		}
		if keep(en) {
			out = append(out, en)
		}
	}
	return out
}

// rates is a reference set's output tok/s and, over its priced responses,
// micro-USD per second.
type rates struct {
	speed, cost       float64
	hasSpeed, hasCost bool
}

func ratesOf(set []*entry) rates {
	var out, dur, cost, costDur int64
	for _, en := range set {
		s, end, _ := en.r.Interval()
		out += en.r.Output
		dur += end - s
		if en.cost.Known {
			cost += en.cost.Micro
			costDur += end - s
		}
	}
	var r rates
	if dur > 0 {
		r.speed, r.hasSpeed = float64(out)/(float64(dur)/1000), true
	}
	if costDur > 0 {
		r.cost, r.hasCost = float64(cost)/(float64(costDur)/1000), true
	}
	return r
}

// speedOf is Σ output / Σ duration of up to n responses, newest first,
// that ended after since and pass keep.
func speedOf(rs []*entry, n int, since int64, keep func(*entry) bool) (float64, bool) {
	r := ratesOf(refSet(rs, n, since, keep))
	return r.speed, r.hasSpeed
}

// measuredNewestFirst lists the retained measured responses.
func (e *Engine) measuredNewestFirst() []*entry {
	var rs []*entry
	for _, en := range e.recent {
		if en.r.Measured() {
			rs = append(rs, en)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].r.EndMs > rs[j].r.EndMs })
	return rs
}

// reference is the set an agent's unmeasured time is estimated from: its
// own recent responses, else the model's, else the source's (METRICS 6.2).
func reference(rs []*entry, a usage.AgentState, nowMs int64) rates {
	if set := refSet(rs, 3, nowMs-recentMs, func(en *entry) bool { return en.r.Agent == a.Agent }); len(set) > 0 {
		return ratesOf(set)
	}
	if a.Model != "" {
		if set := refSet(rs, 10, nowMs-fallbackMs, func(en *entry) bool { return en.r.Model == a.Model }); len(set) > 0 {
			return ratesOf(set)
		}
	}
	return ratesOf(refSet(rs, 10, nowMs-fallbackMs, func(en *entry) bool { return en.r.Source == a.Source }))
}

// estimate is the speed used for an agent's unmeasured time.
func estimate(rs []*entry, a usage.AgentState, nowMs int64) (float64, bool) {
	r := reference(rs, a, nowMs)
	return r.speed, r.hasSpeed
}

// live is the last LiveWindowMs: output tok/s, the share of it that is
// estimated, and the cost in micro-USD per second (METRICS 6.3, 9.5).
type live struct {
	rate, estShare, costPerSec float64
}

func (e *Engine) liveRate(nowMs int64, rs []*entry, keep func(usage.Source) bool) live {
	w0, w1 := nowMs-LiveWindowMs, nowMs
	var measured, estimated, cost float64
	latestEnd := map[string]int64{}
	for _, en := range e.recent {
		if en.r.EndMs > latestEnd[en.r.Agent] {
			latestEnd[en.r.Agent] = en.r.EndMs
		}
		if !keep(en.r.Source) {
			continue
		}
		s, end, ok := en.r.Interval()
		if !ok {
			continue
		}
		ov := usage.Overlap(w0, w1, s, end)
		if ov == 0 {
			continue
		}
		if en.r.Final {
			measured += float64(en.r.Output) * float64(ov) / float64(end-s)
			if en.cost.Known {
				cost += float64(en.cost.Micro) * float64(ov) / float64(end-s)
			}
		} else {
			r := reference(rs, e.agents[en.r.Agent], nowMs)
			if r.hasSpeed {
				estimated += r.speed * float64(ov) / 1000
			}
			if r.hasCost {
				cost += r.cost * float64(ov) / 1000
			}
		}
	}
	for _, a := range e.agents {
		if !keep(a.Source) || a.Phase != usage.Generating || a.InFlightMs == 0 || nowMs-a.LastLineMs > inFlightIdleMs {
			continue
		}
		r := reference(rs, a, nowMs)
		secs := float64(usage.Overlap(w0, w1, max(a.InFlightMs, latestEnd[a.Agent]), nowMs)) / 1000
		if r.hasSpeed {
			estimated += r.speed * secs
		}
		if r.hasCost {
			cost += r.cost * secs
		}
	}
	var l live
	total := measured + estimated
	if total > 0 {
		l.estShare = estimated / total
	}
	l.rate = total / (LiveWindowMs / 1000)
	l.costPerSec = cost / (LiveWindowMs / 1000)
	return l
}
