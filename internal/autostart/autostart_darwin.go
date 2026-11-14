package autostart

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
)

func plistPath() string { return plistOf(label) }

func plistOf(l string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", l+".plist")
}

// dropOld removes the LaunchAgents of the old names. They only ever
// pointed at code-speed, and launchd forgets them at the next login.
func dropOld() {
	for _, l := range oldLabels {
		_ = removeFile(plistOf(l))
	}
}

// HadOld reports whether a LaunchAgent of an old name is still there.
func HadOld() bool {
	for _, l := range oldLabels {
		if _, err := os.Stat(plistOf(l)); err == nil {
			return true
		}
	}
	return false
}

func esc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// A LaunchAgent run at load: launchd starts it at the next login. The app
// bundle's binary is named directly so the agent never waits on Finder.
func enable(exe string) error {
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(exe) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
	return writeFile(plistPath(), []byte(plist))
}

func disable() error { return removeFile(plistPath()) }

func enabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}
