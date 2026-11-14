// Package cli is code-speed's command line.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/daemonx666/code-speed/internal/appdir"
	"github.com/daemonx666/code-speed/internal/claudebridge"
	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/plugin"
	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/service"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

const usageText = `code-speed: live token speed, usage, cost and quotas of your coding agents.

Usage:
  code-speed            open the menu bar app (the terminal build runs watch)
  code-speed watch      one live line in the terminal, refreshed every second
  code-speed today      today's usage and cost by agent and model
  code-speed week       the last 7 days
  code-speed stats [day|week|month|year|all] [agent|model|project]
                        usage of a period, by day or month and by one dimension
  code-speed quota      subscription limits, their pace and when they reset
  code-speed claude-quota on|off|status
                        read Claude Code's subscription limits through its status line
  code-speed plugins    the plugins in the plugins folder, and why any can't load
  code-speed plugin test <folder> [sample.jsonl]
                        check a plugin: parse a sample file, or run its command once
  code-speed prices     custom prices: where the file is and what it holds
  code-speed json       everything above as one JSON snapshot
  code-speed version    print the version

Reads the local logs of Claude Code, Codex, Gemini CLI, Grok Build, Kimi Code,
OpenCode, Pi and Oh My Pi (Cursor, online, when turned on in the app's Settings).
Finished days are kept in the history file, so later starts read only what is new.
`

// Start reads the history and the logs, and returns when done.
func Start(version string) *service.Service {
	s := service.New(version)
	s.SeedNow()
	s.Monitor().SaveHistory(time.Now())
	return s
}

// Run runs a command and returns the exit code. gui opens the desktop
// app; nil in the terminal-only build.
func Run(args []string, version string, gui func() error) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	out := os.Stdout
	switch cmd {
	case "statusline":
		// Claude Code's status line (claudebridge): fast, silent, never fails
		return claudebridge.RunHelper(os.Stdin, out)
	case "":
		if gui != nil {
			if err := gui(); err != nil {
				fmt.Fprintln(os.Stderr, "code-speed:", err)
				return 1
			}
			return 0
		}
		return watch(out, version)
	case "watch":
		return watch(out, version)
	case "today":
		printToday(out, Start(version).Monitor().Engine.Snapshot(time.Now().UnixMilli()))
	case "week":
		printWeek(out, Start(version).Monitor().Engine.Snapshot(time.Now().UnixMilli()))
	case "plugins":
		return listPlugins(out)
	case "plugin":
		return testPlugin(out, args[1:])
	case "prices":
		return showPrices(out)
	case "claude-quota":
		return claudeQuota(out, version, args[1:])
	case "stats":
		period, dim := engine.PeriodWeek, "agent"
		if len(args) > 1 {
			period = args[1]
		}
		if len(args) > 2 {
			dim = args[2]
		}
		printStats(out, Start(version).Monitor().Engine.Period(time.Now().UnixMilli(), period), dim)
	case "quota":
		printQuota(out, Start(version).Monitor().Engine.Snapshot(time.Now().UnixMilli()))
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(Start(version).Monitor().Engine.Snapshot(time.Now().UnixMilli())); err != nil {
			return 1
		}
	case "version", "--version", "-v":
		fmt.Fprintln(out, "code-speed", version)
	case "help", "--help", "-h":
		fmt.Fprint(out, usageText)
	default:
		fmt.Fprintf(os.Stderr, "code-speed: unknown command %q\n\n%s", cmd, usageText)
		return 2
	}
	return 0
}

// Line is the one-line summary the watch command and the tray show.
func Line(s engine.Snapshot) string {
	parts := []string{"▸ " + Speed(s.Live)}
	if s.LiveEstimated > 0.5 {
		parts[0] += " (est.)"
	}
	if s.LiveCostPerHour > 0 {
		parts[0] += " ≈ " + USD(s.LiveCostPerHour) + "/h"
	}
	parts = append(parts, "today "+Tokens(s.Today.Total())+" tok", USD(s.Today.CostMicro))
	if s.Today.AvgSpeed > 0 {
		parts = append(parts, "avg "+Speed(s.Today.AvgSpeed))
	}
	for _, q := range s.Quotas {
		var ws []string
		for _, w := range q.Windows {
			ws = append(ws, fmt.Sprintf("%s %.0f%%", WindowName(w.Kind, w.WindowMinutes), w.UsedPercent))
		}
		parts = append(parts, q.Source.Label()+" "+strings.Join(ws, " "))
	}
	return strings.Join(parts, " · ")
}

func watch(out io.Writer, version string) int {
	m := Start(version).Monitor()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go m.Run(ctx)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		line := Line(m.Engine.Snapshot(time.Now().UnixMilli()))
		fmt.Fprintf(out, "\r\033[K%s", line)
		select {
		case <-ctx.Done():
			fmt.Fprintln(out)
			return 0
		case <-tick.C:
		}
	}
}

func table(out io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
}

