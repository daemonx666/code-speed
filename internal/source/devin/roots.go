package devin

import (
	"os"
	"path/filepath"
	"runtime"
)

// autoDatabase is the automatic entry's database (METRICS §4.17):
// $CHISEL_SESSION_DB when not empty, else the CLI's XDG data folder on
// macOS and Linux (also on macOS), %APPDATA%\devin\cli on Windows.
func autoDatabase(goos string, getenv func(string) string, home string) string {
	if v := getenv("CHISEL_SESSION_DB"); v != "" {
		return v
	}
	if goos == "windows" {
		base := getenv("APPDATA")
		if base == "" {
			if home == "" {
				return ""
			}
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "devin", "cli", "sessions.db")
	}
	if v := getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "devin", "cli", "sessions.db")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "devin", "cli", "sessions.db")
}

func isRegular(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular()
}

// ResolveLocation is the database of a user location (a folder): its
// sessions.db, else cli/sessions.db, the first that is a regular file
// (never a symbolic link). "" when there is none.
func ResolveLocation(folder string) string {
	for _, p := range []string{
		filepath.Join(folder, "sessions.db"),
		filepath.Join(folder, "cli", "sessions.db"),
	} {
		if isRegular(p) {
			return p
		}
	}
	return ""
}

func defaultDatabases() []string {
	home, _ := os.UserHomeDir()
	if p := autoDatabase(runtime.GOOS, os.Getenv, home); p != "" {
		return []string{p}
	}
	return nil
}
