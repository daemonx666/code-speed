// Package gemini reads Gemini CLI sessions (<home>/.gemini/tmp/<project>/chats).
// METRICS §4.6 (append-only *.jsonl, v0.39+). The whole-file session-*.json
// of v0.38 and older is rewritten in place and is not read.
package gemini

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Gemini is the source id.
const Gemini usage.Source = "gemini"

func init() {
	source.Register(source.Spec{
		Source: Gemini,
		Label:  "Gemini CLI",
		Color:  "#14b8a6",
		Roots:  roots,
		Match:  match,
		New:    func(path string, d *source.Dedup, sink source.Sink) source.Parser { return newParser(path, d, sink) },
	})
}

func roots(home string) []string {
	if v := os.Getenv("GEMINI_CLI_HOME"); v != "" {
		home = v
	}
	if home == "" {
		return nil
	}
	out := []string{filepath.Join(home, ".gemini", "tmp")}
	if runtime.GOOS == "darwin" {
		out = append(out, filepath.Join(home, ".cache", ".gemini", "tmp"))
	}
	return out
}

// chatsIndex returns the index of the "chats" folder in parts such that the
// path ends <projectDir>/chats/<file> or <projectDir>/chats/<dir>/<file>.
func chatsIndex(parts []string) int {
	n := len(parts)
	if n >= 4 && parts[n-2] == "chats" {
		return n - 2
	}
	if n >= 5 && parts[n-3] == "chats" {
		return n - 3
	}
	return -1
}

func splitPath(path string) []string {
	return strings.Split(filepath.ToSlash(path), "/")
}

// match picks <projectDir>/chats/<file>.jsonl and
// <projectDir>/chats/<parent>/<file>.jsonl below a Gemini tmp folder.
func match(path string) bool {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	parts := splitPath(path)
	i := chatsIndex(parts)
	if i < 0 {
		return false
	}
	if parts[i-1] == "tmp" {
		return false // no project folder
	}
	if source.HasPathDir(path, "tmp") {
		return true
	}
	// a user location that is not named tmp: the project folder says so
	_, err := os.Stat(filepath.Join(filepath.FromSlash(strings.Join(parts[:i], "/")), ".project_root"))
	return err == nil
}

// projectOf resolves the project name of a session file from its project
// directory: .project_root, else <root>/../projects.json.
func projectOf(path string) string {
	parts := splitPath(path)
	i := chatsIndex(parts)
	if i < 0 {
		return ""
	}
	dir := strings.Join(parts[:i], "/")
	dir = filepath.FromSlash(dir)
	if b, err := os.ReadFile(filepath.Join(dir, ".project_root")); err == nil {
		root := strings.TrimSpace(string(b))
		if root != "" {
			root = strings.ReplaceAll(root, "\\", "/")
			root = strings.TrimRight(root, "/")
			return root[strings.LastIndex(root, "/")+1:]
		}
	}
	name := filepath.Base(dir)
	pj := filepath.Join(filepath.Dir(filepath.Dir(dir)), "projects.json")
	if b, err := os.ReadFile(pj); err == nil {
		var found string
		gjson.GetBytes(b, "projects").ForEach(func(k, v gjson.Result) bool {
			if v.Type == gjson.String && v.String() == name {
				found = strings.Clone(k.String())
				return false
			}
			return true
		})
		if found != "" {
			found = strings.ReplaceAll(found, "\\", "/")
			found = strings.TrimRight(found, "/")
			return found[strings.LastIndex(found, "/")+1:]
		}
	}
	return ""
}