func printRows(out io.Writer, title string, rows []engine.Row) {
	fmt.Fprintf(out, "\n%s\n", title)
	w := table(out)
	fmt.Fprintln(w, "\tRequests\tInput\tCache hit\tOutput\tAvg speed\tCost\t")
	for _, r := range rows {
		cost := USD(r.CostMicro)
		if r.Unpriced > 0 {
			cost += fmt.Sprintf(" (+%d unpriced)", r.Unpriced)
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t\n", r.Label, r.Requests, Tokens(r.Input),
			CacheHit(r.CachedInput, r.Input), Tokens(r.Output), Speed(r.AvgSpeed), cost)
	}
	w.Flush()
}

func printToday(out io.Writer, s engine.Snapshot) {
	t := s.Today
	fmt.Fprintf(out, "Today (%s): %s tokens · %s · %d requests · avg %s\n", t.Date, Tokens(t.Total()),
		USD(t.CostMicro), t.Requests, Speed(t.AvgSpeed))
	if t.Unpriced > 0 {
		fmt.Fprintf(out, "%d responses have no known price and are not in the cost.\n", t.Unpriced)
	}
	if s.Last != nil {
		fmt.Fprintf(out, "Last reply: %s, %s tokens at %s (%s)\n", s.Last.Model, Tokens(s.Last.Output),
			Speed(s.Last.Speed), s.Last.Source.Label())
	}
	if len(t.BySource) > 0 {
		printRows(out, "By agent", t.BySource)
		printRows(out, "By model", t.ByModel)
	}
	fmt.Fprintf(out, "\nCosts are API list prices (table v%d, %s), whatever your plan bills.\n", s.PricesVersion, s.PricesUpdated)
}

func printWeek(out io.Writer, s engine.Snapshot) {
	w := table(out)
	fmt.Fprintln(w, "Date\tRequests\tTokens\tOutput\tCost\t")
	var sum engine.DayTotal
	for _, d := range s.Week {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t\n", d.Date, d.Requests, Tokens(d.Tokens), Tokens(d.Output), USD(d.CostMicro))
		sum.Requests += d.Requests
		sum.Tokens += d.Tokens
		sum.Output += d.Output
		sum.CostMicro += d.CostMicro
	}
	fmt.Fprintf(w, "Total\t%d\t%s\t%s\t%s\t\n", sum.Requests, Tokens(sum.Tokens), Tokens(sum.Output), USD(sum.CostMicro))
	w.Flush()
}

func printQuota(out io.Writer, s engine.Snapshot) {
	if len(s.Quotas) == 0 {
		fmt.Fprintln(out, "No quota seen yet. Codex reports its limits with every reply; use it once and look again.")
		return
	}
	for _, q := range s.Quotas {
		plan := ""
		if q.Plan != "" {
			plan = " (" + q.Plan + ")"
		}
		fmt.Fprintf(out, "%s%s, as of %s\n", q.Source.Label(), plan, time.UnixMilli(q.ObservedMs).Format("Jan 2 15:04"))
		for _, w := range q.Windows {
			fmt.Fprintf(out, "  %-4s %5.1f%% used  resets in %-8s %s\n", WindowName(w.Kind, w.WindowMinutes),
				w.UsedPercent, Until(s.NowMs, w.ResetsAtMs), PaceText(w.Pace))
		}
		if q.Reached != "" {
			fmt.Fprintf(out, "  limit reached: %s\n", q.Reached)
		}
	}
}

func printStats(out io.Writer, v engine.PeriodView, dim string) {
	fmt.Fprintf(out, "%s %s – %s: %s tokens · %s · %d requests · %d active days · avg %s\n", v.Period, v.From, v.To,
		Tokens(v.Total()), USD(v.CostMicro), v.Requests, v.ActiveDays, Speed(v.AvgSpeed))
	w := table(out)
	fmt.Fprintln(w, "\tRequests\tTokens\tOutput\tCost\t")
	for _, p := range v.Series {
		if p.Requests == 0 {
			continue
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t\n", p.Label, p.Requests, Tokens(p.Tokens), Tokens(p.Output), USD(p.CostMicro))
	}
	w.Flush()
	rows, title := v.BySource, "By agent"
	switch dim {
	case "model":
		rows, title = v.ByModel, "By model"
	case "project":
		rows, title = v.ByProject, "By project"
	}
	if len(rows) > 0 {
		if dim != "agent" {
			for i := range rows {
				rows[i].Label = rows[i].Source.Label() + " · " + rows[i].Label
			}
		}
		printRows(out, title, rows)
	}
}

// claudeQuota turns the Claude Code status-line bridge on or off from the
// terminal, as the app's Settings does.
func claudeQuota(out io.Writer, version string, args []string) int {
	svc := service.New(version)
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		svc.HelperPath = exe
	}
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "on", "off":
		st := svc.Settings()
		st.ClaudeQuota = cmd == "on"
		if err := svc.UpdateSettings(st); err != nil {
			fmt.Fprintln(os.Stderr, "code-speed:", err)
			return 1
		}
	case "status":
	default:
		fmt.Fprintln(os.Stderr, "usage: code-speed claude-quota on|off|status")
		return 2
	}
	fmt.Fprintf(out, "Claude Code status line: %s (%s)\n", claudebridge.Status(), claudebridge.SettingsPath())
	return 0
}

