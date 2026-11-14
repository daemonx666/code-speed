// Package alerts decides which desktop notifications to send: a quota
// window crossing a threshold or running out before it resets, spend
// reaching a budget, a new speed record. Each fires once per window, day
// or month; what fired is kept on disk so a restart doesn't repeat it.
package alerts

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/settings"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Alert is one notification.
type Alert struct {
	Key    string       `json:"key"`
	AtMs   int64        `json:"atMs"`
	Kind   string       `json:"kind"` // quota, pace, budget, peak
	Source usage.Source `json:"source,omitempty"`
	Title  string       `json:"title"`
	Body   string       `json:"body"`
}

// Checker remembers what fired.
type Checker struct {
	mu     sync.Mutex
	path   string
	fired  map[string]int64
	recent []Alert

	peakDay   string
	peak      float64
	lastPeak  int64
	peakReady bool
}

const keepFor = 40 * 24 * time.Hour

type state struct {
	Fired  map[string]int64 `json:"fired"`
	Recent []Alert          `json:"recent"`
}

// New reads the state kept at path ("" keeps none).
func New(path string) *Checker {
	c := &Checker{path: path, fired: map[string]int64{}}
	if path == "" {
		return c
	}
	if b, err := os.ReadFile(path); err == nil {
		var st state
		if json.Unmarshal(b, &st) == nil {
			if st.Fired != nil {
				c.fired = st.Fired
			}
			c.recent = st.Recent
		}
	}
	return c
}

func (c *Checker) save(nowMs int64) {
	for k, at := range c.fired {
		if nowMs-at > keepFor.Milliseconds() {
			delete(c.fired, k)
		}
	}
	if c.path == "" {
		return
	}
	b, err := json.Marshal(state{Fired: c.fired, Recent: c.recent})
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(c.path), 0o700)
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		os.Rename(tmp, c.path)
	}
}

// Recent lists the latest alerts, newest first.
func (c *Checker) Recent() []Alert {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Alert, len(c.recent))
	copy(out, c.recent)
	return out
}

// Check returns the alerts the snapshot calls for that haven't fired yet.
// lang is "zh-CN" or "en".
func (c *Checker) Check(s engine.Snapshot, st settings.Settings, lang string) []Alert {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := words(lang)
	now := s.NowMs
	var out []Alert
	fire := func(a Alert) {
		if _, done := c.fired[a.Key]; done {
			return
		}
		c.fired[a.Key] = now
		a.AtMs = now
		out = append(out, a)
	}
	// mark keeps a key as fired without notifying (a lower threshold
	// passed together with a higher one)
	mark := func(key string) {
		if _, done := c.fired[key]; !done {
			c.fired[key] = now
		}
	}

	for _, q := range s.Quotas {
		for _, win := range q.Windows {
			scope := fmt.Sprintf("%s:%s:%d", q.Source, win.Kind, win.ResetsAtMs)
			if win.ResetsAtMs == 0 {
				scope = fmt.Sprintf("%s:%s:%s", q.Source, win.Kind, s.Today.Date)
			}
			name := q.Source.Label() + " " + w.window(win)
			thresholds := append([]float64(nil), st.Alerts.Quota...)
			sort.Sort(sort.Reverse(sort.Float64Slice(thresholds)))
			for i, t := range thresholds {
				if win.UsedPercent >= t {
					fire(Alert{Key: fmt.Sprintf("quota:%s:%g", scope, t), Kind: "quota", Source: q.Source,
						Title: fmt.Sprintf(w.quotaTitle, name, win.UsedPercent), Body: resetText(w, now, win.ResetsAtMs)})
					for _, lower := range thresholds[i+1:] {
						mark(fmt.Sprintf("quota:%s:%g", scope, lower))
					}
					break
				}
			}
			if p := win.Pace; st.Alerts.Pace && p != nil && !p.LastsToReset && p.RunsOutInMs > 0 && win.UsedPercent < 100 {
				fire(Alert{Key: "pace:" + scope, Kind: "pace", Source: q.Source,
					Title: fmt.Sprintf(w.paceTitle, name),
					Body:  fmt.Sprintf(w.paceBody, win.UsedPercent, w.duration(p.RunsOutInMs), w.duration(win.ResetsAtMs-now))})
			}
		}
	}

	if st.Alerts.Budget {
		check := func(kind, scope string, spent int64, budget float64) {
			if budget <= 0 {
				return
			}
			b := int64(budget * 1e6)
			for _, t := range []int64{100, 80} {
				if spent*100 >= b*t {
					title := w.budgetDay
					if kind == "month" {
						title = w.budgetMonth
					}
					fire(Alert{Key: fmt.Sprintf("budget:%s:%s:%d", kind, scope, t), Kind: "budget",
						Title: fmt.Sprintf(title, t),
						Body:  fmt.Sprintf(w.budgetBody, money(spent, st.Currency), money(b, st.Currency))})
					if t == 100 {
						mark(fmt.Sprintf("budget:%s:%s:80", kind, scope))
					}
					break
				}
			}
		}
		check("day", s.Today.Date, s.Today.CostMicro, st.Budget.Daily)
		if len(s.Today.Date) >= 7 {
			check("month", s.Today.Date[:7], s.MonthCostMicro, st.Budget.Monthly)
		}
	}

	// a speed record: 10 % over the day's best so far, at least 20 tok/s,
	// at most every 30 minutes, never the day's first reading
	if s.Today.Date != c.peakDay {
		c.peakDay, c.peak, c.peakReady = s.Today.Date, 0, false
	}
	if s.Live > c.peak {
		if st.Alerts.Peak && c.peakReady && s.Live >= 20 && s.Live >= c.peak*1.1 && now-c.lastPeak >= 30*60_000 {
			c.lastPeak = now
			key := fmt.Sprintf("peak:%s:%d", s.Today.Date, now/60_000)
			fire(Alert{Key: key, Kind: "peak", Title: fmt.Sprintf(w.peakTitle, s.Live), Body: fmt.Sprintf(w.peakBody, c.peak)})
		}
		c.peak = s.Live
	}
	if s.Live > 0 {
		c.peakReady = true
	}

	if len(out) > 0 {
		c.recent = append(append([]Alert(nil), out...), c.recent...)
		if len(c.recent) > 20 {
			c.recent = c.recent[:20]
		}
		c.save(now)
	}
	return out
}

