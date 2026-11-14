package hermes

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

type phaseExp struct {
	Phase    string `json:"phase"`
	InFlight *int64 `json:"inFlightStartMs"`
	LastLine *int64 `json:"lastLineMs"`
	Model    string `json:"model"`
}

type respExp struct {
	Key      string `json:"key"`
	StartMs  *int64 `json:"startMs"`
	EndMs    int64  `json:"endMs"`
	Input    int64  `json:"input"`
	Cached   int64  `json:"cachedInput"`
	CW       int64  `json:"cacheWriteInput"`
	CW1h     int64  `json:"cacheWrite1hInput"`
	Output   int64  `json:"output"`
	Reason   int64  `json:"reasoning"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Fast     bool   `json:"fast"`
	Final    bool   `json:"final"`
}

type agentExp struct {
	Model      string    `json:"model"`
	IsSubagent bool      `json:"isSubagent"`
	Phase      string    `json:"phase"`
	InFlight   *int64    `json:"inFlightStartMs"`
	LastLine   int64     `json:"lastLineMs"`
	Responses  []respExp `json:"responses"`
}

type expected struct {
	TimeZone string           `json:"timeZone"`
	Mtimes   map[string]int64 `json:"mtimes"`
	Sets     map[string]struct {
		Agents map[string]agentExp `json:"agents"`
	} `json:"sets"`
	WallTimes []struct {
		TimeZone string `json:"timeZone"`
		Line     string `json:"line"`
		Ms       *int64 `json:"ms"`
	} `json:"wallTimes"`
	Homes []struct {
		Env      map[string]string `json:"env"`
		Platform string            `json:"platform"`
		Home     string            `json:"home"`
		Expected string            `json:"expected"`
	} `json:"homes"`
	Rotation struct {
		TimeZone string            `json:"timeZone"`
		Steps    []json.RawMessage `json:"steps"`
	} `json:"rotation"`
	RuntimeConfigs []struct {
		Name  string `json:"name"`
		Text  string `json:"text"`
		Codex bool   `json:"codexRuntime"`
	} `json:"runtimeConfigs"`
	CodexRuntime struct {
		TimeZone string            `json:"timeZone"`
		Steps    []json.RawMessage `json:"steps"`
	} `json:"codexRuntime"`
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

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no time zone data for %s: %v", name, err)
	}
	return l
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

func setMtime(t *testing.T, path string, ms int64) {
	t.Helper()
	tm := time.UnixMilli(ms)
	if err := os.Chtimes(path, tm, tm); err != nil {
		t.Fatal(err)
	}
}

func poll(t *testing.T, p *poller, s *sourcetest.Sink, d *source.Dedup) {
	t.Helper()
	if err := p.Poll(context.Background(), time.Time{}, time.UnixMilli(1792500000000), d, s); err != nil {
		t.Fatal(err)
	}
}

func ptrEq(a *int64, b int64) bool {
	if a == nil {
		return b == 0
	}
	return *a == b
}

func TestFixtures(t *testing.T) {
	e := load(t)
	root := t.TempDir()
	home := filepath.Join(root, "hermes", "home")
	copyTree(t, "testdata/home", home)
	for rel, ms := range e.Mtimes {
		setMtime(t, filepath.Join(root, filepath.FromSlash(rel)), ms)
	}
	p := &poller{loc: zone(t, e.TimeZone), dirs: func() []string { return logFolders(home) }}
	s := sourcetest.NewSink()
	poll(t, p, s, source.NewDedup())

	for rel, set := range e.Sets {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		for sid, want := range set.Agents {
			id := dir + "#" + sid
			var got []usage.Response
			for _, r := range s.Responses() {
				if r.Agent == id {
					got = append(got, r)
				}
			}
			if len(got) != len(want.Responses) {
				t.Errorf("%s %q: %d responses, want %d", rel, sid, len(got), len(want.Responses))
				continue
			}
			for i, w := range want.Responses {
				g := got[i]
				if g.Key != w.Key || !ptrEq(w.StartMs, g.StartMs) || g.EndMs != w.EndMs ||
					g.Input != w.Input || g.CachedInput != w.Cached || g.CacheWrite != w.CW ||
					g.CacheWrite1h != w.CW1h || g.Output != w.Output || g.Reasoning != w.Reason ||
					g.Model != w.Model || g.Provider != w.Provider || g.Fast != w.Fast || g.Final != w.Final ||
					g.Source != Hermes {
					t.Errorf("%s %q response %d: got %+v want %+v", rel, sid, i, g, w)
				}
			}
			st, ok := s.Agents[id]
			if !ok {
				t.Errorf("%s %q: no agent state", rel, sid)
				continue
			}
			if string(st.Phase) != want.Phase || !ptrEq(want.InFlight, st.InFlightMs) ||
				st.LastLineMs != want.LastLine || st.Model != want.Model || st.Subagent != want.IsSubagent {
				t.Errorf("%s %q state: got %+v want %+v", rel, sid, st, want)
			}
		}
	}
	if n := len(s.Responses()); n != 11 {
		t.Logf("responses: %d", n)
	}
}

func TestWallTimes(t *testing.T) {
	e := load(t)
	for _, c := range e.WallTimes {
		loc := zone(t, c.TimeZone)
		got, ok := parseStamp([]byte(c.Line), loc)
		if c.Ms == nil {
			if ok {
				t.Errorf("%q: parsed %d, want none", c.Line, got)
			}
			continue
		}
		if !ok || got != *c.Ms {
			t.Errorf("%s %q: got %d (%v), want %d", c.TimeZone, c.Line, got, ok, *c.Ms)
		}
	}
}

func TestRuntimeConfigs(t *testing.T) {
	for _, c := range load(t).RuntimeConfigs {
		if got := codexRuntime([]byte(c.Text)); got != c.Codex {
			t.Errorf("%s: got %v want %v", c.Name, got, c.Codex)
		}
	}
}

func TestHomes(t *testing.T) {
	for _, c := range load(t).Homes {
		if c.Platform != "darwin" || filepath.Separator != '/' {
			continue
		}
		t.Setenv("HERMES_HOME", c.Env["HERMES_HOME"])
		if got := homeDir(c.Home); got != c.Expected {
			t.Errorf("%v: got %s want %s", c.Env, got, c.Expected)
		}
	}
}

type step struct {
	Append string   `json:"append"`
	Lines  []string `json:"lines"`
	Text   *string  `json:"text"`
	Mtime  int64    `json:"mtime"`
	Rename []string `json:"rename"`
	Delete string   `json:"delete"`
	Write  string   `json:"write"`
	Size   int      `json:"size"`
	Remove string   `json:"remove"`
	Read   bool     `json:"read"`

	ResponseKeys []string `json:"responseKeys"`
	Agents       map[string]phaseExp
	Sets         map[string]struct {
		Codex  bool                `json:"codexRuntime"`
		Agents map[string]phaseExp `json:"agents"`
	} `json:"sets"`
}

func (s *step) UnmarshalJSON(b []byte) error {
	type plain step
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var raw struct {
		Agents map[string]phaseExp `json:"agents"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*s = step(p)
	s.Agents = raw.Agents
	return nil
}

