package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/usage"
)

// within reports whether got is within one bucket (19 %) of want.
func within(got, want float64) bool { return math.Abs(got/want-1) < 0.2 }

func TestSpeedPercentilesRankModels(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	// terra: 10, 20 … 100 tok/s over 10 s each; first content 1 s … 10 s in
	for i := 1; i <= 10; i++ {
		r := resp(fmt.Sprint("t", i), "/f", t0-20_000, t0-10_000, int64(i)*100)
		r.FirstMs = r.StartMs + int64(i)*1000
		e.Response(r)
	}
	// a faster model with only timed replies
	for i := range 6 {
		r := resp(fmt.Sprint("q", i), "/g", t0-20_000, t0-10_000, 2000)
		r.Model = "gpt-6.1-quick"
		e.Response(r)
	}
	// and one with too few to rank
	r := resp("x", "/h", t0-20_000, t0-10_000, 5000)
	r.Model = "rare"
	e.Response(r)

	v := e.Period(t0, PeriodDay)
	if len(v.Speeds) != 2 || v.Speeds[0].Model != "gpt-6.1-quick" || v.Speeds[1].Model != "gpt-6.1-terra" {
		t.Fatalf("rows %+v", v.Speeds)
	}
	q, s := v.Speeds[0], v.Speeds[1]
	if q.Timed != 6 || !within(q.P50, 200) || q.First50 != 0 {
		t.Fatalf("quick %+v", q)
	}
	if s.Timed != 10 || !within(s.P10, 10) || !within(s.P50, 50) || !within(s.P90, 90) {
		t.Fatalf("terra speed %+v", s)
	}
	if s.FirstN != 10 || !within(s.First50, 5000) || !within(s.First95, 10000) || s.Requests != 10 {
		t.Fatalf("terra first content %+v", s)
	}

	// replacing a reply moves it, it doesn't count twice
	r = resp("t10", "/f", t0-20_000, t0-10_000, 50)
	e.Response(r)
	if s := e.Period(t0, PeriodDay).Speeds[1]; s.Timed != 10 || s.FirstN != 9 {
		t.Fatalf("after upsert %+v", s)
	}

	// and the history file keeps the spread
	b, err := json.Marshal(e.days["2026-11-14"])
	if err != nil {
		t.Fatal(err)
	}
	var d day
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	k := modelKey{"codex", "gpt-6.1-terra"}
	if n, _ := quantiles(d.dists[k].Speed, 1, 0.5); n != 10 {
		t.Fatalf("round trip: %s", b)
	}
}

func TestActivityStreaks(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	at := func(daysAgo int, key string, out int64) {
		end := t0 - int64(daysAgo)*86_400_000
		e.Response(resp(key, "/f", end-10_000, end, out))
	}
	// a 3-day streak ending 10 days ago, then 4 days ending yesterday
	for _, d := range []int{12, 11, 10, 4, 3, 2, 1} {
		at(d, fmt.Sprint("d", d), 100)
	}
	at(3, "big", 9000)
	a := e.Activity(t0)
	if len(a.Days) != ActivityDays || a.Days[len(a.Days)-1].Date != "2026-11-14" {
		t.Fatalf("days %d, last %v", len(a.Days), a.Days[len(a.Days)-1])
	}
	if a.ActiveDays != 7 || a.Current != 4 || a.Longest != 4 || a.LongestTo != "2026-11-13" {
		t.Fatalf("streaks %+v", a)
	}
	if a.Best.Date != "2026-11-11" || a.Best.Requests != 2 {
		t.Fatalf("best %+v", a.Best)
	}
	// today counts once it has a reply; a gap ends the streak
	at(0, "today", 10)
	if a := e.Activity(t0); a.Current != 5 || a.Longest != 5 {
		t.Fatalf("with today %+v", a)
	}
	if a := e.Activity(t0 + 2*86_400_000); a.Current != 0 {
		t.Fatalf("after a gap %+v", a.Current)
	}
}

func TestForgetDropsOneAgentAndReopensItsDays(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	e.SetSources([]usage.Source{usage.Codex, usage.ClaudeCode})
	day := int64(86_400_000)
	e.Response(resp("x", "/f", t0-2*day-10_000, t0-2*day, 100))
	c := resp("c", "/g", t0-2*day-10_000, t0-2*day, 50)
	c.Source, c.Model = usage.ClaudeCode, "claude-opus-5-5"
	e.Response(c)
	path := t.TempDir() + "/h.json"
	if err := e.SaveHistory(path, t0); err != nil {
		t.Fatal(err)
	}
	e2 := New(pricing.Bundled, time.UTC)
	e2.SetSources([]usage.Source{usage.Codex, usage.ClaudeCode})
	if err := e2.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	e2.Forget(usage.Codex)
	v := e2.Period(t0, PeriodWeek)
	if v.Requests != 1 || len(v.BySource) != 1 || v.BySource[0].Source != usage.ClaudeCode || v.Output != 50 {
		t.Fatalf("after forget %+v", v.Agg)
	}
	if !e2.SeedSince(time.UnixMilli(t0)).IsZero() {
		t.Fatal("codex's days not reopened")
	}
	// rereading its log counts it once again
	e2.Response(resp("x", "/f", t0-2*day-10_000, t0-2*day, 100))
	if v := e2.Period(t0, PeriodWeek); v.Requests != 2 || len(v.BySource) != 2 {
		t.Fatalf("reread %+v", v.Agg)
	}
}

func TestByEffort(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	for i, eff := range []string{"high", "high", "low", ""} {
		r := resp(fmt.Sprint("e", i), "/f", t0-20_000, t0-10_000, 100)
		r.Effort = eff
		e.Response(r)
	}
	got := map[string]int64{}
	for _, r := range e.Period(t0, PeriodDay).ByEffort {
		got[r.Name] = r.Requests
	}
	if got["high"] != 2 || got["low"] != 1 || got["—"] != 1 {
		t.Fatalf("by effort %v", got)
	}
}
