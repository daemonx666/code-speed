package cli

import (
	"fmt"
	"math"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Tokens formats a count: 950, 12.3K, 4.56M, 1.2B.
func Tokens(n int64) string {
	f := float64(n)
	switch {
	case math.Abs(f) >= 1e9:
		return trim(f/1e9) + "B"
	case math.Abs(f) >= 1e6:
		return trim(f/1e6) + "M"
	case math.Abs(f) >= 1e3:
		return trim(f/1e3) + "K"
	}
	return fmt.Sprintf("%d", n)
}

func trim(f float64) string {
	switch {
	case f >= 100:
		return fmt.Sprintf("%.0f", f)
	case f >= 10:
		return fmt.Sprintf("%.1f", f)
	}
	return fmt.Sprintf("%.2f", f)
}

// USD formats micro-USD.
func USD(micro int64) string {
	d := float64(micro) / 1e6
	switch {
	case d == 0:
		return "$0"
	case d < 0.01:
		return "<$0.01"
	case d >= 1000:
		return fmt.Sprintf("$%.0f", d)
	}
	return fmt.Sprintf("$%.2f", d)
}

// Speed formats tok/s.
func Speed(v float64) string {
	switch {
	case v <= 0:
		return "—"
	case v >= 100:
		return fmt.Sprintf("%.0f tok/s", v)
	}
	return fmt.Sprintf("%.1f tok/s", v)
}

// Until formats the time from now to a reset: 2h 15m, 3d 4h, 45s.
func Until(nowMs, atMs int64) string {
	if atMs <= 0 {
		return "?"
	}
	d := time.Duration(atMs-nowMs) * time.Millisecond
	if d <= 0 {
		return "now"
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// WindowName names a quota window.
func WindowName(kind string, minutes int64) string {
	switch kind {
	case "fiveHour":
		return "5h"
	case "sevenDay":
		return "7d"
	}
	if minutes%1440 == 0 {
		return fmt.Sprintf("%dd", minutes/1440)
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// CacheHit is the share of input read from the cache: "97%", "—" with no input.
func CacheHit(cached, input int64) string {
	if input <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", float64(cached)/float64(input)*100)
}

// PaceText says how a quota window is being used against its time.
func PaceText(p *usage.Pace) string {
	if p == nil {
		return ""
	}
	var s string
	switch {
	case p.DeltaPercent > 2:
		s = fmt.Sprintf("%.0f%% ahead of pace", p.DeltaPercent)
	case p.DeltaPercent < -2:
		s = fmt.Sprintf("%.0f%% under pace", -p.DeltaPercent)
	default:
		s = "on pace"
	}
	if p.LastsToReset {
		return s + ", lasts to the reset"
	}
	return s + ", runs out in " + Until(0, p.RunsOutInMs)
}
