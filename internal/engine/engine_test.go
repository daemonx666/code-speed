package engine

import (
	"math"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/usage"
)

var t0 = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).UnixMilli()

func resp(key, agent string, start, end, out int64) usage.Response {
	return usage.Response{Key: key, Source: usage.Codex, Agent: agent, Model: "gpt-6.1-terra",
		StartMs: start, EndMs: end, Final: true, Tokens: usage.Tokens{Input: 1000, Output: out}}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLiveRateSpreadsOutputOverItsInterval(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	// 600 tokens over 6 s: 100 tok/s, of which the last 3 s fall in the window
	e.Response(resp("a", "/f", t0-6000, t0, 600))
	if s := e.Snapshot(t0); !near(s.Live, 100) || s.LiveEstimated != 0 {
		t.Fatalf("live = %v (est %v), want 100", s.Live, s.LiveEstimated)
	}
	// 1.5 s later only half the window still overlaps the response
	if s := e.Snapshot(t0 + 1500); !near(s.Live, 50) {
		t.Fatalf("live = %v, want 50", s.Live)
	}
	if s := e.Snapshot(t0 + 4000); s.Live != 0 {
		t.Fatalf("live = %v after the response, want 0", s.Live)
	}
}

func TestInFlightAgentIsEstimatedFromItsRecentSpeed(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	e.Response(resp("a", "/f", t0-60_000, t0-50_000, 400)) // 40 tok/s
	e.Agent(usage.AgentState{Agent: "/f", Source: usage.Codex, Phase: usage.Generating,
		InFlightMs: t0 - 10_000, LastLineMs: t0 - 1000})
	s := e.Snapshot(t0)
	if !near(s.Live, 40) || s.LiveEstimated != 1 {
		t.Fatalf("live = %v (est %v), want 40 estimated", s.Live, s.LiveEstimated)
	}
	// a stalled log stops being estimated
	e.Agent(usage.AgentState{Agent: "/f", Source: usage.Codex, Phase: usage.Generating,
		InFlightMs: t0 - 10_000, LastLineMs: t0 - 400_000})
	if s := e.Snapshot(t0); s.Live != 0 {
		t.Fatalf("live = %v for an agent silent 400 s", s.Live)
	}
}

func TestUpsertReplacesItsContribution(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	r := resp("m", "/f", t0-4000, t0-2000, 5)
	r.Final = false
	e.Response(r)
	r.Output, r.EndMs, r.Final = 200, t0, true
	e.Response(r)
	s := e.Snapshot(t0)
	if s.Today.Requests != 1 || s.Today.Output != 200 {
		t.Fatalf("today = %+v, want one response of 200", s.Today.Agg)
	}
	if !near(s.Today.AvgSpeed, 50) {
		t.Fatalf("avg = %v, want 50", s.Today.AvgSpeed)
	}
	// gpt-6.1-terra is priced in the bundled table
	if s.Today.CostMicro <= 0 || s.Today.Unpriced != 0 {
		t.Fatalf("cost = %d unpriced = %d", s.Today.CostMicro, s.Today.Unpriced)
	}
}

func TestUnknownModelIsUnpricedNotFree(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	r := resp("x", "/f", t0-1000, t0, 10)
	r.Model = "some-model-nobody-prices"
	e.Response(r)
	if s := e.Snapshot(t0); s.Today.CostMicro != 0 || s.Today.Unpriced != 1 {
		t.Fatalf("cost = %d unpriced = %d", s.Today.CostMicro, s.Today.Unpriced)
	}
}

func TestDaysAreLocal(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	e := New(pricing.Bundled, loc)
	// 17:00 UTC on the 13th is 01:00 on the 14th in UTC+8
	end := time.Date(2026, 11, 13, 17, 0, 0, 0, time.UTC).UnixMilli()
	e.Response(resp("d", "/f", end-1000, end, 10))
	s := e.Snapshot(t0)
	if s.Today.Date != "2026-11-14" || s.Today.Requests != 1 {
		t.Fatalf("today %s has %d", s.Today.Date, s.Today.Requests)
	}
	if len(s.Week) != HistoryDays || s.Week[HistoryDays-1].Requests != 1 {
		t.Fatalf("week = %+v", s.Week)
	}
}

func TestQuotaKeepsNewest(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	e.Quota(usage.Quota{Source: usage.Codex, ObservedMs: 2, Windows: []usage.QuotaWindow{{UsedPercent: 20}}})
	e.Quota(usage.Quota{Source: usage.Codex, ObservedMs: 1, Windows: []usage.QuotaWindow{{UsedPercent: 90}}})
	if s := e.Snapshot(t0); len(s.Quotas) != 1 || s.Quotas[0].Windows[0].UsedPercent != 20 {
		t.Fatalf("quotas = %+v", s.Quotas)
	}
}

func TestLiveCostSpreadsCostLikeOutput(t *testing.T) {
	e := New(pricing.Bundled, time.UTC)
	r := resp("a", "/f", t0-6000, t0, 600)
	e.Response(r)
	cost := pricing.Bundled.ResponseCost(r).Micro
	// half the response is in the 3 s window: cost/2 over 3 s, an hour of it
	want := float64(cost) / 2 / 3 * 3600
	if s := e.Snapshot(t0); math.Abs(float64(s.LiveCostPerHour)-want) > 1 {
		t.Fatalf("live cost %d µUSD/h, want %.0f", s.LiveCostPerHour, want)
	}
	// an agent writing now is estimated at its recent cost per second
	e.Agent(usage.AgentState{Agent: "/f", Source: usage.Codex, Phase: usage.Generating, InFlightMs: t0 + 10_000, LastLineMs: t0 + 20_000})
	perSec := float64(cost) / 6
	if s := e.Snapshot(t0 + 20_000); math.Abs(float64(s.LiveCostPerHour)-perSec*3600) > 1 || s.LiveEstimated != 1 {
		t.Fatalf("estimated live cost %d µUSD/h, want %.0f", s.LiveCostPerHour, perSec*3600)
	}
	if s := e.Snapshot(t0); s.Last30BySource[usage.Codex] != cost {
		t.Fatalf("last 30 days %v, want %d", s.Last30BySource, cost)
	}
}
