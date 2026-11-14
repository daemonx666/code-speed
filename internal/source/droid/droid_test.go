package droid

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

type expResp struct {
	Key          string  `json:"key"`
	StartMs      *int64  `json:"startMs"`
	EndMs        int64   `json:"endMs"`
	FirstMs      *int64  `json:"firstMs"`
	Model        *string `json:"model"`
	Provider     *string `json:"provider"`
	Fast         bool    `json:"fast"`
	Final        bool    `json:"final"`
	Input        int64   `json:"input"`
	CachedInput  int64   `json:"cachedInput"`
	CacheWrite   int64   `json:"cacheWriteInput"`
	CacheWrite1h int64   `json:"cacheWrite1hInput"`
	Output       int64   `json:"output"`
	Reasoning    int64   `json:"reasoning"`
}

type expFile struct {
	Project    string    `json:"project"`
	Model      *string   `json:"model"`
	IsSubagent bool      `json:"isSubagent"`
	Phase      string    `json:"phase"`
	InFlightMs *int64    `json:"inFlightStartMs"`
	LastLineMs int64     `json:"lastLineMs"`
	Responses  []expResp `json:"responses"`
}

type expected struct {
	Discover []string           `json:"discover"`
	Mtimes   map[string]int64   `json:"mtimes"`
	Files    map[string]expFile `json:"files"`
	Sidecars []struct {
		Name     string `json:"name"`
		Text     string `json:"text"`
		Readable bool   `json:"readable"`
		Counters *struct {
			In, CW, CR, Out, Think int64
		} `json:"-"`
		Raw      map[string]int64 `json:"counters"`
		Model    *string          `json:"model"`
		Encoding string           `json:"encoding"`
		Size     int              `json:"size"`
	} `json:"sidecars"`
	Allocations []struct {
		Total   int64   `json:"total"`
		Weights []int64 `json:"weights"`
		Shares  []int64 `json:"shares"`
	} `json:"allocations"`
	Live struct {
		Steps []json.RawMessage `json:"steps"`
	} `json:"live"`
}

