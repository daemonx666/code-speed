package gemini

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

const (
	orbitMain = "testdata/tmp/orbit/chats/session-2026-10-20T10-30-7f3a2b10.jsonl"
	orbitSub1 = "testdata/tmp/orbit/chats/7f3a2b10-5c1d-4e2f-9a3b-0c4d5e6f7a8b/0b8e6d2c-3f4a-4b5c-8d6e-7f8091a2b3c4.jsonl"
	orbitSub2 = "testdata/tmp/orbit/chats/7f3a2b10-5c1d-4e2f-9a3b-0c4d5e6f7a8b/5d4c3b2a-1f0e-4d9c-8b7a-695847362514.jsonl"
	novaMain  = "testdata/tmp/nova/chats/session-2026-10-20T10-20-1e9c0a77.jsonl"
)

type want struct {
	key                 string
	start, end          int64
	in, cached, out, rs int64
	model               string
}

func lines(t *testing.T, path string) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

func run(t *testing.T, d *source.Dedup, s *sourcetest.Sink, path string) *parser {
	p := newParser(path, d, s)
	for _, l := range lines(t, path) {
		p.Line(l)
	}
	return p
}

func check(t *testing.T, s *sourcetest.Sink, path string, ws []want) {
	t.Helper()
	var got []usage.Response
	for _, r := range s.Responses() {
		if r.Agent == path {
			got = append(got, r)
		}
	}
	if len(got) != len(ws) {
		t.Fatalf("%s: %d responses, want %d", path, len(got), len(ws))
	}
	for i, w := range ws {
		r := got[i]
		if r.Key != w.key || r.StartMs != w.start || r.EndMs != w.end || r.Model != w.model ||
			r.Input != w.in || r.CachedInput != w.cached || r.Output != w.out || r.Reasoning != w.rs ||
			r.CacheWrite != 0 || !r.Final || r.Source != Gemini {
			t.Errorf("%s[%d] = %+v, want %+v", path, i, r, w)
		}
	}
}

func TestFixtures(t *testing.T) {
	d := source.NewDedup()
	s := sourcetest.NewSink()
	pm := run(t, d, s, orbitMain)
	p1 := run(t, d, s, orbitSub1)
	p2 := run(t, d, s, orbitSub2)
	// the legacy .json of the same session is read first and owns its keys
	d.Claim("c0c0c0c0-0000-4000-8000-000000000002", "legacy.json")
	d.Claim("c0c0c0c0-0000-4000-8000-000000000003", "legacy.json")
	pn := run(t, d, s, novaMain)

	const pro = "gemini-3-pro-preview"
	check(t, s, orbitMain, []want{
		{"a0a0a0a0-0000-4000-8000-000000000002", 1792492205000, 1792492211000, 15000, 12288, 500, 410, pro},
		{"a0a0a0a0-0000-4000-8000-000000000004", 1792492212420, 1792492220000, 16296, 15000, 800, 160, pro},
		{"a0a0a0a0-0000-4000-8000-000000000008", 1792492320000, 1792492327000, 17000, 16384, 420, 300, pro},
	})
	const fl = "gemini-3-flash-preview"
	check(t, s, orbitSub1, []want{
		{"b0b0b0b0-0000-4000-8000-000000000002", 1792492327200, 1792492330000, 6000, 0, 220, 150, fl},
	})
	check(t, s, orbitSub2, []want{
		{"e0e0e0e0-0000-4000-8000-000000000002", 1792492327250, 1792492332000, 5000, 4096, 300, 0, fl},
	})
	const f25 = "gemini-2.5-flash"
	check(t, s, novaMain, []want{
		{"d0d0d0d0-0000-4000-8000-000000000004", 1792492800000, 1792492804000, 9000, 8192, 120, 40, f25},
		{"d0d0d0d0-0000-4000-8000-000000000006", 1792492806520, 1792492809000, 9300, 9000, 150, 0, f25},
	})

	for _, c := range []struct {
		p     *parser
		phase usage.Phase
		inFl  int64
		last  int64
		proj  string
		sub   bool
	}{
		{pm, usage.Tools, 0, 1792492327002, "orbit", false},
		{p1, usage.Generating, 1792492330810, 1792492330811, "orbit", true},
		{p2, usage.Idle, 0, 1792492332300, "orbit", true},
		{pn, usage.Idle, 0, 1792492862010, "nova", false},
	} {
		st := c.p.State()
		if st.Phase != c.phase || st.InFlightMs != c.inFl || st.LastLineMs != c.last || st.Project != c.proj || st.Subagent != c.sub {
			t.Errorf("%s state = %+v", st.Agent, st)
		}
	}
	if st := pm.State(); st.Model != pro || st.Context != 17000 {
		t.Errorf("main model/context = %q %d", st.Model, st.Context)
	}
}

