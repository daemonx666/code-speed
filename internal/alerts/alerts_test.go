package alerts

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/settings"
	"github.com/daemonx666/code-speed/internal/usage"
)

const now = int64(1_794_600_000_000)

func snap(used float64, pace *usage.Pace) engine.Snapshot {
	var s engine.Snapshot
	s.NowMs = now
	s.Today.Date = "2026-11-14"
	s.Quotas = []usage.Quota{{Source: usage.Codex, Windows: []usage.QuotaWindow{
		{Kind: "sevenDay", UsedPercent: used, WindowMinutes: 10080, ResetsAtMs: now + 3*86_400_000, Pace: pace}}}}
	return s
}

func keys(as []Alert) string {
	var k []string
	for _, a := range as {
		k = append(k, a.Kind)
	}
	return strings.Join(k, ",")
}

func TestQuotaThresholdsFireOncePerWindowAndSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	st := settings.Default()
	c := New(path)
	if got := c.Check(snap(70, nil), st, "en"); len(got) != 0 {
		t.Fatalf("70%%: %v", keys(got))
	}
	got := c.Check(snap(81, nil), st, "en")
	if len(got) != 1 || got[0].Title != "Codex weekly quota at 81%" || !strings.HasPrefix(got[0].Body, "Resets in 3d") {
		t.Fatalf("81%%: %+v", got)
	}
	if again := c.Check(snap(85, nil), st, "en"); len(again) != 0 {
		t.Fatal("80% fired twice in one window")
	}
	// a restart remembers; 95 % still fires
	c = New(path)
	if got := c.Check(snap(96, nil), st, "zh-CN"); len(got) != 1 || got[0].Title != "Codex 每周 额度已用 96%" {
		t.Fatalf("96%% after restart: %+v", got)
	}
	// jumping straight past both thresholds in a new window notifies once
	s := snap(97, nil)
	s.Quotas[0].Windows[0].ResetsAtMs += 7 * 86_400_000
	if got := c.Check(s, st, "en"); len(got) != 1 || !strings.Contains(got[0].Title, "97%") {
		t.Fatalf("new window: %+v", got)
	}
	if len(c.Recent()) != 3 {
		t.Fatalf("recent %d", len(c.Recent()))
	}
}

func TestPaceAndBudget(t *testing.T) {
	st := settings.Default()
	st.Budget = settings.Budget{Daily: 10, Monthly: 100}
	st.Currency = settings.Currency{Code: "CNY", Rate: 7.2}
	c := New("")
	s := snap(40, &usage.Pace{RunsOutInMs: 36 * 3_600_000})
	s.Today.CostMicro = 8_500_000 // 85 % of the day
	s.MonthCostMicro = 120_000_000
	got := c.Check(s, st, "en")
	if keys(got) != "pace,budget,budget" {
		t.Fatalf("alerts %v", keys(got))
	}
	if got[1].Title != "Today's spend reached 80% of the budget" || got[1].Body != "¥61.20 spent of ¥72.00 (API price)" {
		t.Fatalf("day budget %+v", got[1])
	}
	if !strings.Contains(got[2].Title, "100%") {
		t.Fatalf("month budget %+v", got[2])
	}
	// over 100 % later today: once more; the 80 % one is not repeated
	s.Today.CostMicro = 11_000_000
	if got := c.Check(s, st, "en"); keys(got) != "budget" || !strings.Contains(got[0].Title, "100%") {
		t.Fatalf("day over: %+v", got)
	}
	// turned off: nothing
	st.Alerts = settings.Alerts{}
	if got := New("").Check(s, st, "en"); len(got) != 0 {
		t.Fatalf("off: %v", keys(got))
	}
}

func TestPeakRecord(t *testing.T) {
	st := settings.Default()
	st.Alerts.Peak = true
	c := New("")
	s := snap(10, nil)
	for i, live := range []float64{30, 32, 40, 60} {
		s.NowMs = now + int64(i)*60_000
		s.Live = live
		got := c.Check(s, st, "en")
		// the day's first reading never notifies; 32 isn't 10 % over 30;
		// 40 is; 60 comes within 30 minutes of it
		want := map[int]int{0: 0, 1: 0, 2: 1, 3: 0}[i]
		if len(got) != want {
			t.Fatalf("reading %d (%v): %v", i, live, keys(got))
		}
	}
}
