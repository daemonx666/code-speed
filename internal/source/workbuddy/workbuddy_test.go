package workbuddy

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
	Key         string  `json:"key"`
	StartMs     *int64  `json:"startMs"`
	EndMs       int64   `json:"endMs"`
	Input       int64   `json:"input"`
	CachedInput int64   `json:"cachedInput"`
	CacheWrite  int64   `json:"cacheWriteInput"`
	Output      int64   `json:"output"`
	Reasoning   int64   `json:"reasoning"`
	Model       *string `json:"model"`
	Provider    *string `json:"provider"`
	Final       bool    `json:"final"`
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
	var traces []trace
	json.Unmarshal(all["_traces"], &traces)

	dedup := source.NewDedup()
	for _, rel := range order {
		var exp expFile
		if err := json.Unmarshal(all[rel], &exp); err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(rel), func(t *testing.T) {
			sink := sourcetest.NewSink()
			path := filepath.Join("testdata", filepath.FromSlash(rel))
			src := usage.Source(exp.Source)
			if src == "workbuddyAi" {
				src = "workbuddy-ai-test"
			}
			p := newParser(src, path, dedup, sink)
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
				for _, x := range traces {
					if x.File != rel || x.AfterLine != n {
						continue
					}
					s := p.State()
					if string(s.Phase) != x.Phase || s.InFlightMs != val(x.InFlightMs) {
						t.Errorf("after line %d: phase %s/%d, want %s/%d", n, s.Phase, s.InFlightMs, x.Phase, val(x.InFlightMs))
					}
				}
			}
			s := p.State()
			if s.Project != exp.Project || s.Model != str(exp.Model) || s.Subagent != exp.IsSubagent ||
				string(s.Phase) != exp.Phase || s.InFlightMs != val(exp.InFlightMs) || s.LastLineMs != exp.LastLineMs {
				t.Errorf("state = %+v, want %+v", s, exp)
			}
			got := sink.Responses()
			if len(got) != len(exp.Responses) {
				t.Fatalf("%d responses, want %d: %+v", len(got), len(exp.Responses), got)
			}
			for i, w := range exp.Responses {
				want := usage.Response{Key: w.Key, Source: src, Agent: path, Project: exp.Project,
					Model: str(w.Model), Provider: str(w.Provider), StartMs: val(w.StartMs), EndMs: w.EndMs,
					Tokens: usage.Tokens{Input: w.Input, CachedInput: w.CachedInput, CacheWrite: w.CacheWrite,
						Output: w.Output, Reasoning: w.Reasoning}, Final: w.Final}
				if got[i] != want {
					t.Errorf("response %d:\n got %+v\nwant %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestSpec(t *testing.T) {
	s, ok := source.Lookup(WorkBuddy)
	if !ok || s.Label != "WorkBuddy" || s.Color != "#06b6d4" || s.Poller != nil {
		t.Errorf("spec %+v", s)
	}
}

func TestRootsAndMatch(t *testing.T) {
	home := t.TempDir()
	r := roots(home, ".workbuddy")
	if len(r) != 1 || r[0] != filepath.Join(home, ".workbuddy", "projects") {
		t.Fatalf("roots %v", r)
	}
	if ai := roots(home, ".workbuddy-ai"); ai[0] != filepath.Join(home, ".workbuddy-ai", "projects") {
		t.Fatalf("ai roots %v", ai)
	}
	if s, ok := source.Lookup(WorkBuddyAI); !ok || s.Label != "WorkBuddy AI" {
		t.Fatalf("ai spec %+v", s)
	}
	aiFile := filepath.Join(home, ".workbuddy-ai", "projects", "slug", "s.jsonl")
	os.MkdirAll(filepath.Dir(aiFile), 0o755)
	os.WriteFile(aiFile, []byte("{}\n"), 0o600)
	if !match(aiFile) {
		t.Fatal("ai transcript not matched")
	}
	if roots("", ".workbuddy") != nil {
		t.Error("roots without home")
	}
	// discovery over the fixture tree, root = the copied data folder
	for _, c := range []struct{ dir, list string }{{"workbuddy-cn", "projects"}, {"workbuddy-ai", "projects"}} {
		td, _ := filepath.Abs("testdata")
		base := filepath.Join(td, c.dir, c.list)
		rootMu.Lock()
		knownRoot = map[string]bool{base: true}
		rootMu.Unlock()
		var got []string
		filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && match(p) {
				rel, _ := filepath.Rel(td, p)
				got = append(got, filepath.ToSlash(rel))
			}
			return nil
		})
		raw, _ := os.ReadFile("testdata/expected.json")
		var all map[string]json.RawMessage
		json.Unmarshal(raw, &all)
		var order []string
		json.Unmarshal(all["_order"], &order)
		want := 0
		for _, o := range order {
			if strings.HasPrefix(o, c.dir+"/") {
				want++
				found := false
				for _, g := range got {
					found = found || g == o
				}
				if !found {
					t.Errorf("%s not discovered", o)
				}
			}
		}
		if len(got) != want {
			t.Errorf("%s: discovered %v, want %d files", c.dir, got, want)
		}
	}
	// the CodeBuddy CLI folder is not a WorkBuddy root
	rootMu.Lock()
	knownRoot = map[string]bool{filepath.Join(home, ".workbuddy", "projects"): true}
	rootMu.Unlock()
	if match(filepath.Join(home, ".codebuddy", "projects", "a", "b.jsonl")) {
		t.Error("codebuddy matched")
	}
}

func TestTimestamps(t *testing.T) {
	for s, want := range map[string]int64{
		"2026-10-21T11:20:00Z":               1792581600000,
		"2026-10-21 19:20:00+08:00":          1792581600000,
		"2026-10-21t11:20:00.5z":             1792581600500,
		"2026-10-21T06:20:00.123456789-0500": 1792581600123,
		"0000-01-01T00:00:00Z":               -62167219200000,
		"9999-12-31T23:59:59.999-23:59":      253402387139999,
	} {
		got, ok := parseTimeString(s)
		if !ok || got != want {
			t.Errorf("%q = %d,%v want %d", s, got, ok, want)
		}
	}
	for _, s := range []string{"2026-10-21T11:20:06", " 2026-10-21T11:20:07Z", "2026-10-21T11:20:08Z ",
		"2026-10-21T11:20:60Z", "2026-10-21T11:20:09+08", "2026-10-21T11:20:10+24:00", "2026-10-21T11:20:11+05:60",
		"2026-10-21T24:00:00Z", "2026-02-29T11:20:12Z", "２０２６-10-21T11:20:13Z", "2026-10-21T11:20:15.Z",
		"2026-10-21T11:20:16,5Z"} {
		if _, ok := parseTimeString(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
}
