package qoder

import (
	"os"
	"path/filepath"
	"runtime"
)

// editions are the IDE's data folder names: international, then China.
var editions = []string{"Qoder", "QoderCN"}

// dataDir is where VS Code forks keep their user data folders (METRICS §4.18).
func dataDir(goos string, getenv func(string) string, home string) string {
	switch goos {
	case "windows":
		if v := getenv("APPDATA"); v != "" {
			return v
		}
		return filepath.Join(home, "AppData", "Roaming")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support")
	}
	if v := getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".config")
}

// ideFolders are the edition folders of the IDE.
func ideFolders(home string) []string {
	if home == "" {
		return nil
	}
	data := dataDir(runtime.GOOS, os.Getenv, home)
	var out []string
	for _, e := range editions {
		out = append(out, filepath.Join(data, e))
	}
	return out
}

// ideDatabases are the automatic entry's databases, international first.
func ideDatabases(home string) []string {
	var out []string
	for _, f := range ideFolders(home) {
		out = append(out, filepath.Join(f, "SharedClientCache", "cache", "db", "local.db"))
	}
	return out
}

// cliRoots are the `projects` folders of Qoder CLI's two config folders:
// $QODER_CONFIG_DIR else ~/.qoder, $QODERCN_CONFIG_DIR else ~/.qoder-cn.
func cliRoots(getenv func(string) string, home string) []string {
	var out []string
	for _, c := range []struct{ env, dir string }{{"QODER_CONFIG_DIR", ".qoder"}, {"QODERCN_CONFIG_DIR", ".qoder-cn"}} {
		base := getenv(c.env)
		if base == "" {
			if home == "" {
				continue
			}
			base = filepath.Join(home, c.dir)
		}
		out = append(out, filepath.Join(base, "projects"))
	}
	return out
}
