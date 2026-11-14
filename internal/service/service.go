// Package service runs code-speed: it holds the settings and the monitor,
// and restarts the monitor when the agents to read change. The app, the
// terminal commands and the panel's API all go through it.
package service

import (
	"context"
	"errors"
	"maps"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/daemonx666/code-speed/internal/appdir"
	"github.com/daemonx666/code-speed/internal/claudebridge"
	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/monitor"
	"github.com/daemonx666/code-speed/internal/plugin"
	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/settings"
	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/update"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Service is one running code-speed.
type Service struct {
	Version string

	mu       sync.Mutex
	settings settings.Settings
	mon      *monitor.Monitor
	cancel   context.CancelFunc
	ready    *atomic.Bool
	// SettingsError is why the settings file couldn't be read, if it couldn't
	SettingsError error
	// OnSettings is called after the settings change (the tray relabels)
	OnSettings func(settings.Settings)
	// Plugins are the user's plugins as last loaded, broken ones included
	Plugins []plugin.Loaded
	// PricesCount and PricesError describe custom-prices.json
	PricesCount int
	PricesError error
	lastExport  string
	latest      *update.Release
	updateErr   string

	// Restart starts cmd (which waits for this process to quit and starts
	// the updated copy) and quits; nil where the app can't (InstallUpdate)
	Restart     func(cmd *exec.Cmd)
	installStep string
	installErr  string

	// HelperPath is the code-speed binary Claude Code's status line runs
	// (`code-speed statusline`); "" in builds that can't be one (devui)
	HelperPath string
}

// ErrNoHelper: this build can't serve as Claude Code's status line.
var ErrNoHelper = errors.New("this build of Code Speed can't be Claude Code's status line")

// applyBridge installs or removes the Claude Code status-line bridge.
func (s *Service) applyBridge(on bool) error {
	if !on {
		if claudebridge.Status() == claudebridge.StatusOff {
			return nil
		}
		return claudebridge.Uninstall()
	}
	if s.HelperPath == "" {
		return ErrNoHelper
	}
	return claudebridge.Install(s.HelperPath)
}

// RepairBridge points the status line at this binary again when the
// bridge is on (the app moved or was updated). Called at start.
func (s *Service) RepairBridge() error {
	if !s.Settings().ClaudeQuota || s.HelperPath == "" {
		return nil
	}
	if st := claudebridge.Status(); st == claudebridge.StatusReplaced || st == claudebridge.StatusUnreadable {
		return nil // the user's change wins; Settings shows it
	}
	return claudebridge.Install(s.HelperPath)
}

// New loads the settings and prepares a monitor; Start runs it.
func New(version string) *Service {
	s := &Service{Version: version}
	s.settings, s.SettingsError = settings.Load(appdir.Settings())
	s.Plugins = plugin.LoadAll()
	s.mon, s.ready = s.newMonitor(), &atomic.Bool{}
	return s
}

// prices is the price table (the bundled one, or a newer one synced)
// with the user's custom prices on top.
func (s *Service) prices() *pricing.Table {
	base := synced().Current(time.Now())
	c, err := pricing.LoadCustom(appdir.CustomPrices())
	s.PricesCount, s.PricesError = c.Count(), err
	if err != nil {
		return base
	}
	t, err := base.With(c)
	if err != nil {
		s.PricesError = err
		return base
	}
	return t
}

func (s *Service) newMonitor() *monitor.Monitor {
	st := s.settings
	source.SetExtra(extraOf(st))
	m := monitor.New(engine.New(s.prices(), time.Local), func(src usage.Source) bool { return st.Enabled(src) })
	m.LoadHistory(appdir.History())
	return m
}

// Start reads the history behind the scenes and then follows the logs.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	m, ready := s.mon, s.ready
	go func() {
		m.Seed(time.Now())
		ready.Store(true)
		m.Run(ctx)
	}()
}

// SeedNow reads the history and returns when done: the terminal commands.
func (s *Service) SeedNow() {
	s.mu.Lock()
	m := s.mon
	s.mu.Unlock()
	m.Seed(time.Now())
	s.ready.Store(true)
}

// Stop stops following the logs and writes the history.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mon.SaveHistory(time.Now())
}

// Monitor is the monitor now running.
func (s *Service) Monitor() *monitor.Monitor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mon
}

// Ready reports whether the history has been read.
func (s *Service) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready.Load()
}

// Settings is the settings now.
func (s *Service) Settings() settings.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// Reload reads the plugins and custom prices again and restarts the
// monitor with them. Responses already in the history file keep the price
// they had.
func (s *Service) Reload() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mon.SaveHistory(time.Now())
	s.Plugins = plugin.LoadAll()
	s.mon, s.ready = s.newMonitor(), &atomic.Bool{}
	running := s.cancel != nil
	s.mu.Unlock()
	if running {
		s.Start()
	}
}

// UpdateSettings saves new settings and applies them. Turning an agent on
// or off restarts the monitor, which rereads the days the history file
// doesn't hold.
func (s *Service) UpdateSettings(n settings.Settings) error {
	n = n.Normalize()
	if n.ClaudeQuota != s.Settings().ClaudeQuota {
		if err := s.applyBridge(n.ClaudeQuota); err != nil {
			return err
		}
	}
	if n.LaunchAtLogin != s.Settings().LaunchAtLogin {
		if err := s.applyAutostart(n.LaunchAtLogin); err != nil {
			return err
		}
	}
	if err := n.Save(appdir.Settings()); err != nil {
		return err
	}
	s.mu.Lock()
	moved := placesChanged(n, s.settings)
	var before source.Extra
	if moved {
		before = extraOf(s.settings)
	}
	restart := moved || !maps.Equal(n.Sources, s.settings.Sources)
	s.settings = n
	running := s.cancel != nil
	if restart {
		if s.cancel != nil {
			s.cancel()
		}
		s.mon.SaveHistory(time.Now())
		s.mon, s.ready = s.newMonitor(), &atomic.Bool{}
		if moved {
			for _, src := range movedSources(n, before, extraOf(n)) {
				s.mon.Engine.Forget(src)
			}
		}
	}
	cb := s.OnSettings
	s.mu.Unlock()
	if restart && running {
		s.Start()
	}
	if cb != nil {
		cb(n)
	}
	return nil
}
