package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/daemonx666/code-speed/internal/usage"
)

// piAgentDir is Pi's agent folder: $PI_CODING_AGENT_DIR, else ~/.pi/agent.
func piAgentDir(home string) string {
	if v := os.Getenv("PI_CODING_AGENT_DIR"); v != "" {
		return v
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// within reports whether p is root or lies inside it.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func piRoots(home string) []string {
	var out []string
	add := func(p string, mustExist bool) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		for _, o := range out {
			if o == p {
				return
			}
		}
		if mustExist && !isDir(p) {
			return
		}
		out = append(out, p)
	}
	agent := piAgentDir(home)
	if agent != "" {
		add(filepath.Join(agent, "sessions"), false)
		// sessionDir of <agent dir>/settings.json
		if b, err := os.ReadFile(filepath.Join(agent, "settings.json")); err == nil {
			var s struct {
				SessionDir any `json:"sessionDir"`
			}
			if json.Unmarshal(b, &s) == nil {
				if d, ok := s.SessionDir.(string); ok && d != "" {
					add(expandHome(d, home), true)
				}
			}
		}
	}
	if v := os.Getenv("PI_CODING_AGENT_SESSION_DIR"); v != "" {
		add(expandHome(v, home), true)
	}
	setRoots(Pi, out)
	return out
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

func ompRoots(home string) []string {
	if home == "" {
		return nil
	}
	cfg := os.Getenv("PI_CONFIG_DIR")
	if cfg == "" {
		cfg = ".omp"
	}
	xdg := ""
	if runtime.GOOS == "darwin" {
		xdg = os.Getenv("XDG_DATA_HOME")
	}
	var cand []string
	switch {
	case os.Getenv("PI_CODING_AGENT_DIR") != "":
		cand = append(cand, filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "sessions"))
	case xdg != "" && isDir(filepath.Join(xdg, "omp")):
		cand = append(cand, filepath.Join(xdg, "omp", "sessions"))
	default:
		cand = append(cand, filepath.Join(home, cfg, "agent", "sessions"))
	}
	// profiles
	names := map[string]bool{}
	list := func(dir string) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() {
				names[e.Name()] = true
			}
		}
	}
	list(filepath.Join(home, cfg, "profiles"))
	if xdg != "" {
		list(filepath.Join(xdg, "omp", "profiles"))
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sortStrings(sorted)
	for _, n := range sorted {
		if xdg != "" && isDir(filepath.Join(xdg, "omp", "profiles", n)) {
			cand = append(cand, filepath.Join(xdg, "omp", "profiles", n, "sessions"))
		} else {
			cand = append(cand, filepath.Join(home, cfg, "profiles", n, "agent", "sessions"))
		}
	}
	// A root that is, or lies inside, Pi's is Pi's.
	piRoot := ""
	if a := piAgentDir(home); a != "" {
		piRoot = filepath.Join(a, "sessions")
	}
	var out []string
	for _, p := range cand {
		p = filepath.Clean(p)
		if piRoot != "" && within(p, piRoot) {
			continue
		}
		dup := false
		for _, o := range out {
			if o == p {
				dup = true
			}
		}
		if !dup && isDir(p) {
			out = append(out, p)
		}
	}
	setRoots(Omp, out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var (
	rootMu    sync.RWMutex
	knownRoot = map[usage.Source][]string{}
)

func setRoots(s usage.Source, r []string) {
	rootMu.Lock()
	knownRoot[s] = append([]string(nil), r...)
	rootMu.Unlock()
}

// matcher picks a source's session files: regular *.jsonl files at least
// minDepth components below a root, no component starting with ".".
func matcher(s usage.Source, minDepth int) func(string) bool {
	return func(path string) bool {
		if !strings.HasSuffix(path, ".jsonl") {
			return false
		}
		rootMu.RLock()
		roots := knownRoot[s]
		rootMu.RUnlock()
		for _, r := range roots {
			if within(path, r) {
				rel, _ := filepath.Rel(r, path)
				parts := strings.Split(filepath.ToSlash(rel), "/")
				if len(parts) < minDepth {
					return false
				}
				for _, p := range parts {
					if strings.HasPrefix(p, ".") {
						return false
					}
				}
				return true
			}
		}
		// root unknown: judge the file and its two nearest folders
		parts := strings.Split(filepath.ToSlash(path), "/")
		for i := max(0, len(parts)-3); i < len(parts); i++ {
			if strings.HasPrefix(parts[i], ".") {
				return false
			}
		}
		return true
	}
}

var stemRe = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d-\d\d-\d\d-\d{3}Z_`)

// ompSubagent: the file lives inside a session's artifacts folder, a folder
// named after the session file (a stem).
func ompSubagent(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Dir(path)), "/")
	for _, p := range parts {
		if stemRe.MatchString(p) {
			return true
		}
	}
	return false
}

// piSubagent: the folder's path plus ".jsonl" is a regular file.
func piSubagent(path string) bool {
	st, err := os.Stat(filepath.Dir(path) + ".jsonl")
	return err == nil && st.Mode().IsRegular()
}
