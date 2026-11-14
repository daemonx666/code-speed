// Package autostart starts code-speed when the user logs in, the way each
// system expects of a user app, with no admin rights:
//
//	macOS    ~/Library/LaunchAgents/io.github.daemonx666.code-speed.plist
//	Windows  HKCU\Software\Microsoft\Windows\CurrentVersion\Run, "code-speed"
//	Linux    $XDG_CONFIG_HOME/autostart/code-speed.desktop
package autostart

import (
	"os"
	"path/filepath"
)

const label = "io.github.daemonx666.code-speed"

// oldLabels are the login items earlier builds wrote under the project's
// previous name; they are removed whenever the login item is set.
var oldLabels = []string{"io.github.felix-xqs.code-speed"}

// Set turns starting at login on (running exe) or off.
func Set(on bool, exe string) error {
	dropOld()
	if !on {
		return disable()
	}
	return enable(exe)
}

// Enabled reports whether starting at login is set up.
func Enabled() bool { return enabled() }

func writeFile(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
