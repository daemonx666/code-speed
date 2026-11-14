package qoder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

type expResp struct {
	Key          string  `json:"key"`
	AgentID      string  `json:"agentId"`
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
	Fast         bool    `json:"fast"`
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func checkResp(t *testing.T, label string, want expResp, got usage.Response) {
	t.Helper()
	g := expResp{StartMs: &got.StartMs, EndMs: got.EndMs, Input: got.Input, CachedInput: got.CachedInput,
		CacheWrite: got.CacheWrite, CacheWrite1h: got.CacheWrite1h, Output: got.Output, Reasoning: got.Reasoning,
		Final: got.Final, Fast: got.Fast}
	if val(want.StartMs) != val(g.StartMs) || want.EndMs != g.EndMs || want.Input != g.Input ||
		want.CachedInput != g.CachedInput || want.CacheWrite != g.CacheWrite || want.CacheWrite1h != g.CacheWrite1h ||
		want.Output != g.Output || want.Reasoning != g.Reasoning || want.Final != g.Final || want.Fast != g.Fast ||
		str(want.Model) != got.Model || str(want.Provider) != got.Provider {
		t.Errorf("%s %s: want %+v (model %q) got %+v", label, want.Key, want, str(want.Model), got)
	}
}

// ---- Qoder CLI ----

type cliFile struct {
	Project    string    `json:"project"`
	Model      *string   `json:"model"`
	IsSubagent bool      `json:"isSubagent"`
	Phase      string    `json:"phase"`
	InFlightMs *int64    `json:"inFlightStartMs"`
	LastLineMs int64     `json:"lastLineMs"`
	Responses  []expResp `json:"responses"`
}

type cliExpected struct {
	Order  []string           `json:"order"`
	Files  map[string]cliFile `json:"files"`
	Traces []struct {
		File       string `json:"file"`
		AfterLine  int    `json:"afterLine"`
		Phase      string `json:"phase"`
		InFlightMs *int64 `json:"inFlightStartMs"`
	} `json:"traces"`
}

func TestCLIFixtures(t *testing.T) {
	var exp cliExpected
	b, err := os.ReadFile("testdata/cli-expected.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		t.Fatal(err)
	}
	d := source.NewDedup()
	type trace struct {
		line  int
		phase string
		fl    int64
	}
	traces := map[string][]trace{}
	for _, tr := range exp.Traces {
		traces[tr.File] = append(traces[tr.File], trace{tr.AfterLine, tr.Phase, val(tr.InFlightMs)})
	}
	for _, rel := range exp.Order {
		want := exp.Files[rel]
		sink := sourcetest.NewSink()
		p := newCLI(filepath.Join("testdata", rel), d, sink)
		data, err := os.ReadFile(filepath.Join("testdata", rel))
		if err != nil {
			t.Fatal(err)
		}
		pending := traces[rel]
		for i, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			p.Line([]byte(l))
			for _, tr := range pending {
				if tr.line == i+1 {
					st := p.State()
					if string(st.Phase) != tr.phase || st.InFlightMs != tr.fl {
						t.Errorf("%s after line %d: phase %s/%d want %s/%d", rel, tr.line, st.Phase, st.InFlightMs, tr.phase, tr.fl)
					}
				}
			}
		}
		st := p.State()
		if st.Source != QoderCLI || st.Project != want.Project || st.Model != str(want.Model) ||
			st.Subagent != want.IsSubagent || string(st.Phase) != want.Phase || st.InFlightMs != val(want.InFlightMs) ||
			st.LastLineMs != want.LastLineMs {
			t.Errorf("%s: state %+v want %+v", rel, st, want)
		}
		got := sink.Responses()
		if len(got) != len(want.Responses) {
			t.Fatalf("%s: %d responses, want %d", rel, len(got), len(want.Responses))
		}
		for i, w := range want.Responses {
			if got[i].Key != w.Key || got[i].Source != QoderCLI {
				t.Errorf("%s: key %s want %s", rel, got[i].Key, w.Key)
			}
			checkResp(t, rel, w, got[i])
		}
	}
}

func TestCLIRoots(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	got := cliRoots(get, "/h")
	if got[0] != filepath.FromSlash("/h/.qoder/projects") || got[1] != filepath.FromSlash("/h/.qoder-cn/projects") {
		t.Errorf("default roots %v", got)
	}
	env["QODER_CONFIG_DIR"] = "/srv/q"
	env["QODERCN_CONFIG_DIR"] = "/srv/cn"
	got = cliRoots(get, "/h")
	if got[0] != filepath.FromSlash("/srv/q/projects") || got[1] != filepath.FromSlash("/srv/cn/projects") {
		t.Errorf("env roots %v", got)
	}
}

func TestDataDir(t *testing.T) {
	none := func(string) string { return "" }
	if g := dataDir("darwin", none, "/Users/dev"); g != filepath.FromSlash("/Users/dev/Library/Application Support") {
		t.Error(g)
	}
	if g := dataDir("linux", none, "/home/dev"); g != filepath.FromSlash("/home/dev/.config") {
		t.Error(g)
	}
	if g := dataDir("linux", func(string) string { return "/x" }, "/home/dev"); g != "/x" {
		t.Error(g)
	}
	if g := dataDir("windows", func(string) string { return `C:\A` }, `C:\Users\dev`); g != `C:\A` {
		t.Error(g)
	}
}

