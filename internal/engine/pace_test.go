package engine

import (
	"testing"

	"github.com/daemonx666/code-speed/internal/usage"
)

func TestPace(t *testing.T) {
	const h = 3_600_000
	week := int64(7 * 24 * 60)
	now := int64(1_000_000_000_000)
	// half the week gone, 50 % used: on track, lasts to the reset
	p := PaceOf(usage.QuotaWindow{UsedPercent: 50, WindowMinutes: week, ResetsAtMs: now + 84*h}, now)
	if p == nil || p.Stage != "onTrack" || !p.LastsToReset || p.RunsOutInMs != 0 {
		t.Fatalf("on track: %+v", p)
	}
	// a day gone, 40 % used: far ahead; at 40 % a day the rest lasts 1.5 days
	p = PaceOf(usage.QuotaWindow{UsedPercent: 40, WindowMinutes: week, ResetsAtMs: now + 144*h}, now)
	if p == nil || p.Stage != "farAhead" || p.LastsToReset || p.RunsOutInMs != 36*h {
		t.Fatalf("ahead: %+v", p)
	}
	// 5-hour window, 4 hours gone, 30 % used: far behind
	p = PaceOf(usage.QuotaWindow{UsedPercent: 30, WindowMinutes: 300, ResetsAtMs: now + h}, now)
	if p == nil || p.Stage != "farBehind" || !p.LastsToReset {
		t.Fatalf("behind: %+v", p)
	}
	// no reset time, a reset in the past, or a window longer than said: nothing
	for _, w := range []usage.QuotaWindow{{UsedPercent: 5, WindowMinutes: 300}, {UsedPercent: 5, WindowMinutes: 300, ResetsAtMs: now - 1},
		{UsedPercent: 5, WindowMinutes: 300, ResetsAtMs: now + 6*h}} {
		if PaceOf(w, now) != nil {
			t.Fatalf("pace for %+v", w)
		}
	}
	// used up
	if p := PaceOf(usage.QuotaWindow{UsedPercent: 100, WindowMinutes: 300, ResetsAtMs: now + h}, now); p == nil || p.LastsToReset || p.RunsOutInMs != 0 {
		t.Fatalf("used up: %+v", p)
	}
}
