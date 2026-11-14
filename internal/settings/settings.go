// Package settings is what the user chose: language, what the menu bar
// shows and which agents are read. Kept as JSON in appdir.Settings().
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/status"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Choices for the fields below.
const (
	LangAuto = "auto"
	LangEN   = "en"
	LangZH   = "zh-CN"

	MenuSpeed     = "speed"      // live tok/s while an agent writes
	MenuSpeedCost = "speed-cost" // live tok/s and today's cost
	MenuCost      = "cost"       // today's cost
	MenuIcon      = "icon"       // the icon alone

	NumbersMetric = "metric" // 12.3K, 4.56M, 1.2B
	NumbersCJK    = "cjk"    // 1.23万, 4.56亿

	ThemeAuto  = "auto"
	ThemeLight = "light"
	ThemeDark  = "dark"
)

// Settings is the user's choices.
type Settings struct {
	Language string `json:"language"`
	MenuBar  string `json:"menuBar"`
	Theme    string `json:"theme"`
	Numbers  string `json:"numbers"`
	// Sources turns agents on or off; an agent not listed keeps its
	// default: local ones on, online ones off.
	Sources map[usage.Source]bool `json:"sources,omitempty"`
	// ClaudeQuota installs the status-line bridge that reads Claude
	// Code's subscription limits (claudebridge). Off by default: it edits
	// Claude Code's settings file.
	ClaudeQuota bool `json:"claudeQuota,omitempty"`
	// Subscriptions is what each agent's plan costs a month, in USD, to
	// set its API-price cost against. 0 or absent: none.
	Subscriptions map[usage.Source]float64 `json:"subscriptions,omitempty"`

	Alerts   Alerts   `json:"alerts"`
	Budget   Budget   `json:"budget"`
	Currency Currency `json:"currency"`
	// LaunchAtLogin starts the app when the user logs in.
	LaunchAtLogin bool `json:"launchAtLogin,omitempty"`
	// Shortcut opens the panel from anywhere ("CmdOrCtrl+Shift+K"); "" none.
	Shortcut string `json:"shortcut,omitempty"`
	// UpdateCheck asks GitHub once a day whether a newer release exists.
	// Off by default: it is a network request.
	UpdateCheck bool `json:"updateCheck,omitempty"`
	// PriceSync fetches a newer price table once a day (Agent Speed's,
	// from GitHub). Off by default: it is a network request.
	PriceSync bool `json:"priceSync,omitempty"`

	// Order lists the agents the panel shows first, in this order; the
	// rest follow by name.
	Order []usage.Source `json:"order,omitempty"`
	// Hidden agents are left out of the overview's lists (live sessions,
	// quotas, today's rows) but still read and counted in every total.
	Hidden []usage.Source `json:"hidden,omitempty"`

	// Paths are more log folders per agent; Homes more home folders, each
	// looked in for every agent the way the user's own is. WSL adds the
	// homes inside the WSL distributions (Windows).
	Paths map[usage.Source][]string `json:"paths,omitempty"`
	Homes []string                  `json:"homes,omitempty"`
	WSL   bool                      `json:"wsl,omitempty"`

	// StatusCheck reads the providers' public status pages (the same file
	// for everyone, nothing about the user) every 10 minutes. On by
	// default; the links show either way.
	StatusCheck bool `json:"statusCheck"`
	// StatusProviders are the providers the overview lists when
	// StatusCustom; otherwise those of the agents in use.
	StatusProviders []string `json:"statusProviders,omitempty"`
	StatusCustom    bool     `json:"statusCustom,omitempty"`

	// Orb is the small always-on-top speed window.
	Orb Orb `json:"orb"`
	// AutoUpdate installs a newer release as soon as the update check
	// finds one, and restarts.
	AutoUpdate bool `json:"autoUpdate,omitempty"`
}

// Orb is the floating speed window: shown or not, and where the user left
// it (screen points; Placed false: not moved yet).
type Orb struct {
	On     bool `json:"on,omitempty"`
	X      int  `json:"x,omitempty"`
	Y      int  `json:"y,omitempty"`
	Placed bool `json:"placed,omitempty"`
}

const maxPaths = 16

