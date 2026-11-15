package gui

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/appdir"
	"github.com/daemonx666/code-speed/internal/claudebridge"
	"github.com/daemonx666/code-speed/internal/cli"
	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/library"
	"github.com/daemonx666/code-speed/internal/plugin"
	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/service"
	"github.com/daemonx666/code-speed/internal/settings"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/status"
	"github.com/daemonx666/code-speed/internal/update"
	"github.com/daemonx666/code-speed/internal/usage"
)

// The panel's page and its API. Nothing here needs the webview, so the
// terminal build can serve the same page to a browser (build/devui).

//go:embed assets
var assets embed.FS

// view is what the panel polls every second.
type view struct {
	Ready      bool   `json:"ready"`
	Version    string `json:"version"`
	Files      int    `json:"files"`
	SeedDone   int64  `json:"seedDone"`
	SeedTotal  int64  `json:"seedTotal"`
	HistoryEnd string `json:"historyEnd,omitempty"` // last day in the history file
	engine.Snapshot
	Sources []engine.SourceInfo `json:"sources"` // labels and colours of every agent
}

// OpenPanel opens the panel (the app sets it; the orb's button calls it).
var OpenPanel func()

// PlaceOrb moves the orb (the app sets it; the orb's page calls it once,
// to put itself in the top right corner of its screen).
var PlaceOrb func(x, y int)

// liveView is what the orb polls: the live rate and today's cost, ready
// to show.
type liveView struct {
	Ready   bool       `json:"ready"`
	Live    float64    `json:"live"`
	Speed   string     `json:"speed"` // "84.2"
	Cost    string     `json:"cost"`  // today's, in the user's currency
	Parts   []livePart `json:"parts"` // each agent's share of the live rate
	Theme   string     `json:"theme"`
	Status  string     `json:"status,omitempty"` // the worst provider indicator, when one is off
	Pending int        `json:"pending,omitempty"`
	Placed  bool       `json:"placed"` // the orb has a remembered place
}

type livePart struct {
	Color string  `json:"color"`
	Share float64 `json:"share"`
}

// worst is the most severe indicator of the shown providers' states.
func worst(ps []service.ProviderStatus) string {
	rank := map[string]int{"minor": 1, "maintenance": 1, "major": 2, "critical": 3}
	out := ""
	for _, p := range ps {
		if rank[p.Indicator] > rank[out] {
			out = p.Indicator
		}
	}
	return out
}

// sourceView is one agent on the Settings page.
type sourceView struct {
	ID       usage.Source `json:"id"`
	Label    string       `json:"label"`
	Color    string       `json:"color"`
	Online   bool         `json:"online"`
	Enabled  bool         `json:"enabled"`
	Detected bool         `json:"detected"`
	Plugin   bool         `json:"plugin"`
	RunsCode bool         `json:"runsCode"` // a command plugin
	// Logs: the agent writes log folders, so more can be added (Paths)
	Logs bool `json:"logs"`
	// Roots are the log folders read now
	Roots []string `json:"roots,omitempty"`
}

// priceTable is the price table in use and its daily sync.
type priceTable struct {
	Version int               `json:"version"`
	Updated string            `json:"updated"`
	Bundled int               `json:"bundled"`
	Sync    pricing.SyncState `json:"sync"`
}

type settingsView struct {
	Settings settings.Settings `json:"settings"`
	Sources  []sourceView      `json:"sources"`
	DataDir  string            `json:"dataDir"`
	// ClaudeBridge is the status-line bridge's state (claudebridge.Status*)
	ClaudeBridge string `json:"claudeBridge"`
	// CanBridge: this build can install the bridge
	CanBridge bool `json:"canBridge"`
	// the user's plugins and custom prices
	PluginDir   string          `json:"pluginDir"`
	Plugins     []plugin.Loaded `json:"plugins"`
	PricesPath  string          `json:"pricesPath"`
	PricesCount int             `json:"pricesCount"`
	PricesError string          `json:"pricesError,omitempty"`
	// the newest release, when update checks are on
	Latest      *update.Release     `json:"latest,omitempty"`
	UpdateState service.UpdateState `json:"updateState"`
	UpdateError string              `json:"updateError,omitempty"`
	Currencies  map[string]string   `json:"currencies"`
	PriceTable  priceTable          `json:"priceTable"`
	// WSL: this system has WSL to look into (Windows)
	WSL     bool   `json:"wsl"`
	Version string `json:"version"`
	Error   string `json:"error,omitempty"`
}

