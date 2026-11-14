package engine

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Periods the views can cover.
const (
	PeriodDay   = "day"   // today, by hour
	PeriodWeek  = "week"  // the last 7 days, by day
	PeriodMonth = "month" // the last 30 days, by day
	PeriodYear  = "year"  // the last 12 months, by month
	PeriodAll   = "all"   // everything kept, by month
)

// Point is one bar of a period's chart.
type Point struct {
	Label     string                       `json:"label"` // "14" (hour), "2026-11-14" (day) or "2026-11" (month)
	Requests  int64                        `json:"requests"`
	Tokens    int64                        `json:"tokens"`
	Output    int64                        `json:"output"`
	CostMicro int64                        `json:"costMicro"`
	Sources   map[usage.Source]SourcePoint `json:"sources,omitempty"`
	// Models splits the bar by model, keyed as PeriodView.Models names
	// them; by day and month only (hours aren't kept by model)
	Models map[string]SourcePoint `json:"models,omitempty"`
}

// SourcePoint is one agent's part of a bar.
type SourcePoint struct {
	Tokens    int64 `json:"tokens"`
	CostMicro int64 `json:"costMicro"`
}

// PeriodView is the usage of a period in every dimension.
type PeriodView struct {
	Period     string  `json:"period"`
	From       string  `json:"from"` // first day, YYYY-MM-DD
	To         string  `json:"to"`   // last day (today)
	ActiveDays int     `json:"activeDays"`
	AvgSpeed   float64 `json:"avgSpeed"`
	Agg
	BySource  []Row `json:"bySource"`
	ByModel   []Row `json:"byModel"`
	ByProject []Row `json:"byProject"`
	// ByEffort splits the period by the reasoning effort each agent's
	// requests asked for ("—" where the log doesn't say)
	ByEffort []Row `json:"byEffort"`
	// Speeds ranks the models by median output speed, with their spread
	// and time to first content (none when focused on an hour)
	Speeds  []SpeedRow   `json:"speeds"`
	Series  []Point      `json:"series"`
	Sources []SourceInfo `json:"sources"` // every agent in the period, for the legend
	// Models are the parts Point.Models splits a bar into, largest first:
	// the period's top models and, for the rest, OtherModels
	Models []ModelInfo `json:"models"`
	// HistoryFrom is the first day there is any data for, so a view can
	// say how far back "all" goes.
	HistoryFrom string `json:"historyFrom,omitempty"`

	// Focus is the bar the rows are narrowed to ("14", a date, a month),
	// "" for the whole period; Focused is that bar's totals. An hour
	// narrows the agents only: models and projects aren't kept by hour.
	Focus   string `json:"focus,omitempty"`
	Focused *Agg   `json:"focused,omitempty"`

	// Prev is the period before, as long, to compare with: yesterday up to
	// this hour, the 7, 30 days or 12 months before. nil for "all".
	Prev *Prev `json:"prev,omitempty"`
}

// Prev is the totals of the period before.
type Prev struct {
	From string `json:"from"`
	To   string `json:"to"`
	Agg
}

// dims sums days in every dimension.
type dims struct {
	total     Agg
	bySource  map[usage.Source]*Agg
	byModel   map[modelKey]*Agg
	byProject map[projectKey]*Agg
	byEffort  map[effortKey]*Agg
	dists     map[modelKey]*Dist
}

func newDims() *dims {
	return &dims{bySource: map[usage.Source]*Agg{}, byModel: map[modelKey]*Agg{}, byProject: map[projectKey]*Agg{},
		byEffort: map[effortKey]*Agg{},
		dists:    map[modelKey]*Dist{}}
}

func (m *dims) add(d *day) {
	m.total.add(&d.total)
	for k, a := range d.bySource {
		get(m.bySource, k).add(a)
	}
	for k, a := range d.byModel {
		get(m.byModel, k).add(a)
	}
	for k, a := range d.byProject {
		get(m.byProject, k).add(a)
	}
	for k, a := range d.byEffort {
		get(m.byEffort, k).add(a)
	}
	for k, x := range d.dists {
		if m.dists[k] == nil {
			m.dists[k] = &Dist{}
		}
		m.dists[k].add(x)
	}
}

// hourAgg is an hour's totals as an Agg (hours keep tokens, not their kinds).
func hourAgg(h Hour) Agg {
	return Agg{Requests: h.Requests, Tokens: usage.Tokens{Input: h.Tokens - h.Output, Output: h.Output}, CostMicro: h.CostMicro}
}

