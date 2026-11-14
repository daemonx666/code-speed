package engine

import (
	"sort"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Row is one line of a usage breakdown.
type Row struct {
	Name     string       `json:"name"`
	Label    string       `json:"label"`
	Source   usage.Source `json:"source"` // the agent the row belongs to
	AvgSpeed float64      `json:"avgSpeed"`
	Agg
}

// DayTotal is one day of the history strip.
type DayTotal struct {
	Date      string                         `json:"date"`
	Requests  int64                          `json:"requests"`
	Tokens    int64                          `json:"tokens"`
	Output    int64                          `json:"output"`
	CostMicro int64                          `json:"costMicro"`
	Unpriced  int64                          `json:"unpriced"`
	Sources   map[usage.Source]DaySourceCost `json:"sources,omitempty"`
}

// DaySourceCost is one agent's share of a day.
type DaySourceCost struct {
	Tokens    int64 `json:"tokens"`
	CostMicro int64 `json:"costMicro"`
}

// Last is the newest measured response.
type Last struct {
	Source  usage.Source `json:"source"`
	Model   string       `json:"model"`
	Project string       `json:"project,omitempty"`
	Output  int64        `json:"output"`
	Speed   float64      `json:"speed"`
	TTFTMs  int64        `json:"ttftMs,omitempty"` // to the first content, where the log says
	EndMs   int64        `json:"endMs"`
}

// Agent is an agent active in the last ten minutes.
type Agent struct {
	ID       string       `json:"id"` // the session's id, for /api/session
	Source   usage.Source `json:"source"`
	Label    string       `json:"label"`
	Project  string       `json:"project,omitempty"`
	Model    string       `json:"model,omitempty"`
	Phase    usage.Phase  `json:"phase"`
	Subagent bool         `json:"subagent,omitempty"`
	Speed    float64      `json:"speed"` // recent output tok/s, 0 when none
	Live     float64      `json:"live"`
	LastMs   int64        `json:"lastMs"`
	// Context is the session's context now and ContextWindow its model's
	// limit (0 when not known)
	Context       int64 `json:"context,omitempty"`
	ContextWindow int64 `json:"contextWindow,omitempty"`
}

// Snapshot is everything code-speed shows, at one instant.
type Snapshot struct {
	NowMs         int64                    `json:"nowMs"`
	Live          float64                  `json:"live"`          // output tok/s, last 3 s
	LiveEstimated float64                  `json:"liveEstimated"` // share of Live that is estimated, 0…1
	LiveBySource  map[usage.Source]float64 `json:"liveBySource"`
	// LiveCostPerHour is the API-price cost of the live output, in
	// micro-USD per hour
	LiveCostPerHour int64 `json:"liveCostPerHour"`
	// Last30BySource is each agent's API-price cost over the last 30 days
	// (today included), in micro-USD: what a subscription is set against
	Last30BySource map[usage.Source]int64 `json:"last30BySource"`
	// MonthCostMicro is this calendar month's cost so far, for a budget
	MonthCostMicro int64 `json:"monthCostMicro"`
	Last           *Last `json:"last,omitempty"`
	Today          struct {
		Date     string  `json:"date"`
		AvgSpeed float64 `json:"avgSpeed"`
		Peak     float64 `json:"peak"`
		Agg
		BySource []Row `json:"bySource"`
		ByModel  []Row `json:"byModel"`
	} `json:"today"`
	Week   []DayTotal    `json:"week"` // oldest first, today last
	Agents []Agent       `json:"agents"`
	Quotas []usage.Quota `json:"quotas"`
	// Pending counts the replies of the last half hour whose usage the log
	// hasn't settled yet (still streaming): their numbers can still change
	Pending       int    `json:"pending,omitempty"`
	PricesVersion int    `json:"pricesVersion"`
	PricesUpdated string `json:"pricesUpdated"`
}

func rows[K comparable](m map[K]*Agg, name func(K) (string, string, usage.Source)) []Row {
	var out []Row
	for k, a := range m {
		if a.Requests <= 0 {
			continue
		}
		n, l, src := name(k)
		out = append(out, Row{Name: n, Label: l, Source: src, AvgSpeed: a.AvgSpeed(), Agg: *a})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostMicro != out[j].CostMicro {
			return out[i].CostMicro > out[j].CostMicro
		}
		return out[i].Total() > out[j].Total()
	})
	return out
}