func TestTraces(t *testing.T) {
	type tr struct {
		after int
		phase usage.Phase
		inFl  int64
	}
	for path, traces := range map[string][]tr{
		orbitMain: {
			{2, usage.Generating, 1792492205000}, {4, usage.Tools, 0}, {6, usage.Generating, 1792492212400},
			{7, usage.Generating, 1792492212420}, {9, usage.Idle, 0}, {11, usage.Idle, 0}, {12, usage.Idle, 0},
			{14, usage.Generating, 1792492260000}, {16, usage.Generating, 1792492260000}, {17, usage.Idle, 0},
			{19, usage.Idle, 0}, {20, usage.Generating, 1792492290000}, {22, usage.Idle, 0},
			{25, usage.Generating, 1792492310000}, {26, usage.Idle, 0}, {27, usage.Generating, 1792492320000},
			{29, usage.Tools, 0},
		},
		novaMain: {
			{5, usage.Generating, 1792491660000}, {6, usage.Idle, 0}, {10, usage.Tools, 0},
			{12, usage.Generating, 1792492806500}, {13, usage.Generating, 1792492806520},
			{18, usage.Generating, 1792492860000}, {20, usage.Idle, 0},
		},
	} {
		p := newParser(path, source.NewDedup(), sourcetest.NewSink())
		ls := lines(t, path)
		next := 0
		for n, l := range ls {
			p.Line(l)
			if next < len(traces) && traces[next].after == n+1 {
				st := p.State()
				if st.Phase != traces[next].phase || st.InFlightMs != traces[next].inFl {
					t.Errorf("%s after line %d: %v %d, want %v %d", path, n+1, st.Phase, st.InFlightMs, traces[next].phase, traces[next].inFl)
				}
				next++
			}
		}
	}
}

func TestOtherFileOwnsKey(t *testing.T) {
	d := source.NewDedup()
	s := sourcetest.NewSink()
	run(t, d, s, orbitSub1)
	s2 := sourcetest.NewSink()
	p := newParser("copy.jsonl", d, s2)
	for _, l := range lines(t, orbitSub1) {
		p.Line(l)
	}
	if len(s2.Responses()) != 0 {
		t.Fatal("a copied response counted again")
	}
}

func TestMatch(t *testing.T) {
	for p, ok := range map[string]bool{
		"/h/.gemini/tmp/proj/chats/session-2026-10-20T10-30-7f3a2b10.jsonl":     true,
		"/h/.gemini/tmp/proj/chats/parent/child.jsonl":                          true,
		"/h/.gemini/tmp/proj/chats/session-x.json":                              false,
		"/h/.gemini/tmp/proj/chats/session-x.jsonl.tmp-42":                      false,
		"/h/.gemini/tmp/proj/checkpoint-a.jsonl":                                false,
		"/h/.gemini/tmp/proj/chats/a/b/c.jsonl":                                 false,
		"/h/.gemini/tmp/proj/logs.json":                                         false,
		"/h/.claude/projects/proj/chats.jsonl":                                  false,
		"/h/.gemini/tmp/chats/x.jsonl":                                          false,
		"/h/.gemini/tmp/proj/chats/session-x.jsonl.unreadable-1700000000000000": false,
	} {
		if match(filepath.FromSlash(p)) != ok {
			t.Errorf("match(%s) != %v", p, ok)
		}
	}
}

func TestRoots(t *testing.T) {
	t.Setenv("GEMINI_CLI_HOME", "")
	r := roots("/h")
	want := []string{filepath.Join("/h", ".gemini", "tmp")}
	if runtime.GOOS == "darwin" {
		want = append(want, filepath.Join("/h", ".cache", ".gemini", "tmp"))
	}
	if len(r) != len(want) || r[0] != want[0] || (len(r) > 1 && r[1] != want[1]) {
		t.Fatalf("roots = %v, want %v", r, want)
	}
	t.Setenv("GEMINI_CLI_HOME", "/g")
	if r := roots("/h"); r[0] != filepath.Join("/g", ".gemini", "tmp") {
		t.Fatalf("GEMINI_CLI_HOME ignored: %v", r)
	}
}