func resetText(w lang, now, resetsAt int64) string {
	if resetsAt <= 0 {
		return ""
	}
	return fmt.Sprintf(w.resets, w.duration(resetsAt-now))
}

// money formats micro-USD in the user's currency.
func money(micro int64, c settings.Currency) string { return c.Format(micro) }

type lang struct {
	quotaTitle, paceTitle, paceBody, resets string
	budgetDay, budgetMonth, budgetBody      string
	peakTitle, peakBody                     string
	fiveHour, sevenDay, month               string
	minutes, hours, days                    string
	zh                                      bool
}

func words(l string) lang {
	if strings.HasPrefix(l, "zh") {
		return lang{
			quotaTitle: "%s 额度已用 %.0f%%", paceTitle: "%s 额度按当前速度会提前用完",
			paceBody: "已用 %.0f%%，预计 %s后用完，%s后才重置", resets: "%s后重置",
			budgetDay: "今日费用已达预算的 %d%%", budgetMonth: "本月费用已达预算的 %d%%", budgetBody: "已花 %s（预算 %s，按 API 价格）",
			peakTitle: "新的速度纪录：%.0f tok/s", peakBody: "今日之前最高 %.0f tok/s",
			fiveHour: "5 小时", sevenDay: "每周", month: "每月", minutes: "%d 分钟", hours: "%d 小时 %d 分", days: "%d 天 %d 小时", zh: true,
		}
	}
	return lang{
		quotaTitle: "%s quota at %.0f%%", paceTitle: "%s quota will run out before it resets",
		paceBody: "%.0f%% used; at this pace it runs out in %s, and resets in %s", resets: "Resets in %s",
		budgetDay: "Today's spend reached %d%% of the budget", budgetMonth: "This month's spend reached %d%% of the budget", budgetBody: "%s spent of %s (API price)",
		peakTitle: "New speed record: %.0f tok/s", peakBody: "Today's best before was %.0f tok/s",
		fiveHour: "5-hour", sevenDay: "weekly", month: "monthly", minutes: "%d min", hours: "%dh %dm", days: "%dd %dh",
	}
}

func (w lang) window(q usage.QuotaWindow) string {
	switch q.Kind {
	case "fiveHour":
		return w.fiveHour
	case "sevenDay":
		return w.sevenDay
	case "month", "monthly":
		return w.month
	}
	return ""
}

func (w lang) duration(ms int64) string {
	m := max(ms/60_000, 0)
	switch {
	case m < 60:
		return fmt.Sprintf(w.minutes, m)
	case m < 24*60:
		return fmt.Sprintf(w.hours, m/60, m%60)
	}
	return fmt.Sprintf(w.days, m/(24*60), (m%(24*60))/60)
}
