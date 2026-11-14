package openclaw

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
	"github.com/daemonx666/code-speed/internal/usage"
)

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
	Final    bool   `json:"final"`
}

type winExp struct {
	Project    string    `json:"project"`
	Model      *string   `json:"model"`
	IsSubagent bool      `json:"isSubagent"`
	Phase      string    `json:"phase"`
	InFlight   *int64    `json:"inFlightStartMs"`
	LastLine   int64     `json:"lastLineMs"`
	Responses  []respExp `json:"responses"`
}

type expected struct {
	Mtimes    map[string]int64           `json:"mtimes"`
	DBOrder   []string                   `json:"dbOrder"`
	Discover  []string                   `json:"discover"`
	Databases map[string]json.RawMessage `json:"databases"`
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

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func ptrEq(a *int64, b int64) bool {
	if a == nil {
		return b == 0
	}
	return *a == b
}

func setup(t *testing.T) (root string, e expected) {
	t.Helper()
	e = load(t)
	root = t.TempDir()
	copyTree(t, "testdata", root)
	os.Remove(filepath.Join(root, "expected.json"))
	for rel, ms := range e.Mtimes {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(p); err == nil {
			tm := time.UnixMilli(ms)
			os.Chtimes(p, tm, tm)
		}
	}
	return
}

func TestDiscovery(t *testing.T) {
	root, e := setup(t)
	base := filepath.Join(root, "openclaw")
	t.Setenv("OPENCLAW_STATE_DIR", "")
	t.Setenv("OPENCLAW_HOME", "")
	states := stateDirs(base)
	if len(states) != 2 || states[0] != filepath.Join(base, ".openclaw") || states[1] != filepath.Join(base, ".openclaw-work") {
		t.Fatalf("state folders %q", states)
	}
	var got []string
	for _, p := range databases(states) {
		rel, _ := filepath.Rel(root, p)
		got = append(got, filepath.ToSlash(rel))
	}
	want := append([]string{}, e.Discover...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("databases %q want %q", got, want)
	}
	t.Setenv("OPENCLAW_STATE_DIR", filepath.Join(base, ".openclaw-work"))
	if s := stateDirs(base); len(s) != 1 || s[0] != filepath.Join(base, ".openclaw-work") {
		t.Errorf("state dir env: %q", s)
	}
}

func TestFixtures(t *testing.T) {
	root, e := setup(t)
	before := listFiles(t, root)
	p := &poller{dbs: func() []string {
		var out []string
		for _, rel := range e.DBOrder {
			out = append(out, filepath.Join(root, filepath.FromSlash(rel)))
		}
		return out
	}}
	s := sourcetest.NewSink()
	if err := p.Poll(context.Background(), time.Time{}, time.UnixMilli(1792504000000), source.NewDedup(), s); err != nil {
		t.Fatal(err)
	}
	if after := listFiles(t, root); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("reading changed the files:\nbefore %q\nafter  %q", before, after)
	}
	for rel, raw := range e.Databases {
		var wins map[string]winExp
		if json.Unmarshal(raw, &wins) != nil {
			continue // an unsupported database (schema 1): nothing to read
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		for sid, want := range wins {
			sid = sid[strings.LastIndex(sid, "#")+1:]
			id := path + "#" + sid
			var got []usage.Response
			for _, r := range s.Responses() {
				if r.Agent == id {
					got = append(got, r)
				}
			}
			if len(got) != len(want.Responses) {
				t.Errorf("%s: %d responses, want %d", sid, len(got), len(want.Responses))
				continue
			}
			for i, w := range want.Responses {
				g := got[i]
				if g.Key != w.Key || !ptrEq(w.StartMs, g.StartMs) || g.EndMs != w.EndMs ||
					g.Input != w.Input || g.CachedInput != w.Cached || g.CacheWrite != w.CW ||
					g.CacheWrite1h != w.CW1h || g.Output != w.Output || g.Reasoning != w.Reason ||
					g.Model != w.Model || g.Provider != w.Provider || g.Final != w.Final ||
					g.Project != want.Project || g.Fast || g.Source != OpenClaw {
					t.Errorf("%s response %d: got %+v want %+v", sid, i, g, w)
				}
			}
			st, ok := s.Agents[id]
			if !ok {
				t.Errorf("%s: no state", sid)
				continue
			}
			model := ""
			if want.Model != nil {
				model = *want.Model
			}
			if st.Project != want.Project || st.Model != model || st.Subagent != want.IsSubagent ||
				string(st.Phase) != want.Phase || !ptrEq(want.InFlight, st.InFlightMs) || st.LastLineMs != want.LastLine {
				t.Errorf("%s state: got %+v want %+v", sid, st, want)
			}
		}
	}
}

func TestIncrementalAndRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE session_windows (session_id TEXT PRIMARY KEY, created_at INTEGER, transcript_updated_at INTEGER)`,
		`CREATE TABLE transcript_events (session_id TEXT, seq INTEGER, event_json TEXT, created_at INTEGER, PRIMARY KEY (session_id, seq))`,
		`CREATE TABLE transcript_rewrite_watermarks (session_id TEXT PRIMARY KEY, generation TEXT, updated_at INTEGER)`,
		`INSERT INTO session_windows VALUES ('w', 1000, 1000)`,
		`INSERT INTO transcript_rewrite_watermarks VALUES ('w', 'g1', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	add := func(seq int, upd int64, js string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO transcript_events VALUES ('w', ?, ?, 0)`, seq, js); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE session_windows SET transcript_updated_at = ?`, upd); err != nil {
			t.Fatal(err)
		}
	}
	asst := func(id, ts string, start int64, out int) string {
		return fmt.Sprintf(`{"type":"message","id":"%s","timestamp":"%s","message":{"role":"assistant","model":"m","provider":"p","api":"x","stopReason":"stop","timestamp":%d,"usage":{"input":10,"output":%d,"cacheRead":0,"cacheWrite":0},"content":[]}}`, id, ts, start, out)
	}
	add(1, 1000, `{"type":"session","version":4,"id":"w","timestamp":"2026-10-20T13:00:00.000Z","cwd":"/x/proj"}`)
	add(2, 1001, `{"type":"message","id":"u1","timestamp":"2026-10-20T13:00:01.000Z","message":{"role":"user","content":[],"timestamp":1792501201000}}`)

	p := &poller{dbs: func() []string { return []string{path} }}
	s := sourcetest.NewSink()
	d := source.NewDedup()
	poll := func() {
		t.Helper()
		if err := p.Poll(context.Background(), time.Time{}, time.UnixMilli(1792504000000), d, s); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	id := path + "#w"
	if st := s.Agents[id]; st.Phase != usage.Generating || st.InFlightMs != 1792501201000 || st.Project != "proj" {
		t.Fatalf("state after user entry: %+v", st)
	}
	add(3, 1002, asst("a1", "2026-10-20T13:00:05.000Z", 1792501202000, 50))
	poll()
	rs := s.Responses()
	if len(rs) != 1 || rs[0].StartMs != 1792501202000 || rs[0].EndMs != 1792501205000 || rs[0].Output != 50 {
		t.Fatalf("responses: %+v", rs)
	}
	if st := s.Agents[id]; st.Phase != usage.Idle {
		t.Fatalf("state after answer: %+v", st)
	}

	// a rewrite: new generation, same entries plus one more; the same key merges
	if _, err := db.Exec(`UPDATE transcript_rewrite_watermarks SET generation = 'g2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE transcript_events SET event_json = ? WHERE seq = 3`, asst("a1", "2026-10-20T13:00:05.000Z", 1792501202000, 60)); err != nil {
		t.Fatal(err)
	}
	add(4, 1003, asst("a2", "2026-10-20T13:00:09.000Z", 1792501207000, 70))
	poll()
	rs = s.Responses()
	if len(rs) != 2 || rs[0].Output != 60 || rs[1].Output != 70 {
		t.Fatalf("responses after rewrite: %+v", rs)
	}
	poll() // unchanged: nothing new
	if len(s.Responses()) != 2 {
		t.Errorf("responses after idle poll: %d", len(s.Responses()))
	}
}
