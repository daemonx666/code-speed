package engine

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/daemonx666/code-speed/internal/usage"
)

// ExportRow is one day of one model of one agent.
type ExportRow struct {
	Date        string       `json:"date"`
	Source      usage.Source `json:"source"`
	Agent       string       `json:"agent"`
	Model       string       `json:"model"`
	Requests    int64        `json:"requests"`
	Input       int64        `json:"input"` // all input, cache included
	CachedInput int64        `json:"cachedInput"`
	CacheWrite  int64        `json:"cacheWrite"`
	Output      int64        `json:"output"` // reasoning included
	Reasoning   int64        `json:"reasoning"`
	CostUSD     float64      `json:"costUSD"` // API price; priced replies only
	Unpriced    int64        `json:"unpriced"`
}

// Export lists a period's usage by day, agent and model.
func (e *Engine) Export(nowMs int64, period string) []ExportRow {
	v := e.Period(nowMs, period) // for the period's first day
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []ExportRow
	for date, d := range e.days {
		if date < v.From || date > v.To {
			continue
		}
		for k, a := range d.byModel {
			if a.Requests == 0 {
				continue
			}
			out = append(out, ExportRow{Date: date, Source: k.Source, Agent: k.Source.Label(), Model: k.Model,
				Requests: a.Requests, Input: a.Input, CachedInput: a.CachedInput, CacheWrite: a.CacheWrite,
				Output: a.Output, Reasoning: a.Reasoning, CostUSD: float64(a.CostMicro) / 1e6, Unpriced: a.Unpriced})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// WriteCSV writes rows as CSV with a header.
func WriteCSV(w io.Writer, rows []ExportRow) error {
	c := csv.NewWriter(w)
	c.Write([]string{"date", "source", "agent", "model", "requests", "input", "cached_input", "cache_write", "output", "reasoning", "cost_usd", "unpriced"})
	i := func(n int64) string { return strconv.FormatInt(n, 10) }
	for _, r := range rows {
		c.Write([]string{r.Date, string(r.Source), r.Agent, r.Model, i(r.Requests), i(r.Input), i(r.CachedInput), i(r.CacheWrite),
			i(r.Output), i(r.Reasoning), fmt.Sprintf("%.6f", r.CostUSD), i(r.Unpriced)})
	}
	c.Flush()
	return c.Error()
}

// WriteJSON writes rows as a JSON array.
func WriteJSON(w io.Writer, rows []ExportRow) error {
	if rows == nil {
		rows = []ExportRow{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rows)
}
