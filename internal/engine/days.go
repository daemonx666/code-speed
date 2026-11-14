package engine

import (
	"encoding/json"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Agg sums responses.
type Agg struct {
	Requests int64 `json:"requests"`
	usage.Tokens
	CostMicro int64 `json:"costMicro"` // priced responses only
	Unpriced  int64 `json:"unpriced"`  // responses whose model has no price
	// output tokens and milliseconds of the measured responses: the
	// average speed is one over the other
	MeasuredOut int64 `json:"measuredOut,omitempty"`
	MeasuredMs  int64 `json:"measuredMs,omitempty"`
}

// AvgSpeed is output tok/s over the measured responses, 0 when none.
func (a *Agg) AvgSpeed() float64 {
	if a.MeasuredMs == 0 {
		return 0
	}
	return float64(a.MeasuredOut) / (float64(a.MeasuredMs) / 1000)
}

func (a *Agg) apply(e *entry, sign int64) {
	a.Requests += sign
	if sign > 0 {
		a.Tokens = a.Tokens.Add(e.r.Tokens)
	} else {
		a.Tokens = a.Tokens.Sub(e.r.Tokens)
	}
	if e.cost.Known {
		a.CostMicro += sign * e.cost.Micro
	} else {
		a.Unpriced += sign
	}
	if e.r.Measured() {
		s, en, _ := e.r.Interval()
		a.MeasuredOut += sign * e.r.Output
		a.MeasuredMs += sign * (en - s)
	}
}

// sub takes o out of a.
func (a *Agg) sub(o *Agg) {
	a.Requests -= o.Requests
	a.Tokens = a.Tokens.Sub(o.Tokens)
	a.CostMicro -= o.CostMicro
	a.Unpriced -= o.Unpriced
	a.MeasuredOut -= o.MeasuredOut
	a.MeasuredMs -= o.MeasuredMs
}

// add sums o into a.
func (a *Agg) add(o *Agg) {
	a.Requests += o.Requests
	a.Tokens = a.Tokens.Add(o.Tokens)
	a.CostMicro += o.CostMicro
	a.Unpriced += o.Unpriced
	a.MeasuredOut += o.MeasuredOut
	a.MeasuredMs += o.MeasuredMs
}

// Hour is one hour of a day for one agent.
type Hour struct {
	Requests  int64 `json:"r,omitempty"`
	Tokens    int64 `json:"t,omitempty"`
	Output    int64 `json:"o,omitempty"`
	CostMicro int64 `json:"c,omitempty"`
}

// modelKey and projectKey split a model or project by the agent that used
// it: the same model in Claude Code and in Codex is two rows.
type modelKey struct {
	Source usage.Source
	Model  string
}

// effortKey is a reasoning effort as one agent asked for it.
type effortKey struct {
	Source usage.Source
	Effort string
}

type projectKey struct {
	Source  usage.Source
	Project string
}

// day is one local day's usage in every dimension.
type day struct {
	date      string
	total     Agg
	bySource  map[usage.Source]*Agg
	byModel   map[modelKey]*Agg
	byProject map[projectKey]*Agg
	byEffort  map[effortKey]*Agg
	hours     map[usage.Source]*[24]Hour
	dists     map[modelKey]*Dist // speed and first-content spread
	peak      float64            // highest live rate seen while running
}

func newDay(date string) *day {
	return &day{date: date, bySource: map[usage.Source]*Agg{}, byModel: map[modelKey]*Agg{},
		byProject: map[projectKey]*Agg{}, byEffort: map[effortKey]*Agg{}, hours: map[usage.Source]*[24]Hour{},
		dists: map[modelKey]*Dist{}}
}

func get[K comparable](m map[K]*Agg, k K) *Agg {
	a := m[k]
	if a == nil {
		a = &Agg{}
		m[k] = a
	}
	return a
}

func (d *day) apply(e *entry, sign int64, loc *time.Location) {
	d.total.apply(e, sign)
	get(d.bySource, e.r.Source).apply(e, sign)
	model := e.r.Model
	if model == "" {
		model = "unknown"
	}
	mk := modelKey{e.r.Source, model}
	get(d.byModel, mk).apply(e, sign)
	dist := d.dists[mk]
	if dist == nil {
		dist = &Dist{}
	}
	if dist.apply(e.r, sign) {
		d.dists[mk] = dist
	}
	if len(dist.Speed) == 0 && len(dist.TTFT) == 0 {
		delete(d.dists, mk)
	}
	proj := e.r.Project
	if proj == "" {
		proj = "—"
	}
	get(d.byProject, projectKey{e.r.Source, proj}).apply(e, sign)
	effort := e.r.Effort
	if effort == "" {
		effort = "—"
	}
	get(d.byEffort, effortKey{e.r.Source, effort}).apply(e, sign)
	hs := d.hours[e.r.Source]
	if hs == nil {
		hs = &[24]Hour{}
		d.hours[e.r.Source] = hs
	}
	h := &hs[time.UnixMilli(e.r.EndMs).In(loc).Hour()]
	h.Requests += sign
	h.Tokens += sign * e.r.Total()
	h.Output += sign * e.r.Output
	if e.cost.Known {
		h.CostMicro += sign * e.cost.Micro
	}
}

// the history file's form of a day: maps keyed by strings, lists for the
// two-part keys
type dayJSON struct {
	Date     string                     `json:"date"`
	Total    Agg                        `json:"total"`
	Sources  map[usage.Source]*Agg      `json:"sources"`
	Models   []keyedAgg                 `json:"models"`
	Projects []keyedAgg                 `json:"projects"`
	Efforts  []keyedAgg                 `json:"efforts,omitempty"`
	Hours    map[usage.Source]*[24]Hour `json:"hours,omitempty"`
	Dists    []keyedDist                `json:"dists,omitempty"`
	Peak     float64                    `json:"peak,omitempty"`
}

type keyedDist struct {
	Source usage.Source `json:"source"`
	Name   string       `json:"name"`
	Dist
}

type keyedAgg struct {
	Source usage.Source `json:"source"`
	Name   string       `json:"name"`
	Agg
}

func (d *day) MarshalJSON() ([]byte, error) {
	j := dayJSON{Date: d.date, Total: d.total, Sources: d.bySource, Hours: d.hours, Peak: d.peak}
	for k, a := range d.byModel {
		j.Models = append(j.Models, keyedAgg{k.Source, k.Model, *a})
	}
	for k, a := range d.byProject {
		j.Projects = append(j.Projects, keyedAgg{k.Source, k.Project, *a})
	}
	for k, a := range d.byEffort {
		j.Efforts = append(j.Efforts, keyedAgg{k.Source, k.Effort, *a})
	}
	for k, x := range d.dists {
		j.Dists = append(j.Dists, keyedDist{k.Source, k.Model, *x})
	}
	return json.Marshal(j)
}

func (d *day) UnmarshalJSON(b []byte) error {
	var j dayJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*d = *newDay(j.Date)
	d.total, d.peak = j.Total, j.Peak
	for k, a := range j.Sources {
		if a != nil {
			d.bySource[k] = a
		}
	}
	for _, m := range j.Models {
		a := m.Agg
		d.byModel[modelKey{m.Source, m.Name}] = &a
	}
	for _, p := range j.Projects {
		a := p.Agg
		d.byProject[projectKey{p.Source, p.Name}] = &a
	}
	for _, x := range j.Efforts {
		a := x.Agg
		d.byEffort[effortKey{x.Source, x.Name}] = &a
	}
	for _, x := range j.Dists {
		dist := x.Dist
		d.dists[modelKey{x.Source, x.Name}] = &dist
	}
	for k, h := range j.Hours {
		if h != nil {
			d.hours[k] = h
		}
	}
	return nil
}