// Snapshot computes the view at nowMs.
func (e *Engine) Snapshot(nowMs int64) Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prune(nowMs)
	var s Snapshot
	s.NowMs = nowMs
	s.PricesVersion, s.PricesUpdated = e.prices.Version, e.prices.Updated

	rs := e.measuredNewestFirst()
	all := e.liveRate(nowMs, rs, func(usage.Source) bool { return true })
	s.Live, s.LiveEstimated = all.rate, all.estShare
	s.LiveCostPerHour = int64(all.costPerSec * 3600)
	s.LiveBySource = map[usage.Source]float64{}
	seen := map[usage.Source]bool{}
	for _, en := range e.recent {
		seen[en.r.Source] = true
		if !en.r.Final {
			s.Pending++
		}
	}
	for _, a := range e.agents {
		seen[a.Source] = true
	}
	for src := range seen {
		if l := e.liveRate(nowMs, rs, func(x usage.Source) bool { return x == src }); l.rate > 0 {
			s.LiveBySource[src] = l.rate
		}
	}

	today := e.dayFor(e.dayOf(nowMs))
	today.peak = max(today.peak, s.Live)
	s.Today.Date = today.date
	s.Today.Agg = today.total
	s.Today.AvgSpeed = today.total.AvgSpeed()
	s.Today.Peak = today.peak
	s.Today.BySource = rows(today.bySource, func(k usage.Source) (string, string, usage.Source) { return string(k), k.Label(), k })
	s.Today.ByModel = rows(today.byModel, func(k modelKey) (string, string, usage.Source) { return k.Model, k.Model, k.Source })

	t := time.UnixMilli(nowMs).In(e.loc)
	for i := HistoryDays - 1; i >= 0; i-- {
		date := t.AddDate(0, 0, -i).Format("2006-01-02")
		dt := DayTotal{Date: date}
		if d := e.days[date]; d != nil {
			dt.Requests, dt.Tokens, dt.Output = d.total.Requests, d.total.Total(), d.total.Output
			dt.CostMicro, dt.Unpriced = d.total.CostMicro, d.total.Unpriced
			dt.Sources = map[usage.Source]DaySourceCost{}
			for src, a := range d.bySource {
				if a.Requests > 0 {
					dt.Sources[src] = DaySourceCost{Tokens: a.Total(), CostMicro: a.CostMicro}
				}
			}
		}
		s.Week = append(s.Week, dt)
	}

	if e.last != nil {
		r := e.last.r
		sp, _ := r.Speed()
		l := &Last{Source: r.Source, Model: r.Model, Project: r.Project, Output: r.Output, Speed: sp, EndMs: r.EndMs}
		if r.FirstMs > 0 && r.StartMs > 0 && r.FirstMs >= r.StartMs {
			l.TTFTMs = r.FirstMs - r.StartMs
		}
		s.Last = l
	}

	for _, a := range e.agents {
		if a.LastLineMs < nowMs-agentListMs {
			continue
		}
		ag := Agent{ID: a.Agent, Source: a.Source, Label: a.Source.Label(), Project: a.Project, Model: a.Model,
			Phase: a.Phase, Subagent: a.Subagent, LastMs: a.LastLineMs,
			Context: a.Context, ContextWindow: a.ContextWindow}
		ag.Speed, _ = speedOf(rs, 3, nowMs-recentMs, func(en *entry) bool { return en.r.Agent == a.Agent })
		if a.Phase == usage.Generating && a.InFlightMs != 0 && nowMs-a.LastLineMs <= inFlightIdleMs {
			ag.Live, _ = estimate(rs, a, nowMs)
		}
		s.Agents = append(s.Agents, ag)
	}
	sort.Slice(s.Agents, func(i, j int) bool { return s.Agents[i].LastMs > s.Agents[j].LastMs })

	for _, q := range e.quotas {
		q.Windows = append([]usage.QuotaWindow(nil), q.Windows...)
		for i := range q.Windows {
			q.Windows[i].Pace = PaceOf(q.Windows[i], nowMs)
		}
		s.Quotas = append(s.Quotas, q)
	}

	month := t.Format("2006-01")
	for date, d := range e.days {
		if len(date) >= 7 && date[:7] == month && date <= today.date {
			s.MonthCostMicro += d.total.CostMicro
		}
	}

	s.Last30BySource = map[usage.Source]int64{}
	from := t.AddDate(0, 0, -29).Format("2006-01-02")
	for date, d := range e.days {
		if date < from || date > today.date {
			continue
		}
		for src, a := range d.bySource {
			s.Last30BySource[src] += a.CostMicro
		}
	}
	sort.Slice(s.Quotas, func(i, j int) bool { return s.Quotas[i].Source < s.Quotas[j].Source })
	return s
}