func TestRegistered(t *testing.T) {
	for _, c := range []struct {
		src          usage.Source
		label, color string
	}{{Qoder, "Qoder", "#8b5cf6"}, {QoderCLI, "Qoder CLI", "#7c3aed"}} {
		s, ok := source.Lookup(c.src)
		if !ok || s.Label != c.label || s.Color != c.color {
			t.Errorf("%s: %+v", c.src, s)
		}
	}
	if s, _ := source.Lookup(Qoder); s.Poller == nil || s.Every != 2*time.Second {
		t.Error("Qoder poller")
	}
}

// ---- Qoder IDE ----

type ideAgent struct {
	Project    string    `json:"project"`
	CwdPath    *string   `json:"cwdPath"`
	Model      *string   `json:"model"`
	IsSubagent bool      `json:"isSubagent"`
	Phase      string    `json:"phase"`
	InFlightMs *int64    `json:"inFlightStartMs"`
	LastLineMs int64     `json:"lastLineMs"`
	Responses  []expResp `json:"responses"`
}

type ideScenario struct {
	Databases    []string            `json:"databases"`
	TodayStartMs int64               `json:"todayStartMs"`
	Agents       map[string]ideAgent `json:"agents"`
}

func runIDE(t *testing.T, sc ideScenario, since time.Time) (*sourcetest.Sink, string) {
	t.Helper()
	root, _ := filepath.Abs("testdata")
	var paths []string
	for _, d := range sc.Databases {
		paths = append(paths, filepath.Join(root, d))
	}
	p := &poller{paths: func() []string { return paths }}
	sink := sourcetest.NewSink()
	if err := p.Poll(context.Background(), since, since, source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	return sink, root + "/"
}

func TestIDEFixtures(t *testing.T) {
	var exp struct {
		Scenarios map[string]ideScenario `json:"scenarios"`
	}
	b, err := os.ReadFile("testdata/qoder-expected.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		t.Fatal(err)
	}
	for name, sc := range exp.Scenarios {
		t.Run(name, func(t *testing.T) {
			since := time.UnixMilli(0).Add(-24 * 365 * time.Hour) // everything
			if sc.TodayStartMs != 0 {
				since = time.UnixMilli(sc.TodayStartMs)
			}
			sink, prefix := runIDE(t, sc, since)
			wantResp := map[string]expResp{}
			for id, a := range sc.Agents {
				full := prefix + id
				st, ok := sink.Agents[full]
				if !ok {
					t.Errorf("agent %s missing", id)
					continue
				}
				if st.Source != Qoder || st.Project != a.Project || st.Model != str(a.Model) ||
					st.Subagent != a.IsSubagent || string(st.Phase) != a.Phase || st.InFlightMs != val(a.InFlightMs) ||
					st.LastLineMs != a.LastLineMs {
					t.Errorf("agent %s: got %+v want %+v", id, st, a)
				}
				for _, r := range a.Responses {
					wantResp[r.Key] = r
					if r.AgentID != id {
						t.Errorf("fixture agent id %s != %s", r.AgentID, id)
					}
				}
			}
			if len(sink.Agents) != len(sc.Agents) {
				var got []string
				for k := range sink.Agents {
					got = append(got, strings.TrimPrefix(k, prefix))
				}
				sort.Strings(got)
				t.Errorf("agents %v", got)
			}
			if len(sink.Resp) != len(wantResp) {
				t.Errorf("%d responses, want %d", len(sink.Resp), len(wantResp))
			}
			for k, w := range wantResp {
				got, ok := sink.Resp[k]
				if !ok {
					t.Errorf("response %s missing", k)
					continue
				}
				if got.Agent != prefix+w.AgentID {
					t.Errorf("%s agent %s", k, got.Agent)
				}
				checkResp(t, name, w, got)
			}
			// reading never creates side files
			var created []string
			filepath.Walk("testdata", func(p string, _ os.FileInfo, _ error) error {
				if strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm") || strings.HasSuffix(p, "-journal") {
					created = append(created, p)
				}
				return nil
			})
			if len(created) > 0 {
				t.Errorf("side files created: %v", created)
			}
		})
	}
}

func TestProjectPath(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b":                         "/a/b",
		"file:///Users/dev/a%20b":      "/Users/dev/a b",
		"file:///c%3A/work/app":        "c:/work/app",
		"C:\\Users\\dev\\work\\ledger": "C:\\Users\\dev\\work\\ledger",
		"  ":                           "",
	} {
		if got := projectPath(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
	if lastComponent("C:\\Users\\dev\\work\\ledger") != "ledger" || lastComponent("c:/work/app") != "app" {
		t.Error("lastComponent")
	}
}

func TestIDESecondPollReadsNothingNew(t *testing.T) {
	root, _ := filepath.Abs("testdata")
	db := filepath.Join(root, "qoder/home/Library/Application Support/Qoder/SharedClientCache/cache/db/local.db")
	p := &poller{paths: func() []string { return []string{db} }}
	sink := sourcetest.NewSink()
	d := source.NewDedup()
	old := time.UnixMilli(0)
	if err := p.Poll(context.Background(), old, old, d, sink); err != nil {
		t.Fatal(err)
	}
	n := len(sink.Resp)
	if n == 0 {
		t.Fatal("no responses")
	}
	if err := p.Poll(context.Background(), old, old, d, sink); err != nil || len(sink.Resp) != n {
		t.Fatalf("second poll: %v, %d responses", err, len(sink.Resp))
	}
}
