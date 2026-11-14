package antigravity

import (
	"context"
	"encoding/json"
	"io/fs"
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
	Key         string
	StartMs     *int64
	EndMs       int64
	Input       int64
	CachedInput int64
	Output      int64
	Reasoning   int64
	Model       *string
	Final       bool
}

type expDB struct {
	Unsupported     bool
	Project         *string
	Model           *string
	IsSubagent      bool
	Phase           string
	InFlightStartMs *int64
	LastLineMs      int64
	Responses       []expResp
}

type expected struct {
	Databases map[string]expDB
	Mtimes    map[string]int64
}

// home copies the fixtures to a temp folder playing the home folder.
func home(t *testing.T) (string, expected) {
	t.Helper()
	var exp expected
	b, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		t.Fatal(err)
	}
	h := t.TempDir()
	dst := filepath.Join(h, ".gemini")
	err = filepath.WalkDir("testdata/antigravity", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/antigravity", p)
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, ms := range exp.Mtimes {
		mt := time.UnixMilli(ms)
		if err := os.Chtimes(filepath.Join(h, ".gemini", strings.TrimPrefix(rel, "antigravity/")), mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	return h, exp
}

func TestFixtures(t *testing.T) {
	h, exp := home(t)
	p := &poller{home: h}
	sink := sourcetest.NewSink()
	now := time.UnixMilli(1792577000000)
	if err := p.Poll(context.Background(), time.UnixMilli(0), now, source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	// stale phases fall back to idle after 300 s, so check phases apart
	want, agents := 0, 0
	for rel, e := range exp.Databases {
		path := filepath.Join(h, ".gemini", strings.TrimPrefix(rel, "antigravity/"))
		st, ok := sink.Agents[path]
		if e.Unsupported {
			if ok {
				t.Errorf("%s: unsupported database reported an agent", rel)
			}
			continue
		}
		agents++
		if !ok {
			t.Errorf("%s: no agent", rel)
			continue
		}
		if st.Project != deref(e.Project) || st.Model != deref(e.Model) || st.Subagent != e.IsSubagent || st.LastLineMs != e.LastLineMs {
			t.Errorf("%s: agent %+v, want project %q model %q sub %v last %d", rel, st, deref(e.Project), deref(e.Model), e.IsSubagent, e.LastLineMs)
		}
		raw := p.raw[path]
		if string(raw.Phase) != e.Phase || raw.InFlightMs != ptr(e.InFlightStartMs) {
			t.Errorf("%s: phase %s/%d, want %s/%d", rel, raw.Phase, raw.InFlightMs, e.Phase, ptr(e.InFlightStartMs))
		}
		if st.Phase != usage.Idle {
			t.Errorf("%s: stale phase %s not idle", rel, st.Phase)
		}
		for _, r := range e.Responses {
			want++
			got, ok := sink.Resp[r.Key]
			if !ok {
				t.Errorf("%s: missing response %s", rel, r.Key)
				continue
			}
			if got.Agent != path || got.StartMs != ptr(r.StartMs) || got.EndMs != r.EndMs || got.Model != deref(r.Model) ||
				got.Input != r.Input || got.CachedInput != r.CachedInput || got.Output != r.Output || got.Reasoning != r.Reasoning ||
				got.CacheWrite != 0 || got.Final != r.Final || got.Source != Antigravity {
				t.Errorf("%s: response %s = %+v, want %+v", rel, r.Key, got, r)
			}
		}
	}
	if len(sink.Resp) != want {
		t.Errorf("%d responses, want %d", len(sink.Resp), want)
	}
	if len(sink.Agents) != agents {
		t.Errorf("%d agents, want %d", len(sink.Agents), agents)
	}
	for _, f := range []string{"-wal", "-shm"} {
		filepath.WalkDir(h, func(p string, e fs.DirEntry, err error) error {
			if err == nil && strings.HasSuffix(p, ".db"+f) {
				t.Errorf("created %s", p)
			}
			return nil
		})
	}
}

func TestLivePhase(t *testing.T) {
	h, exp := home(t)
	p := &poller{home: h}
	sink := sourcetest.NewSink()
	// shortly after the in-flight conversation's last line
	now := time.UnixMilli(1792573832000)
	if err := p.Poll(context.Background(), time.UnixMilli(0), now, source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	for rel, e := range exp.Databases {
		if e.Phase != "generating" || e.LastLineMs != 1792573831500 {
			continue
		}
		path := filepath.Join(h, ".gemini", strings.TrimPrefix(rel, "antigravity/"))
		st := sink.Agents[path]
		if st.Phase != usage.Generating || st.InFlightMs != ptr(e.InFlightStartMs) {
			t.Errorf("%s: %+v", rel, st)
		}
	}
}

func TestPollSkipsUnchangedAndOldFiles(t *testing.T) {
	h, _ := home(t)
	p := &poller{home: h}
	now := time.UnixMilli(1792577000000)
	// only conversations modified within 15 minutes of since are read first
	since := time.UnixMilli(1792576600000 + 15*60_000)
	s1 := sourcetest.NewSink()
	if err := p.Poll(context.Background(), since, now, source.NewDedup(), s1); err != nil {
		t.Fatal(err)
	}
	if len(s1.Agents) == 0 || len(s1.Agents) >= 11 {
		t.Errorf("%d agents read at startup", len(s1.Agents))
	}
	s2 := sourcetest.NewSink()
	if err := p.Poll(context.Background(), since, now, source.NewDedup(), s2); err != nil {
		t.Fatal(err)
	}
	if len(s2.Resp) != 0 {
		t.Errorf("unchanged databases read again: %d responses", len(s2.Resp))
	}
}

func TestDetect(t *testing.T) {
	h := t.TempDir()
	if Detect(h) {
		t.Error("detected in an empty home")
	}
	os.MkdirAll(filepath.Join(h, ".gemini", "antigravity-backup"), 0o755)
	if Detect(h) {
		t.Error("a -backup store counts")
	}
	os.MkdirAll(filepath.Join(h, ".gemini", "antigravity-ide"), 0o755)
	if !Detect(h) {
		t.Error("store not detected")
	}
}

func TestProjectOf(t *testing.T) {
	for in, want := range map[string]string{
		"file:///c%3A/Users/dev/projects/orbit": "orbit",
		"file:///Users/me/star%20map":           "star map",
		"vscode-remote://host/home/x/nebula/":   "nebula",
		"/Users/me/proj":                        "proj",
		`C:\work\app`:                           "app",
		"file:///a/%zz%41":                      "%zzA",
	} {
		if got := projectOf(in); got != want {
			t.Errorf("projectOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWalkMalformed(t *testing.T) {
	for _, b := range [][]byte{{0x00}, {0x0b}, {0x08}, {0x12, 0x05, 1}, {0x0d, 1, 2}, {0x08, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}} {
		if _, ok := walk(b); ok {
			t.Errorf("% x accepted", b)
		}
	}
	if fs, ok := walk([]byte{0x08, 0x96, 0x01, 0x12, 0x02, 'h', 'i'}); !ok || len(fs) != 2 || fs[0].v != 150 {
		t.Errorf("walk = %+v %v", fs, ok)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func ptr(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}