func allSources() []engine.SourceInfo {
	var out []engine.SourceInfo
	for _, s := range source.Specs() {
		out = append(out, engine.SourceInfo{ID: s.Source, Label: s.Label, Color: s.Color})
	}
	return out
}

func settingsOf(s *service.Service) settingsView {
	st := s.Settings()
	v := settingsView{Settings: st, DataDir: appdir.Dir(), Version: s.Version,
		ClaudeBridge: claudebridge.Status(), CanBridge: s.HelperPath != "",
		PluginDir: plugin.Dir(), Plugins: s.Plugins, PricesPath: appdir.CustomPrices(), PricesCount: s.PricesCount}
	if s.PricesError != nil {
		v.PricesError = s.PricesError.Error()
	}
	v.Latest, v.UpdateError = s.Update()
	v.UpdateState = s.UpdateState()
	v.Currencies = settings.Currencies
	v.PriceTable.Version, v.PriceTable.Updated, v.PriceTable.Sync = s.PriceSync()
	v.PriceTable.Bundled = pricing.Bundled.Version
	v.WSL = runtime.GOOS == "windows"
	roots := s.RootsOf()
	if s.SettingsError != nil {
		v.Error = s.SettingsError.Error()
	}
	for _, sp := range source.Specs() {
		v.Sources = append(v.Sources, sourceView{ID: sp.Source, Label: sp.Label, Color: sp.Color,
			Online: sp.Online, Enabled: st.Enabled(sp.Source), Detected: sp.Detected(),
			Plugin: sp.Plugin, RunsCode: sp.Plugin && sp.Poller != nil,
			Logs: sp.Roots != nil, Roots: roots[sp.Source]})
	}
	return v
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// Handler serves the panel and its API:
//
//	GET  /api/snapshot           live speed, today, quotas, sessions
//	GET  /api/period?p=week[&focus=2026-11-12]  usage of a period in every dimension, rows narrowed to a bar
//	GET  /api/sessions[?since=week] today's (or the week's) sessions with their totals
//	GET  /api/session?id=…       one session, request by request
//	GET  /api/activity           the last 53 weeks, day by day, with streaks
//	GET  /api/settings           settings and the agents that can be read
//	POST /api/prices/sync        fetch a newer price table now
//	POST /api/settings           save settings (JSON body)
//	POST /api/locale             the page's own language, for "auto"
//	POST /api/quit               quit the app
//	/api/library/…               the skill and MCP library (library_api.go)
func Handler(s *service.Service, quit func(), locale func(string)) http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(assets, "assets")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/snapshot", func(w http.ResponseWriter, r *http.Request) {
		m := s.Monitor()
		v := view{Ready: s.Ready(), Version: s.Version, Files: m.Files(), Sources: allSources()}
		v.SeedDone, v.SeedTotal = m.SeedProgress()
		v.HistoryEnd = m.Engine.Closed()
		if v.Ready {
			v.Snapshot = m.Engine.Snapshot(time.Now().UnixMilli())
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("/api/period", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		writeJSON(w, s.Monitor().Engine.Period(time.Now().UnixMilli(), q.Get("p"), q.Get("focus")))
	})
	mux.HandleFunc("/api/live", func(w http.ResponseWriter, r *http.Request) {
		st := s.Settings()
		v := liveView{Ready: s.Ready(), Theme: st.Theme, Speed: "—", Cost: st.Currency.Format(0), Placed: st.Orb.Placed}
		if v.Ready {
			snap := s.Monitor().Engine.Snapshot(time.Now().UnixMilli())
			v.Live, v.Cost, v.Pending = snap.Live, st.Currency.Format(snap.Today.CostMicro), snap.Pending
			if snap.Live >= 0.5 {
				v.Speed = strings.TrimSuffix(cli.Speed(snap.Live), " tok/s")
			}
			for src, rate := range snap.LiveBySource {
				if snap.Live > 0 {
					v.Parts = append(v.Parts, livePart{Color: src.Color(), Share: rate / snap.Live})
				}
			}
			sort.Slice(v.Parts, func(i, j int) bool { return v.Parts[i].Share > v.Parts[j].Share })
		}
		if st.StatusCheck {
			v.Status = worst(s.Status())
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("/api/panel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if OpenPanel != nil {
			OpenPanel()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/orb/place", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct{ X, Y int }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if PlaceOrb != nil && body.X > -100000 && body.X < 100000 && body.Y > -100000 && body.Y < 100000 {
			PlaceOrb(body.X, body.Y)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.CheckStatus(r.Context(), true)
		}
		writeJSON(w, map[string]any{"check": s.Settings().StatusCheck, "providers": s.Status(), "all": status.Providers})
	})
	mux.HandleFunc("/api/activity", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.Monitor().Engine.Activity(time.Now().UnixMilli()))
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		if r.URL.Query().Get("since") == "week" {
			since = since.AddDate(0, 0, -6)
		}
		writeJSON(w, s.Monitor().Engine.Sessions(since.UnixMilli()))
	})
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.Session(r.URL.Query().Get("id"))
		if !ok {
			http.Error(w, "no such session", http.StatusNotFound)
			return
		}
		writeJSON(w, d)
	})
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
		case http.MethodPost:
			var st settings.Settings
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&st); err != nil {
				http.Error(w, "bad settings: "+err.Error(), http.StatusBadRequest)
				return
			}
			// where the orb sits is the app's to keep, not the page's
			cur := s.Settings().Orb
			st.Orb.X, st.Orb.Y, st.Orb.Placed = cur.X, cur.Y, cur.Placed
			if err := s.UpdateSettings(st); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, settingsOf(s))
	})
	post := func(w http.ResponseWriter, r *http.Request, v any) bool {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return false
		}
		if v != nil {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
				http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
				return false
			}
		}
		return true
	}
	libraryRoutes(mux, library.New(appdir.Dir()), post)
	mux.HandleFunc("/api/alerts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.Alerts().Recent())
	})
	mux.HandleFunc("/api/export", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Period, Format string }
		if !post(w, r, &body) {
			return
		}
		path, err := s.Export(body.Period, body.Format)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]string{"path": path})
	})
	mux.HandleFunc("/api/open", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Target string }
		if !post(w, r, &body) {
			return
		}
		if err := s.Open(body.Target); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/prices", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var c pricing.Custom
			if !post(w, r, &c) {
				return
			}
			if err := s.SaveCustomPrices(&c); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		c, err := s.CustomPrices()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, c)
	})
	mux.HandleFunc("/api/prices/sync", func(w http.ResponseWriter, r *http.Request) {
		if !post(w, r, nil) {
			return
		}
		changed, err := s.SyncPrices(r.Context(), true)
		v := struct {
			settingsView
			Changed bool   `json:"changed"`
			SyncErr string `json:"syncError,omitempty"`
		}{settingsView: settingsOf(s), Changed: changed}
		if err != nil {
			v.SyncErr = err.Error()
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("/api/update/install", func(w http.ResponseWriter, r *http.Request) {
		if !post(w, r, nil) {
			return
		}
		// it quits the app when done, so it runs on its own
		go func() { _ = s.InstallUpdate(context.Background()) }()
		time.Sleep(100 * time.Millisecond) // long enough to show "downloading"
		writeJSON(w, settingsOf(s))
	})
	mux.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.CheckUpdate(r.Context())
		}
		writeJSON(w, settingsOf(s))
	})
	mux.HandleFunc("/api/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		s.Reload()
		writeJSON(w, settingsOf(s))
	})
	mux.HandleFunc("/api/locale", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Lang string `json:"lang"`
		}
		json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
		if locale != nil {
			locale(body.Lang)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		if quit != nil {
			go quit()
		}
	})
	return mux
}

// Lang resolves the language setting: "auto" follows the page's own
// language (or the system's, before the page said).
func Lang(st settings.Settings, page string) string {
	switch st.Language {
	case settings.LangZH, settings.LangEN:
		return st.Language
	}
	if strings.HasPrefix(strings.ToLower(page), "zh") {
		return settings.LangZH
	}
	return settings.LangEN
}

// TrayText is what the menu bar shows beside the icon.
func TrayText(mode string, s engine.Snapshot, speed func(float64) string, usd func(int64) string) string {
	live := ""
	if s.Live >= 0.5 {
		live = speed(s.Live)
	}
	switch mode {
	case settings.MenuIcon:
		return ""
	case settings.MenuCost:
		return usd(s.Today.CostMicro)
	case settings.MenuSpeedCost:
		if live == "" {
			return usd(s.Today.CostMicro)
		}
		return live + " · " + usd(s.Today.CostMicro)
	}
	return live
}
