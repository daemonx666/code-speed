package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/daemonx666/code-speed/internal/alerts"
	"github.com/daemonx666/code-speed/internal/appdir"
	"github.com/daemonx666/code-speed/internal/autostart"
	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/plugin"
	"github.com/daemonx666/code-speed/internal/pricing"
	"github.com/daemonx666/code-speed/internal/status"
	"github.com/daemonx666/code-speed/internal/update"
)

var (
	alertsOnce sync.Once
	checker    *alerts.Checker
)

// Alerts is the notification checker, its state in appdir/alerts.json.
func (s *Service) Alerts() *alerts.Checker {
	alertsOnce.Do(func() { checker = alerts.New(filepath.Join(appdir.Dir(), "alerts.json")) })
	return checker
}

// applyAutostart turns starting at login on or off; it needs the app's
// own binary.
func (s *Service) applyAutostart(on bool) error {
	if !on {
		return autostart.Set(false, "")
	}
	if s.HelperPath == "" {
		return ErrNoHelper
	}
	return autostart.Set(true, s.HelperPath)
}

// Export writes a period's usage to the Downloads folder (or code-speed's
// own, without one) and returns the file's path.
func (s *Service) Export(period, format string) (string, error) {
	rows := s.Monitor().Engine.Export(time.Now().UnixMilli(), period)
	dir := downloads()
	ext := "csv"
	if format == "json" {
		ext = "json"
	}
	path := filepath.Join(dir, fmt.Sprintf("code-speed-%s-%s.%s", period, time.Now().Format("2006-01-02-150405"), ext))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if ext == "json" {
		err = engine.WriteJSON(f, rows)
	} else {
		err = engine.WriteCSV(f, rows)
	}
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.lastExport = path
	s.mu.Unlock()
	return path, nil
}

func downloads() string {
	home, _ := os.UserHomeDir()
	if d := filepath.Join(home, "Downloads"); isDir(d) {
		return d
	}
	os.MkdirAll(appdir.Dir(), 0o700)
	return appdir.Dir()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Open shows one of code-speed's own places in the file manager or the
// browser. Only these targets: never a path or URL from the caller.
func (s *Service) Open(target string) error {
	var path string
	reveal := false
	switch target {
	case "data":
		path = appdir.Dir()
	case "plugins":
		path = plugin.Dir()
	case "export":
		s.mu.Lock()
		path, reveal = s.lastExport, true
		s.mu.Unlock()
	case "releases":
		return openURL(update.ReleasesURL)
	default:
		// a provider's status page, from the fixed list
		if id, ok := strings.CutPrefix(target, "status:"); ok {
			if p, ok := status.Lookup(id); ok {
				return openURL(p.Page)
			}
		}
		if u, ok := strings.CutPrefix(target, "incident:"); ok && s.IncidentLink(u) {
			return openURL(u)
		}
		return fmt.Errorf("nothing to open: %q", target)
	}
	if path == "" {
		return fmt.Errorf("nothing exported yet")
	}
	if !reveal {
		os.MkdirAll(path, 0o700)
	}
	switch runtime.GOOS {
	case "darwin":
		if reveal {
			return exec.Command("open", "-R", path).Start()
		}
		return exec.Command("open", path).Start()
	case "windows":
		if reveal {
			return exec.Command("explorer", "/select,", path).Start()
		}
		return exec.Command("explorer", path).Start()
	}
	if reveal {
		path = filepath.Dir(path)
	}
	return exec.Command("xdg-open", path).Start()
}

func openURL(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	}
	return exec.Command("xdg-open", u).Start()
}

// CustomPrices is custom-prices.json as it is now (empty when none).
func (s *Service) CustomPrices() (*pricing.Custom, error) {
	c, err := pricing.LoadCustom(appdir.CustomPrices())
	if c == nil && err == nil {
		c = &pricing.Custom{}
	}
	return c, err
}

// SaveCustomPrices checks custom prices, writes them and reloads.
func (s *Service) SaveCustomPrices(c *pricing.Custom) error {
	if _, err := pricing.Bundled.With(c); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := appdir.CustomPrices() + ".tmp"
	os.MkdirAll(appdir.Dir(), 0o700)
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if _, err := pricing.LoadCustom(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, appdir.CustomPrices()); err != nil {
		return err
	}
	s.Reload()
	return nil
}

// CheckUpdate asks GitHub for the newest release, when the user allowed
// it, and keeps the answer for Settings.
func (s *Service) CheckUpdate(ctx context.Context) {
	if !s.Settings().UpdateCheck {
		return
	}
	r, err := update.Latest(ctx, s.Version)
	s.mu.Lock()
	if err != nil {
		s.updateErr = err.Error()
		s.mu.Unlock()
		return
	}
	s.latest, s.updateErr = &r, ""
	s.mu.Unlock()
	s.autoInstall(ctx)
}

// Update is the last answer of CheckUpdate.
func (s *Service) Update() (*update.Release, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest, s.updateErr
}