func (v *PeriodView) setRows(m *dims) {
	v.BySource = rows(m.bySource, func(k usage.Source) (string, string, usage.Source) { return string(k), k.Label(), k })
	v.ByModel = rows(m.byModel, func(k modelKey) (string, string, usage.Source) { return k.Model, k.Model, k.Source })
	v.ByProject = rows(m.byProject, func(k projectKey) (string, string, usage.Source) { return k.Project, k.Project, k.Source })
	v.ByEffort = rows(m.byEffort, func(k effortKey) (string, string, usage.Source) { return k.Effort, k.Effort, k.Source })
	v.Speeds = speedRows(m.dists, m.byModel)
	if v.Speeds == nil {
		v.Speeds = []SpeedRow{}
	}
}

// SourceInfo names an agent and its colour.
type SourceInfo struct {
	ID    usage.Source `json:"id"`
	Label string       `json:"label"`
	Color string       `json:"color"`
}

// ModelInfo names one model part of the chart.
type ModelInfo struct {
	Key    string       `json:"key"`
	Model  string       `json:"model"` // "" for OtherModels
	Source usage.Source `json:"source,omitempty"`
}

// OtherModels is the part holding every model past the top chartModels.
const OtherModels = "other"

// chartModels: more parts than this can't be told apart by colour.
const chartModels = 7

// chartModelKeys picks the period's top models (rows are largest first)
// and names the part each model's usage goes to.
func chartModelKeys(rows []Row) ([]ModelInfo, func(modelKey) string) {
	top := map[modelKey]string{}
	var info []ModelInfo
	for i, r := range rows {
		if i == chartModels && len(rows) > chartModels+1 {
			info = append(info, ModelInfo{Key: OtherModels})
			break
		}
		k := string(r.Source) + ":" + r.Name
		top[modelKey{r.Source, r.Name}] = k
		info = append(info, ModelInfo{k, r.Name, r.Source})
	}
	return info, func(m modelKey) string {
		if k, ok := top[m]; ok {
			return k
		}
		return OtherModels
	}
}

func addModels(p *Point, d *day, key func(modelKey) string) {
	for m, a := range d.byModel {
		if a.Requests <= 0 {
			continue
		}
		if p.Models == nil {
			p.Models = map[string]SourcePoint{}
		}
		k := key(m)
		mp := p.Models[k]
		mp.Tokens += a.Total()
		mp.CostMicro += a.CostMicro
		p.Models[k] = mp
	}
}

func addPoint(p *Point, src usage.Source, tokens, output, cost, req int64) {
	p.Requests += req
	p.Tokens += tokens
	p.Output += output
	p.CostMicro += cost
	if p.Sources == nil {
		p.Sources = map[usage.Source]SourcePoint{}
	}
	sp := p.Sources[src]
	sp.Tokens += tokens
	sp.CostMicro += cost
	p.Sources[src] = sp
}

