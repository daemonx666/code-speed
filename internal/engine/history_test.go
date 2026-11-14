package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/usage"
)

func at(day, hour int) int64 {
	return time.Date(2026, 11, day, hour, 0, 0, 0, time.UTC).UnixMilli()
}

func withProject(r usage.Response, src usage.Source, project string) usage.Response {
	r.Source, r.Project = src, project
	return r
}

func TestHistoryKeepsClosedDaysAndSkipsThemOnReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	e := New(pricing.Bundled, time.UTC)
	old := resp("old", "/f", at(12, 9)-2000, at(12, 9), 100)
	e.Response(old)
	e.Response(resp("today", "/f", at(14, 9)-2000, at(14, 9), 50))
	if err := e.SaveHistory(path, at(14, 12)); err != nil {
		t.Fatal(err)
	}
	if e.Closed() != "2026-11-13" { // every day before today, data or not
		t.Fatalf("closed = %q", e.Closed())
	}

	// a fresh start: the history holds the days to the 13th; the logs are
	// read again from the 14th, and a copy of the old response is not
	// counted twice
	e2 := New(pricing.Bundled, time.UTC)
	if err := e2.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	if got := e2.SeedSince(time.UnixMilli(at(14, 12))); !got.Equal(time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("seed since %v", got)
	}
	e2.Response(old)
	v := e2.Period(at(14, 12), PeriodWeek)
	if v.Requests != 1 || v.Output != 100 {
		t.Fatalf("week after reload: %d requests, %d output", v.Requests, v.Output)
	}
}

func TestUnreadableHistoryIsSetAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	e := New(pricing.Bundled, time.UTC)
	if err := e.LoadHistory(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".unreadable"); err != nil {
		t.Fatal("an unreadable history was not set aside")
	}
	if !e.SeedSince(time.Now()).IsZero() {
		t.Fatal("with no history the logs are read from the beginning")
	}
}

func TestPeriodsInEveryDimension(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	e.Response(withProject(resp("a", "/a", at(14, 9)-2000, at(14, 9), 100), usage.Codex, "api"))
	e.Response(withProject(resp("b", "/b", at(14, 15)-2000, at(14, 15), 40), usage.ClaudeCode, "web"))
	e.Response(withProject(resp("c", "/c", at(10, 8)-2000, at(10, 8), 10), usage.ClaudeCode, "web"))
	e.Response(withProject(resp("d", "/d", at(1, 8)-2000, at(1, 8), 5), usage.ClaudeCode, "web"))

	day := e.Period(at(14, 20), PeriodDay)
	if day.Requests != 2 || len(day.Series) != 24 || day.Series[9].Output != 100 || day.Series[15].Output != 40 {
		t.Fatalf("day: %d requests, series %+v", day.Requests, day.Series)
	}
	if len(day.BySource) != 2 || len(day.ByProject) != 2 {
		t.Fatalf("day rows: %d sources, %d projects", len(day.BySource), len(day.ByProject))
	}
	week := e.Period(at(14, 20), PeriodWeek)
	if week.Requests != 3 || len(week.Series) != 7 || week.ActiveDays != 2 {
		t.Fatalf("week: %d requests, %d points, %d active days", week.Requests, len(week.Series), week.ActiveDays)
	}
	if last := week.Series[6]; last.Label != "2026-11-14" || last.Sources[usage.Codex].Tokens == 0 {
		t.Fatalf("week's today: %+v", last)
	}
	// the bars split by model too: one model under two agents is two parts
	e.Response(func() usage.Response {
		r := withProject(resp("e", "/e", at(14, 16)-2000, at(14, 16), 30), usage.Codex, "api")
		r.Model = "gpt-6.1-mini"
		return r
	}())
	week = e.Period(at(14, 20), PeriodWeek)
	if len(week.Models) != 3 || week.Models[0].Key == OtherModels {
		t.Fatalf("week models: %+v", week.Models)
	}
	last := week.Series[6].Models
	var sum int64
	for _, m := range week.Models {
		sum += last[m.Key].Tokens
	}
	if len(last) != 3 || sum != week.Series[6].Tokens {
		t.Fatalf("week's today by model: %+v, %d of %d tokens", last, sum, week.Series[6].Tokens)
	}
	if day := e.Period(at(14, 20), PeriodDay); day.Series[16].Models != nil {
		t.Fatal("hours aren't kept by model")
	}
	month := e.Period(at(14, 20), PeriodMonth)
	if month.Requests != 5 || len(month.Series) != 30 {
		t.Fatalf("month: %d requests, %d points", month.Requests, len(month.Series))
	}
	all := e.Period(at(14, 20), PeriodAll)
	if all.Requests != 5 || all.HistoryFrom != "2026-11-01" || len(all.Series) != 1 || all.Series[0].Label != "2026-11" {
		t.Fatalf("all: %+v", all.Series)
	}
}