func listPlugins(out io.Writer) int {
	loaded := plugin.LoadAll()
	fmt.Fprintf(out, "Plugins folder: %s\n", plugin.Dir())
	if len(loaded) == 0 {
		fmt.Fprintln(out, "No plugins. See docs/plugins.md; examples are in examples/plugins.")
		return 0
	}
	for _, l := range loaded {
		if l.Error != "" {
			fmt.Fprintf(out, "  %-20s error: %s\n", l.ID, l.Error)
			continue
		}
		note := ""
		if l.Type == "command" {
			note = " (runs a program: off until turned on in Settings)"
		}
		fmt.Fprintf(out, "  %-20s %s, %s%s\n", l.ID, l.Label, l.Type, note)
	}
	return 0
}

// testPlugin checks a plugin folder: a jsonl plugin parses a sample file,
// a command plugin runs once; what came out is printed.
func testPlugin(out io.Writer, args []string) int {
	if len(args) < 2 || args[0] != "test" {
		fmt.Fprintln(os.Stderr, "usage: code-speed plugin test <folder> [sample.jsonl]")
		return 2
	}
	m, err := plugin.Read(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-speed:", err)
		return 1
	}
	sink := &printSink{out: out}
	switch m.Type {
	case "jsonl":
		if len(args) < 3 {
			fmt.Fprintf(out, "%s: plugin.json is valid. Give a sample file to parse it.\n", m.ID)
			return 0
		}
		b, err := os.ReadFile(args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, "code-speed:", err)
			return 1
		}
		p := m.Spec().New(args[2], source.NewDedup(), sink)
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				p.Line([]byte(line))
			}
		}
		st := p.State()
		fmt.Fprintf(out, "session: project=%q model=%q context=%d\n", st.Project, st.Model, st.Context)
	case "command":
		lines, errs, err := plugin.Run(context.Background(), m, time.Now().Add(-24*time.Hour))
		if err != nil {
			fmt.Fprintln(os.Stderr, "code-speed: the command failed:", err)
			return 1
		}
		for _, l := range lines {
			if err := l.Apply(m, sink); err != nil {
				errs = append(errs, err)
			}
		}
		for _, e := range errs {
			fmt.Fprintln(out, "bad line:", e)
		}
		if len(errs) > 0 {
			return 1
		}
	}
	if sink.n == 0 {
		fmt.Fprintln(out, "No responses came out: check the record paths (or the command's output).")
		return 1
	}
	return 0
}

// printSink prints what a plugin produced.
type printSink struct {
	out io.Writer
	n   int
}

func (p *printSink) Response(r usage.Response) {
	p.n++
	sp, ok := r.Speed()
	speed := "no speed (no start time)"
	if ok {
		speed = Speed(sp)
	}
	cost := pricing.Bundled.ResponseCost(r)
	price := "unpriced"
	if cost.Known {
		price = USD(cost.Micro)
	}
	fmt.Fprintf(p.out, "response %s: model=%s in=%d (cached %d, write %d) out=%d · %s · %s\n", r.Key, r.Model,
		r.Input, r.CachedInput, r.CacheWrite, r.Output, speed, price)
}

func (p *printSink) Quota(q usage.Quota) {
	for _, w := range q.Windows {
		fmt.Fprintf(p.out, "quota %s %s: %.0f%% used\n", q.Plan, WindowName(w.Kind, w.WindowMinutes), w.UsedPercent)
	}
}

func (p *printSink) Agent(a usage.AgentState) {
	fmt.Fprintf(p.out, "session %s: %s %s context=%d/%d\n", a.Agent, a.Phase, a.Model, a.Context, a.ContextWindow)
}

func showPrices(out io.Writer) int {
	path := appdir.CustomPrices()
	fmt.Fprintf(out, "Bundled price table: v%d (%s)\nCustom prices: %s\n", pricing.Bundled.Version, pricing.Bundled.Updated, path)
	c, err := pricing.LoadCustom(path)
	switch {
	case err != nil:
		fmt.Fprintln(out, "  error:", err)
		return 1
	case c == nil:
		fmt.Fprintln(out, "  none (the file doesn't exist). See docs/plugins.md#custom-prices.")
	default:
		if _, err := pricing.Bundled.With(c); err != nil {
			fmt.Fprintln(out, "  error:", err)
			return 1
		}
		fmt.Fprintf(out, "  %d models, %d agent and %d provider multipliers\n", c.Count(),
			len(c.Multipliers.Sources), len(c.Multipliers.Providers))
	}
	return 0
}