// cleanPaths keeps absolute folders ("~/" is the home folder), once each.
func cleanPaths(in []string) []string {
	home, _ := os.UserHomeDir()
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
			if home == "" {
				continue
			}
			p = filepath.Join(home, p[1:])
		}
		if p == "" || len(p) > 1024 || !filepath.IsAbs(p) {
			continue
		}
		p = filepath.Clean(p)
		if !seen[p] && len(out) < maxPaths {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func cleanSources(in []usage.Source) []usage.Source {
	var out []usage.Source
	seen := map[usage.Source]bool{}
	for _, s := range in {
		if s != "" && len(s) <= 64 && !seen[s] && len(out) < 64 {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// Alerts are the desktop notifications.
type Alerts struct {
	// Quota: a window's use reaches one of these percents (each once a
	// window); empty: none
	Quota []float64 `json:"quota"`
	// Pace: a window, at its pace so far, runs out before its reset
	Pace bool `json:"pace"`
	// Budget: today's or this month's cost reaches 80 % and 100 % of the budget
	Budget bool `json:"budget"`
	// Peak: a new speed record for the day (at most every 30 minutes)
	Peak bool `json:"peak"`
}

// Budget is what the user means to spend, in USD at API price; 0: none.
type Budget struct {
	Daily   float64 `json:"daily,omitempty"`
	Monthly float64 `json:"monthly,omitempty"`
}

// Currency is how money is shown: Code with Rate units per US dollar.
// Costs are kept in USD; this only changes the display.
type Currency struct {
	Code string  `json:"code"`
	Rate float64 `json:"rate"`
}

// Currencies the panel offers, with their symbols.
var Currencies = map[string]string{"USD": "$", "CNY": "¥", "EUR": "€", "GBP": "£", "JPY": "¥", "KRW": "₩", "HKD": "HK$", "TWD": "NT$", "SGD": "S$", "BRL": "R$", "INR": "₹", "CAD": "C$", "AUD": "A$"}

// Default is a fresh install's settings.
func Default() Settings {
	return Settings{Language: LangAuto, MenuBar: MenuSpeed, Theme: ThemeAuto, Numbers: NumbersMetric,
		Alerts:      Alerts{Quota: []float64{80, 95}, Pace: true, Budget: true},
		StatusCheck: true,
		Currency:    Currency{Code: "USD", Rate: 1}}
}

func oneOf(v, def string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return def
}

// Normalize replaces values this version doesn't know with the defaults.
func (s Settings) Normalize() Settings {
	d := Default()
	s.Language = oneOf(s.Language, d.Language, LangAuto, LangEN, LangZH)
	s.MenuBar = oneOf(s.MenuBar, d.MenuBar, MenuSpeed, MenuSpeedCost, MenuCost, MenuIcon)
	s.Theme = oneOf(s.Theme, d.Theme, ThemeAuto, ThemeLight, ThemeDark)
	s.Numbers = oneOf(s.Numbers, d.Numbers, NumbersMetric, NumbersCJK)
	var quota []float64
	for _, q := range s.Alerts.Quota {
		if q > 0 && q <= 100 && len(quota) < 4 {
			quota = append(quota, q)
		}
	}
	s.Alerts.Quota = quota
	if !(s.Budget.Daily >= 0 && s.Budget.Daily < 1e7) {
		s.Budget.Daily = 0
	}
	if !(s.Budget.Monthly >= 0 && s.Budget.Monthly < 1e8) {
		s.Budget.Monthly = 0
	}
	if _, ok := Currencies[s.Currency.Code]; !ok || !(s.Currency.Rate > 0 && s.Currency.Rate < 1e6) {
		s.Currency = d.Currency
	}
	if s.Currency.Code == "USD" {
		s.Currency.Rate = 1
	}
	if len(s.Shortcut) > 64 {
		s.Shortcut = ""
	}
	s.Order, s.Hidden = cleanSources(s.Order), cleanSources(s.Hidden)
	var ids []string
	for _, id := range s.StatusProviders {
		if _, ok := status.Lookup(id); ok && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	s.StatusProviders = ids
	if !(s.Orb.X > -100000 && s.Orb.X < 100000 && s.Orb.Y > -100000 && s.Orb.Y < 100000) {
		s.Orb.X, s.Orb.Y, s.Orb.Placed = 0, 0, false
	}
	s.Homes = cleanPaths(s.Homes)
	paths := map[usage.Source][]string{}
	for src, ps := range s.Paths {
		if ps = cleanPaths(ps); len(ps) > 0 && len(paths) < 64 {
			paths[src] = ps
		}
	}
	s.Paths = nil
	if len(paths) > 0 {
		s.Paths = paths
	}
	for src, v := range s.Subscriptions {
		if !(v > 0 && v < 1e6) {
			delete(s.Subscriptions, src)
		}
	}
	return s
}

// Enabled reports whether an agent is read.
func (s Settings) Enabled(src usage.Source) bool {
	if on, ok := s.Sources[src]; ok {
		_, known := source.Lookup(src)
		return on && known
	}
	return source.DefaultEnabled(src)
}

// Load reads the settings; a missing file gives the defaults. A file that
// can't be parsed is reported, and the defaults are used without
// overwriting it.
func Load(path string) (Settings, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Default(), err
	}
	s := Default()
	if err := json.Unmarshal(b, &s); err != nil {
		return Default(), err
	}
	return s.Normalize(), nil
}

// Save writes the settings through a temporary file.
func (s Settings) Save(path string) error {
	b, err := json.MarshalIndent(s.Normalize(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Format shows micro-USD in this currency: "$12.30", "¥89", "<$0.01".
func (c Currency) Format(micro int64) string {
	sym, ok := Currencies[c.Code]
	rate := c.Rate
	if !ok || !(rate > 0) {
		sym, rate = "$", 1
	}
	v := float64(micro) / 1e6 * rate
	whole := c.Code == "JPY" || c.Code == "KRW"
	switch {
	case v == 0:
		return sym + "0"
	case !whole && v < 0.01:
		return "<" + sym + "0.01"
	case whole || v >= 100:
		return fmt.Sprintf("%s%.0f", sym, math.Round(v))
	}
	return fmt.Sprintf("%s%.2f", sym, v)
}
