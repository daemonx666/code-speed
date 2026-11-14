// Package appdir is where code-speed keeps its own files: settings and
// the history of finished days. Nothing else is written.
//
// macOS: ~/Library/Application Support/code-speed
// Windows: %AppData%\code-speed
// Linux: $XDG_CONFIG_HOME/code-speed (~/.config/code-speed)
// CODE_SPEED_HOME overrides it (tests, portable installs).
package appdir

import (
	"os"
	"path/filepath"
)

// Dir is code-speed's folder.
func Dir() string {
	if d := os.Getenv("CODE_SPEED_HOME"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "code-speed")
}

// History is the file of finished days.
func History() string { return filepath.Join(Dir(), "history.json") }

// Settings is the settings file.
func Settings() string { return filepath.Join(Dir(), "settings.json") }

// CustomPrices is the user's own prices (pricing.Custom).
func CustomPrices() string { return filepath.Join(Dir(), "custom-prices.json") }