func checkAgent(t *testing.T, s *sourcetest.Sink, id string, w phaseExp, ctx string) {
	t.Helper()
	st, ok := s.Agents[id]
	if !ok {
		t.Errorf("%s: no state for %s", ctx, id)
		return
	}
	if string(st.Phase) != w.Phase || !ptrEq(w.InFlight, st.InFlightMs) || (w.LastLine != nil && *w.LastLine != st.LastLineMs) ||
		(w.Model != "" && st.Model != w.Model) {
		t.Errorf("%s %s: got %+v want phase=%s inflight=%v last=%v", ctx, id, st, w.Phase, w.InFlight, w.LastLine)
	}
}

func checkKeys(t *testing.T, s *sourcetest.Sink, want []string, ctx string) {
	t.Helper()
	var got []string
	for _, r := range s.Responses() {
		got = append(got, r.Key)
	}
	w := append([]string{}, want...)
	sort.Strings(got)
	sort.Strings(w)
	if strings.Join(got, "\n") != strings.Join(w, "\n") {
		t.Errorf("%s: keys %q want %q", ctx, got, w)
	}
}

func apply(t *testing.T, root string, st step) {
	t.Helper()
	switch {
	case st.Append != "":
		path := filepath.Join(root, filepath.FromSlash(st.Append))
		os.MkdirAll(filepath.Dir(path), 0o755)
		text := ""
		if st.Text != nil {
			text = *st.Text
		}
		for _, l := range st.Lines {
			text += l + "\n"
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text)
		f.Close()
		setMtime(t, path, st.Mtime)
	case len(st.Rename) == 2:
		if err := os.Rename(filepath.Join(root, st.Rename[0]), filepath.Join(root, st.Rename[1])); err != nil {
			t.Fatal(err)
		}
	case st.Delete != "":
		os.Remove(filepath.Join(root, st.Delete))
	case st.Write != "":
		path := filepath.Join(root, filepath.FromSlash(st.Write))
		os.MkdirAll(filepath.Dir(path), 0o755)
		text := *st.Text
		if st.Size > 0 {
			text += strings.Repeat("#", st.Size-len(text))
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		setMtime(t, path, st.Mtime)
	case st.Remove != "":
		os.Remove(filepath.Join(root, filepath.FromSlash(st.Remove)))
	}
}

func TestRotation(t *testing.T) {
	e := load(t)
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	os.MkdirAll(logs, 0o755)
	p := &poller{loc: zone(t, e.Rotation.TimeZone), dirs: func() []string { return []string{logs} }}
	s := sourcetest.NewSink()
	d := source.NewDedup()
	for i, raw := range e.Rotation.Steps {
		var st step
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
		if !st.Read {
			apply(t, logs, st)
			continue
		}
		poll(t, p, s, d)
		ctx := "rotation step " + string(rune('0'+i%10))
		checkKeys(t, s, st.ResponseKeys, ctx)
		for sid, w := range st.Agents {
			checkAgent(t, s, logs+"#"+sid, w, ctx)
		}
	}
}

func TestCodexRuntime(t *testing.T) {
	e := load(t)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "logs"), 0o755)
	os.MkdirAll(filepath.Join(root, "profiles", "p", "logs"), 0o755)
	p := &poller{loc: zone(t, e.CodexRuntime.TimeZone), dirs: func() []string { return logFolders(root) }}
	s := sourcetest.NewSink()
	d := source.NewDedup()
	for i, raw := range e.CodexRuntime.Steps {
		var st step
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
		if !st.Read {
			apply(t, root, st)
			continue
		}
		poll(t, p, s, d)
		ctx := "codex step " + string(rune('a'+i%26))
		checkKeys(t, s, st.ResponseKeys, ctx)
		for rel, set := range st.Sets {
			dir := filepath.Join(root, filepath.FromSlash(rel))
			if got := p.sets[dir].codex; got != set.Codex {
				t.Errorf("%s %s: switch %v want %v", ctx, rel, got, set.Codex)
			}
			for sid, w := range set.Agents {
				checkAgent(t, s, dir+"#"+sid, w, ctx)
			}
		}
	}
}

func TestDiscovery(t *testing.T) {
	home, _ := filepath.Abs("testdata/home")
	got := logFolders(home)
	want := []string{filepath.Join(home, "logs"), filepath.Join(home, "profiles", "work", "logs")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("log folders %q want %q", got, want)
	}
	for _, n := range []string{"agent.log", "agent.log.1", "agent.log.12"} {
		if !isLogName(n) {
			t.Errorf("%s should match", n)
		}
	}
	for _, n := range []string{"agent.log.", "agent.log.x", "agent.log.1.gz", "agent.log.bak", "errors.log"} {
		if isLogName(n) {
			t.Errorf("%s should not match", n)
		}
	}
}
