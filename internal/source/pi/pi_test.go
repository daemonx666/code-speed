package pi

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

type expResp struct {
	Key          string  `json:"key"`
	StartMs      *int64  `json:"startMs"`
	EndMs        int64   `json:"endMs"`
	Input        int64   `json:"input"`
	CachedInput  int64   `json:"cachedInput"`
	CacheWrite   int64   `json:"cacheWriteInput"`
	CacheWrite1h int64   `json:"cacheWrite1hInput"`
	Output       int64   `json:"output"`
	Reasoning    int64   `json:"reasoning"`
	Model        *string `json:"model"`
	Provider     *string `json:"provider"`
	Final        bool    `json:"final"`
}

type expFile struct {
	Source     string    `json:"source"`
	Project    string    `json:"project"`
	Model      *string   `json:"model"`
	IsSubagent bool      `json:"isSubagent"`
	Phase      string    `json:"phase"`
	InFlightMs *int64    `json:"inFlightStartMs"`
	LastLineMs int64     `json:"lastLineMs"`
	Responses  []expResp `json:"responses"`
}

type trace struct {
	File       string `json:"file"`
	AfterLine  int    `json:"afterLine"`
	Phase      string `json:"phase"`
	InFlightMs *int64 `json:"inFlightStartMs"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func TestFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	var order []string
	json.Unmarshal(all["_order"], &order)
	var traces map[string][]trace
	json.Unmarshal(all["_traces"], &traces)
	tr := map[string][]trace{}
	for _, l := range traces {
		for _, x := range l {
			tr[x.File] = append(tr[x.File], x)
		}
	}

	dedup := source.NewDedup()
	for _, rel := range order {
		var exp expFile
		if err := json.Unmarshal(all[rel], &exp); err != nil {
			t.Fatal(err)
		}
		t.Run(rel, func(t *testing.T) {
			sink := sourcetest.NewSink()
			path := filepath.Join("testdata", filepath.FromSlash(rel))
			var p source.Parser
			if exp.Source == "omp" {
				p = newParser(true, path, dedup, sink)
			} else {
				p = newParser(false, path, dedup, sink)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(nil, 1<<20)
			n := 0
			for sc.Scan() {
				n++
				p.Line([]byte(strings.TrimRight(sc.Text(), "\r")))
				for _, x := range tr[rel] {
					if x.AfterLine != n {
						continue
					}
					s := p.State()
					if string(s.Phase) != x.Phase || s.InFlightMs != val(x.InFlightMs) {
						t.Errorf("after line %d: phase %s/%d, want %s/%d", n, s.Phase, s.InFlightMs, x.Phase, val(x.InFlightMs))
					}
				}
			}
			s := p.State()
			if string(s.Source) != exp.Source || s.Project != exp.Project || s.Model != str(exp.Model) ||
				s.Subagent != exp.IsSubagent || string(s.Phase) != exp.Phase || s.InFlightMs != val(exp.InFlightMs) ||
				s.LastLineMs != exp.LastLineMs {
				t.Errorf("state = %+v, want %+v", s, exp)
			}
			got := sink.Responses()
			if len(got) != len(exp.Responses) {
				t.Fatalf("%d responses, want %d: %+v", len(got), len(exp.Responses), got)
			}
			for i, w := range exp.Responses {
				g := got[i]
				want := usage.Response{Key: w.Key, Source: usage.Source(exp.Source), Agent: path, Project: exp.Project,
					Model: str(w.Model), Provider: str(w.Provider), StartMs: val(w.StartMs), EndMs: w.EndMs,
					Tokens: usage.Tokens{Input: w.Input, CachedInput: w.CachedInput, CacheWrite: w.CacheWrite,
						CacheWrite1h: w.CacheWrite1h, Output: w.Output, Reasoning: w.Reasoning}, Final: w.Final}
				if g != want {
					t.Errorf("response %d:\n got %+v\nwant %+v", i, g, want)
				}
			}
		})
	}
}

func TestSpecs(t *testing.T) {
	for _, c := range []struct {
		src          usage.Source
		label, color string
	}{{Pi, "Pi", "#8b5cf6"}, {Omp, "Oh My Pi", "#c084fc"}} {
		s, ok := source.Lookup(c.src)
		if !ok || s.Label != c.label || s.Color != c.color {
			t.Errorf("%s: %+v", c.src, s)
		}
	}
}

func TestRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("PI_CONFIG_DIR", "")
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", "")
	t.Setenv("XDG_DATA_HOME", "")
	custom := filepath.Join(home, "custom")
	os.MkdirAll(custom, 0o755)
	os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o755)
	os.WriteFile(filepath.Join(home, ".pi", "agent", "settings.json"), []byte(`{"sessionDir":"~/custom"}`), 0o644)
	os.MkdirAll(filepath.Join(home, ".omp", "agent", "sessions"), 0o755)
	os.MkdirAll(filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions"), 0o755)

	pr := piRoots(home)
	if len(pr) != 2 || pr[0] != filepath.Join(home, ".pi", "agent", "sessions") || pr[1] != custom {
		t.Errorf("pi roots %v", pr)
	}
	or := ompRoots(home)
	if len(or) != 2 || or[0] != filepath.Join(home, ".omp", "agent", "sessions") ||
		or[1] != filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions") {
		t.Errorf("omp roots %v", or)
	}

	// omp pointed at Pi's folder is Pi's
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, ".pi", "agent"))
	os.MkdirAll(filepath.Join(home, ".pi", "agent", "sessions"), 0o755)
	for _, r := range ompRoots(home) {
		if strings.Contains(r, filepath.Join(".pi", "agent")) {
			t.Errorf("omp reads Pi root %s", r)
		}
	}

	// discovery
	m := matcher(Omp, 2)
	setRoots(Omp, []string{"/r"})
	for p, want := range map[string]bool{
		"/r/b/s.jsonl":                    true,
		"/r/s.jsonl":                      false,
		"/r/b/.s.jsonl":                   false,
		"/r/b/.omp-gc-compress-1/s.jsonl": false,
		"/r/b/s.jsonl.bak":                false,
		"/r/b/stem/ScanCallSites.jsonl":   true,
		"/r/b/stem/ScanCallSites.log":     false,
	} {
		if m(p) != want {
			t.Errorf("match %s = %v", p, !want)
		}
	}
}