func load(t *testing.T) expected {
	t.Helper()
	b, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var e expected
	if err := json.Unmarshal(b, &e); err != nil {
		t.Fatal(err)
	}
	return e
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

func checkResp(t *testing.T, where string, got usage.Response, w expResp) {
	t.Helper()
	g := expResp{
		Key: got.Key, EndMs: got.EndMs, Final: got.Final, Fast: got.Fast,
		Input: got.Input, CachedInput: got.CachedInput, CacheWrite: got.CacheWrite,
		CacheWrite1h: got.CacheWrite1h, Output: got.Output, Reasoning: got.Reasoning,
	}
	if g.Key != w.Key || g.EndMs != w.EndMs || g.Final != w.Final || g.Fast != w.Fast ||
		got.StartMs != val(w.StartMs) || got.FirstMs != val(w.FirstMs) ||
		got.Model != str(w.Model) || got.Provider != str(w.Provider) ||
		g.Input != w.Input || g.CachedInput != w.CachedInput || g.CacheWrite != w.CacheWrite ||
		g.CacheWrite1h != w.CacheWrite1h || g.Output != w.Output || g.Reasoning != w.Reasoning {
		t.Errorf("%s %s:\n got %+v\nwant %+v (start %d model %q provider %q)", where, w.Key, got,
			w, val(w.StartMs), str(w.Model), str(w.Provider))
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if fi.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFixtures(t *testing.T) {
	e := load(t)
	home := t.TempDir()
	sessions := filepath.Join(home, ".factory", "sessions")
	copyTree(t, "testdata/sessions", sessions)
	// a symlink named like a session is not one
	_ = os.Symlink("a1f0c3d2-0b6e-4c1a-9d7e-2f3a4b5c6d70.jsonl",
		filepath.Join(sessions, "-Users-dev-projects-forge", "link.jsonl"))
	for rel, ms := range e.Mtimes {
		tm := time.UnixMilli(ms)
		if err := os.Chtimes(filepath.Join(sessions, rel), tm, tm); err != nil {
			t.Fatal(err)
		}
	}
	sink := sourcetest.NewSink()
	p := &poller{home: home}
	if err := p.Poll(context.Background(), time.Time{}, time.Now(), source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	// discovery
	var found []string
	for a := range sink.Agents {
		rel, _ := filepath.Rel(sessions, a)
		found = append(found, filepath.ToSlash(rel))
	}
	if len(found) != len(e.Discover) {
		t.Errorf("found %d sessions, want %d: %v", len(found), len(e.Discover), found)
	}
	n := 0
	for rel, w := range e.Files {
		abs := filepath.Join(sessions, rel)
		a, ok := sink.Agents[abs]
		if !ok {
			t.Errorf("%s: no agent", rel)
			continue
		}
		if a.Source != Droid || a.Project != w.Project || a.Model != str(w.Model) ||
			a.Subagent != w.IsSubagent || string(a.Phase) != w.Phase ||
			a.InFlightMs != val(w.InFlightMs) || a.LastLineMs != w.LastLineMs {
			t.Errorf("%s: agent %+v, want %+v", rel, a, w)
		}
		var got []usage.Response
		for _, r := range sink.Responses() {
			if r.Agent == abs {
				got = append(got, r)
			}
		}
		if len(got) != len(w.Responses) {
			t.Errorf("%s: %d responses, want %d", rel, len(got), len(w.Responses))
			continue
		}
		for i, wr := range w.Responses {
			n++
			checkResp(t, rel, got[i], wr)
			if got[i].Source != Droid || got[i].Project != w.Project {
				t.Errorf("%s %s: source/project %q %q", rel, wr.Key, got[i].Source, got[i].Project)
			}
		}
	}
	if n == 0 {
		t.Fatal("no responses compared")
	}
	// a second poll changes nothing
	before := len(sink.Responses())
	_ = p.Poll(context.Background(), time.Time{}, time.Now(), source.NewDedup(), sink)
	if len(sink.Responses()) != before {
		t.Error("second poll changed the responses")
	}
}

func TestSidecars(t *testing.T) {
	e := load(t)
	for _, c := range e.Sidecars {
		b := []byte(c.Text)
		if c.Encoding == "latin-1" {
			b = nil
			for _, r := range c.Text {
				b = append(b, byte(r))
			}
		}
		if c.Size > len(b) {
			b = append(b, []byte(strings.Repeat(" ", c.Size-len(b)))...)
		}
		path := filepath.Join(t.TempDir(), "s.settings.json")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		sc, st := readSidecar(path)
		if (st == sideOK) != c.Readable {
			t.Errorf("%s: readable %v, want %v", c.Name, st == sideOK, c.Readable)
			continue
		}
		if !c.Readable {
			continue
		}
		if sc.model != str(c.Model) {
			t.Errorf("%s: model %q, want %q", c.Name, sc.model, str(c.Model))
		}
		if sc.has != (c.Raw != nil) {
			t.Errorf("%s: counters %v, want %v", c.Name, sc.has, c.Raw != nil)
		}
		if c.Raw != nil {
			want := counters{c.Raw["inputTokens"], c.Raw["cacheCreationTokens"], c.Raw["cacheReadTokens"],
				c.Raw["outputTokens"], c.Raw["thinkingTokens"]}
			if sc.counters != want {
				t.Errorf("%s: counters %v, want %v", c.Name, sc.counters, want)
			}
		}
	}
	// a symlink is not followed
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "real.json"), []byte(`{}`), 0o644)
	if err := os.Symlink("real.json", filepath.Join(dir, "x.settings.json")); err == nil {
		if _, st := readSidecar(filepath.Join(dir, "x.settings.json")); st != sideUnreadable {
			t.Error("symlinked sidecar was read")
		}
	}
}

func TestAllocation(t *testing.T) {
	for _, c := range load(t).Allocations {
		got := split(c.Total, c.Weights)
		for i := range got {
			if got[i] != c.Shares[i] {
				t.Errorf("split(%d, %v) = %v, want %v", c.Total, c.Weights, got, c.Shares)
				break
			}
		}
	}
}

func TestParseTime(t *testing.T) {
	ok := map[string]int64{
		"2026-10-20T13:00:03.000Z":      1792501203000,
		"2026-10-20t13:00:03z":          1792501203000,
		"2026-10-20 15:00:03.5+02:00":   1792501203500,
		"2026-10-20T15:00:03.1239+0200": 1792501203123,
		"1969-12-31T23:59:59Z":          -1000,
		"2024-02-29T00:00:00Z":          1709164800000,
	}
	for s, w := range ok {
		if g, k := parseTime(s); !k || g != w {
			t.Errorf("parseTime(%q) = %d, %v; want %d", s, g, k, w)
		}
	}
	for _, s := range []string{"2026-10-20T13:00:03", "2026-10-20T24:00:00Z", "2026-02-29T00:00:00Z",
		"2026-10-20T13:00:03+08", " 2026-10-20T13:00:03Z", "2026-10-20T13:00:03.Z", "2026-10-20T13:00:60Z", ""} {
		if _, k := parseTime(s); k {
			t.Errorf("parseTime(%q) accepted", s)
		}
	}
}

// liveStep is one step of the scripted read cycles.
type liveStep struct {
	Append    string            `json:"append"`
	Rewrite   string            `json:"rewrite"`
	Sidecar   string            `json:"sidecar"`
	Open      string            `json:"open"`
	Read      string            `json:"read"`
	AtEnd     bool              `json:"atEnd"`
	Lines     []json.RawMessage `json:"lines"`
	Doc       json.RawMessage   `json:"doc"`
	Text      *string           `json:"text"`
	Mtime     int64             `json:"mtime"`
	Responses []expResp         `json:"responses"`
	Pending   map[string]int64  `json:"pending"`
	Phase     *string           `json:"phase"`
}

func TestLive(t *testing.T) {
	e := load(t)
	home := t.TempDir()
	sink := sourcetest.NewSink()
	dedup := source.NewDedup()
	sess := map[string]*session{}
	join := func(rel string) string { return filepath.Join(home, filepath.FromSlash(rel)) }
	write := func(path string, lines []json.RawMessage, appendTo bool) {
		var sb strings.Builder
		for _, l := range lines {
			var c bytes.Buffer
			if err := json.Compact(&c, l); err != nil {
				t.Fatal(err)
			}
			sb.Write(c.Bytes())
			sb.WriteByte('\n')
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if appendTo {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			f.WriteString(sb.String())
			f.Close()
			return
		}
		tmp := path + ".new"
		if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}
	for i, raw := range e.Live.Steps {
		var s liveStep
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		switch {
		case s.Append != "":
			write(join(s.Append), s.Lines, true)
		case s.Rewrite != "":
			write(join(s.Rewrite), s.Lines, false)
		case s.Sidecar != "":
			path := sidecarPath(join(s.Sidecar))
			text := string(s.Doc)
			if s.Text != nil {
				text = *s.Text
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			tm := time.UnixMilli(s.Mtime)
			os.Chtimes(path, tm, tm)
		case s.Open != "":
			ss := newSession(join(s.Open), dedup, sink)
			ss.atEnd = s.AtEnd
			sess[s.Open] = ss
		case s.Read != "":
			ss := sess[s.Read]
			ss.cycle()
			var got []usage.Response
			for _, r := range sink.Responses() {
				if r.Agent == ss.path {
					got = append(got, r)
				}
			}
			if len(got) != len(s.Responses) {
				t.Fatalf("step %d: %d responses, want %d", i, len(got), len(s.Responses))
			}
			for k, w := range s.Responses {
				checkResp(t, "step "+string(rune('0'+i%10)), got[k], w)
			}
			wp := counters{s.Pending["inputTokens"], s.Pending["cacheCreationTokens"], s.Pending["cacheReadTokens"],
				s.Pending["outputTokens"], s.Pending["thinkingTokens"]}
			if ss.P != wp {
				t.Errorf("step %d: pending %v, want %v", i, ss.P, wp)
			}
			if s.Phase != nil && string(ss.phase) != *s.Phase {
				t.Errorf("step %d: phase %s, want %s", i, ss.phase, *s.Phase)
			}
		}
	}
}

func TestSpec(t *testing.T) {
	s, ok := source.Lookup(Droid)
	if !ok || s.Label != "Factory Droid" || s.Color != "#f97316" || s.Poller == nil ||
		s.Every != 2*time.Second || s.Detect == nil || s.Roots != nil {
		t.Fatalf("spec: %+v", s)
	}
	home := t.TempDir()
	if s.Detect(home) {
		t.Error("detected without ~/.factory")
	}
	os.Mkdir(filepath.Join(home, ".factory"), 0o755)
	if !s.Detect(home) {
		t.Error("not detected with ~/.factory")
	}
}

// A session older than the startup window starts at its end: only later
// growth counts, and the new call gets exactly it.
func TestPollWindow(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".factory", "sessions", "-Users-dev-p")
	os.MkdirAll(dir, 0o755)
	path := filepath.Join(dir, "5a1d7c3e-0f4b-4e2a-8c6d-9b0e1f2a3b4c.jsonl")
	line := func(id, ts, role string) string {
		return `{"type":"message","id":"` + id + `","timestamp":"` + ts + `","message":{"role":"` + role + `","content":[],"modelId":"m"}}` + "\n"
	}
	os.WriteFile(path, []byte(line("a", "2026-10-20T13:00:00.000Z", "assistant")), 0o644)
	os.WriteFile(sidecarPath(path), []byte(`{"tokenUsage":{"inputTokens":900,"outputTokens":90}}`), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	os.Chtimes(sidecarPath(path), old, old)
	sink := sourcetest.NewSink()
	p := &poller{home: home}
	d := source.NewDedup()
	since := time.Now().Add(-time.Minute)
	p.Poll(context.Background(), since, time.Now(), d, sink)
	if n := len(sink.Responses()); n != 0 {
		t.Fatalf("%d responses from a session before the window", n)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(line("u", "2026-10-20T13:01:00.000Z", "user") + line("b", "2026-10-20T13:01:02.000Z", "assistant"))
	f.Close()
	os.WriteFile(sidecarPath(path), []byte(`{"tokenUsage":{"inputTokens":1000,"outputTokens":100}}`), 0o644)
	p.Poll(context.Background(), since, time.Now(), d, sink)
	rs := sink.Responses()
	if len(rs) != 1 || rs[0].Key != "droid:b" || rs[0].Input != 100 || rs[0].Output != 10 ||
		rs[0].StartMs == 0 || rs[0].Model != "m" {
		t.Fatalf("responses: %+v", rs)
	}
}
