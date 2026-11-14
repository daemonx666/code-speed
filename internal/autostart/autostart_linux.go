package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

func desktopPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "code-speed.desktop")
}

// quote escapes a path for a desktop entry's Exec line.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`)
	return `"` + r.Replace(s) + `"`
}

func enable(exe string) error {
	entry := "[Desktop Entry]\nType=Application\nName=code-speed\nExec=" + quote(exe) +
		"\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
	return writeFile(desktopPath(), []byte(entry))
}

func disable() error { return removeFile(desktopPath()) }

func enabled() bool {
	_, err := os.Stat(desktopPath())
	return err == nil
}

func dropOld() {}

// HadOld is only ever true on macOS, the one system whose login item
// carried the old name.
func HadOld() bool { return false }