func TestAnAgentTurnedOnLaterIsReadFromItsOwnStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	e := New(pricing.Bundled, time.UTC)
	e.SetSources([]usage.Source{usage.Codex})
	e.Response(resp("a", "/f", at(12, 9)-2000, at(12, 9), 100))
	if err := e.SaveHistory(path, at(14, 12)); err != nil {
		t.Fatal(err)
	}
	// Claude Code is turned on: it has no history, so the logs are read
	// from the beginning, and Codex's closed days are still skipped
	e2 := New(pricing.Bundled, time.UTC)
	e2.SetSources([]usage.Source{usage.Codex, usage.ClaudeCode})
	e2.LoadHistory(path)
	if !e2.SeedSince(time.UnixMilli(at(14, 12))).IsZero() {
		t.Fatal("a newly read agent needs the whole log")
	}
	e2.Response(resp("a", "/f", at(12, 9)-2000, at(12, 9), 100))
	c := withProject(resp("c", "/c", at(12, 10)-2000, at(12, 10), 7), usage.ClaudeCode, "x")
	e2.Response(c)
	if v := e2.Period(at(14, 12), PeriodWeek); v.Requests != 2 || v.Output != 107 {
		t.Fatalf("week: %d requests, %d output", v.Requests, v.Output)
	}
}

func TestFocusAndPrevious(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	e.Response(withProject(resp("a", "/a", at(14, 9)-2000, at(14, 9), 100), usage.Codex, "api"))
	e.Response(withProject(resp("b", "/b", at(14, 15)-2000, at(14, 15), 40), usage.ClaudeCode, "web"))
	e.Response(withProject(resp("c", "/c", at(12, 8)-2000, at(12, 8), 10), usage.ClaudeCode, "web"))
	e.Response(withProject(resp("y1", "/y", at(13, 8)-2000, at(13, 8), 7), usage.Codex, "api"))
	e.Response(withProject(resp("y2", "/y", at(13, 22)-2000, at(13, 22), 9), usage.Codex, "api"))
	e.Response(withProject(resp("w", "/w", at(5, 8)-2000, at(5, 8), 3), usage.Codex, "api"))

	// a day of the week: rows narrowed to it, totals kept for the week
	v := e.Period(at(14, 20), PeriodWeek, "2026-11-12")
	if v.Focus != "2026-11-12" || v.Focused == nil || v.Focused.Requests != 1 || v.Requests != 5 {
		t.Fatalf("focus day: %+v / %d", v.Focused, v.Requests)
	}
	if len(v.BySource) != 1 || v.BySource[0].Source != usage.ClaudeCode || len(v.ByModel) != 1 || len(v.ByProject) != 1 {
		t.Fatalf("focus rows: %+v", v.BySource)
	}
	// an hour of today: agents only
	v = e.Period(at(14, 20), PeriodDay, "09")
	if v.Focused == nil || v.Focused.Requests != 1 || len(v.BySource) != 1 || v.BySource[0].Source != usage.Codex || len(v.ByModel) != 0 {
		t.Fatalf("focus hour: %+v %+v", v.Focused, v.BySource)
	}
	// a month
	if v = e.Period(at(14, 20), PeriodYear, "2026-11"); v.Focused == nil || v.Focused.Requests != 6 {
		t.Fatalf("focus month: %+v", v.Focused)
	}
	// yesterday up to this hour: 08:00 counts, 22:00 not yet
	if v = e.Period(at(14, 20), PeriodDay); v.Prev == nil || v.Prev.Requests != 1 || v.Prev.Output != 7 {
		t.Fatalf("prev day: %+v", v.Prev)
	}
	// the 7 days before the week
	if v = e.Period(at(14, 20), PeriodWeek); v.Prev == nil || v.Prev.Requests != 1 || v.Prev.From != "2026-11-01" || v.Prev.To != "2026-11-07" {
		t.Fatalf("prev week: %+v", v.Prev)
	}
	if v = e.Period(at(14, 20), PeriodAll); v.Prev != nil {
		t.Fatal("all has no period before")
	}
}

