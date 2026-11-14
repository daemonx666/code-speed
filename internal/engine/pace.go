package engine

import (
	"math"

	"github.com/daemonx666/code-speed/internal/usage"
)

// PaceOf compares a quota window's use with the time gone in it, as
// CodexBar's UsagePace does (MIT, github.com/steipete/CodexBar): the
// expected use is the share of the window gone; at the pace so far, the
// window runs out before its reset or lasts to it. nil when the window has
// no reset time or length, or has not started.
func PaceOf(w usage.QuotaWindow, nowMs int64) *usage.Pace {
	if w.ResetsAtMs <= 0 || w.WindowMinutes <= 0 {
		return nil
	}
	duration := float64(w.WindowMinutes) * 60_000
	left := float64(w.ResetsAtMs - nowMs)
	if left <= 0 || left > duration {
		return nil
	}
	elapsed := duration - left
	expected := math.Min(100, math.Max(0, elapsed/duration*100))
	actual := math.Min(100, math.Max(0, w.UsedPercent))
	if elapsed == 0 && actual > 0 {
		return nil
	}
	p := &usage.Pace{ExpectedPercent: expected, DeltaPercent: actual - expected, Stage: paceStage(actual - expected)}
	switch {
	case actual >= 100:
		p.RunsOutInMs = 0
	case elapsed > 0 && actual > 0:
		rate := actual / elapsed // percent per ms
		runsOut := (100 - actual) / rate
		if runsOut >= left {
			p.LastsToReset = true
		} else {
			p.RunsOutInMs = int64(runsOut)
		}
	default:
		p.LastsToReset = true
	}
	return p
}

func paceStage(delta float64) string {
	abs := math.Abs(delta)
	switch {
	case abs <= 2:
		return "onTrack"
	case abs <= 6 && delta > 0:
		return "slightlyAhead"
	case abs <= 6:
		return "slightlyBehind"
	case abs <= 12 && delta > 0:
		return "ahead"
	case abs <= 12:
		return "behind"
	case delta > 0:
		return "farAhead"
	}
	return "farBehind"
}