// Period computes a period's view at nowMs. focus narrows the rows to one
// bar of the chart (see PeriodView.Focus); "" for none.
func (e *Engine) Period(nowMs int64, period string, focus ...string) PeriodView {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.UnixMilli(nowMs).In(e.loc)
	today := now.Format("2006-01-02")
	v := PeriodView{Period: period, To: today}

	var first string
	for date := range e.days {
		if first == "" || date < first {
			first = date
		}
	}
	v.HistoryFrom = first

	switch period {
	case PeriodWeek:
		v.From = now.AddDate(0, 0, -6).Format("2006-01-02")
	case PeriodMonth:
		v.From = now.AddDate(0, 0, -29).Format("2006-01-02")
	case PeriodYear:
		y, m, _ := now.AddDate(0, -11, 0).Date()
		v.From = time.Date(y, m, 1, 0, 0, 0, 0, e.loc).Format("2006-01-02")
	case PeriodAll:
		v.From = first
		if v.From == "" {
			v.From = today
		}
	default:
		v.Period, v.From = PeriodDay, today
	}

	all := newDims()
	var days []*day
	for date, d := range e.days {
		if date >= v.From && date <= today {
			days = append(days, d)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].date < days[j].date })
	seen := map[usage.Source]bool{}
	for _, d := range days {
		if d.total.Requests > 0 {
			v.ActiveDays++
		}
		all.add(d)
		for k, a := range d.bySource {
			if a.Requests > 0 {
				seen[k] = true
			}
		}
	}
	v.Agg = all.total
	v.AvgSpeed = v.Agg.AvgSpeed()
	v.setRows(all)
	models, modelPart := chartModelKeys(v.ByModel)
	v.Models = models
	for src := range seen {
		v.Sources = append(v.Sources, SourceInfo{src, src.Label(), src.Color()})
	}
	sort.Slice(v.Sources, func(i, j int) bool { return v.Sources[i].Label < v.Sources[j].Label })

	if len(focus) > 0 && focus[0] != "" {
		e.focus(&v, days, today, focus[0])
	}
	v.Prev = e.prev(now, v.Period)

	switch v.Period {
	case PeriodDay:
		v.Series = make([]Point, 24)
		for h := range v.Series {
			v.Series[h].Label = time.Date(2000, 1, 1, h, 0, 0, 0, time.UTC).Format("15")
		}
		if d := e.days[today]; d != nil {
			for src, hs := range d.hours {
				for h, x := range hs {
					if x.Requests != 0 || x.Tokens != 0 {
						addPoint(&v.Series[h], src, x.Tokens, x.Output, x.CostMicro, x.Requests)
					}
				}
			}
		}
	case PeriodWeek, PeriodMonth:
		start, _ := time.ParseInLocation("2006-01-02", v.From, e.loc)
		for t := start; t.Format("2006-01-02") <= today; t = t.AddDate(0, 0, 1) {
			p := Point{Label: t.Format("2006-01-02")}
			if d := e.days[p.Label]; d != nil {
				for src, a := range d.bySource {
					addPoint(&p, src, a.Total(), a.Output, a.CostMicro, a.Requests)
				}
				addModels(&p, d, modelPart)
			}
			v.Series = append(v.Series, p)
		}
	default: // by month
		start, _ := time.ParseInLocation("2006-01-02", v.From, e.loc)
		start = time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, e.loc)
		idx := map[string]int{}
		for t := start; t.Format("2006-01") <= now.Format("2006-01"); t = t.AddDate(0, 1, 0) {
			idx[t.Format("2006-01")] = len(v.Series)
			v.Series = append(v.Series, Point{Label: t.Format("2006-01")})
		}
		for _, d := range days {
			i, ok := idx[d.date[:7]]
			if !ok {
				continue
			}
			for src, a := range d.bySource {
				addPoint(&v.Series[i], src, a.Total(), a.Output, a.CostMicro, a.Requests)
			}
			addModels(&v.Series[i], d, modelPart)
		}
	}
	return v
}

// focus narrows v's rows to one bar: an hour of today, a day, or a month.
func (e *Engine) focus(v *PeriodView, days []*day, today, f string) {
	m := newDims()
	switch v.Period {
	case PeriodDay:
		h, err := strconv.Atoi(f)
		if err != nil || h < 0 || h > 23 {
			return
		}
		if d := e.days[today]; d != nil {
			for src, hs := range d.hours {
				a := hourAgg(hs[h])
				if a.Requests != 0 {
					m.total.add(&a)
					get(m.bySource, src).add(&a)
				}
			}
		}
	case PeriodWeek, PeriodMonth:
		d := e.days[f]
		if d == nil || f < v.From || f > today {
			v.Focus, v.Focused = f, &Agg{}
			v.setRows(m)
			return
		}
		m.add(d)
	default: // a month, YYYY-MM
		found := false
		for _, d := range days {
			if strings.HasPrefix(d.date, f+"-") {
				m.add(d)
				found = true
			}
		}
		if !found && len(f) != 7 {
			return
		}
	}
	v.Focus = f
	t := m.total
	v.Focused = &t
	v.setRows(m)
}

// prev is the totals of the period before v's, as long.
func (e *Engine) prev(now time.Time, period string) *Prev {
	day := func(t time.Time) string { return t.Format("2006-01-02") }
	sum := func(from, to string) *Prev {
		p := &Prev{From: from, To: to}
		for date, d := range e.days {
			if date >= from && date <= to {
				p.Agg.add(&d.total)
			}
		}
		return p
	}
	switch period {
	case PeriodDay:
		// yesterday up to this hour, so a morning isn't set against a whole day
		y := day(now.AddDate(0, 0, -1))
		p := &Prev{From: y, To: y}
		if d := e.days[y]; d != nil {
			for _, hs := range d.hours {
				for h := 0; h <= now.Hour(); h++ {
					a := hourAgg(hs[h])
					p.Agg.add(&a)
				}
			}
		}
		return p
	case PeriodWeek:
		return sum(day(now.AddDate(0, 0, -13)), day(now.AddDate(0, 0, -7)))
	case PeriodMonth:
		return sum(day(now.AddDate(0, 0, -59)), day(now.AddDate(0, 0, -30)))
	case PeriodYear:
		y, m, _ := now.AddDate(0, -23, 0).Date()
		from := time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
		y2, m2, _ := now.AddDate(0, -11, 0).Date()
		to := time.Date(y2, m2, 1, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -1)
		return sum(day(from), day(to))
	}
	return nil
}