func TestSessions(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	e.Response(withProject(resp("a1", "/s1", at(14, 9)-4000, at(14, 9), 400), usage.Codex, "api"))
	e.Response(withProject(resp("a2", "/s1", at(14, 10)-2000, at(14, 10), 100), usage.Codex, "api"))
	e.Response(withProject(resp("b1", "/s2", at(14, 11)-1000, at(14, 11), 50), usage.ClaudeCode, "web"))
	e.Response(withProject(resp("old", "/s3", at(10, 11)-1000, at(10, 11), 50), usage.ClaudeCode, "web"))
	e.Agent(usage.AgentState{Agent: "/s1", Source: usage.Codex, Phase: usage.Tools, Context: 1234, ContextWindow: 272000, LastLineMs: at(14, 10) + 5})
	ss := e.Sessions(at(14, 0))
	if len(ss) != 2 || ss[0].Agent != "/s2" || ss[1].Agent != "/s1" {
		t.Fatalf("sessions %+v", ss)
	}
	s1 := ss[1]
	if s1.Requests != 2 || s1.Output != 500 || s1.Phase != usage.Tools || s1.Context != 1234 || s1.FirstMs != at(14, 9)-4000 {
		t.Fatalf("s1 %+v", s1)
	}
	if !near(s1.AvgSpeed, 500.0/6) {
		t.Fatalf("avg %v", s1.AvgSpeed)
	}
	rq := e.SessionRequests("/s1")
	if len(rq) != 2 || rq[0].Key != "a1" || !near(rq[0].Speed, 100) || !rq[0].Priced || rq[0].CostMicro <= 0 {
		t.Fatalf("requests %+v", rq)
	}
}

func TestExportAndMonthCost(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	e.Response(withProject(resp("a", "/a", at(14, 9)-2000, at(14, 9), 100), usage.Codex, "api"))
	e.Response(withProject(resp("b", "/b", at(13, 9)-2000, at(13, 9), 40), usage.ClaudeCode, "web"))
	e.Response(withProject(resp("old", "/b", time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC).UnixMilli()-2000, time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC).UnixMilli(), 40), usage.ClaudeCode, "web"))
	rows := e.Export(at(14, 20), PeriodWeek)
	if len(rows) != 2 || rows[0].Date != "2026-11-13" || rows[1].Source != usage.Codex || rows[1].Output != 100 || rows[1].CostUSD <= 0 {
		t.Fatalf("rows %+v", rows)
	}
	var b strings.Builder
	if err := WriteCSV(&b, rows); err != nil || !strings.HasPrefix(b.String(), "date,source,agent,model,requests") || strings.Count(b.String(), "\n") != 3 {
		t.Fatalf("csv %q %v", b.String(), err)
	}
	s := e.Snapshot(at(14, 20))
	if s.MonthCostMicro != s.Week[6].CostMicro+s.Week[5].CostMicro {
		t.Fatalf("month %d, want only November's", s.MonthCostMicro)
	}
}

func TestTheChartKeepsTheTopModelsAndGroupsTheRest(t *testing.T) {
	var rows []Row
	for i := range 10 {
		rows = append(rows, Row{Name: fmt.Sprintf("m%d", i), Source: usage.Codex})
	}
	info, part := chartModelKeys(rows)
	if len(info) != chartModels+1 || info[chartModels].Key != OtherModels {
		t.Fatalf("parts: %+v", info)
	}
	if part(modelKey{usage.Codex, "m0"}) != "codex:m0" || part(modelKey{usage.Codex, "m9"}) != OtherModels {
		t.Fatal("parts of models")
	}
	// one past the top is shown itself, not as an "other" of one
	if info, _ := chartModelKeys(rows[:chartModels+1]); len(info) != chartModels+1 || info[chartModels].Key == OtherModels {
		t.Fatalf("eight models: %+v", info)
	}
}
