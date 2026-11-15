package devin

import (
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
	AgentID      string  `json:"agentId"`
	StartMs      *int64  `json:"startMs"`
	EndMs        int64   `json:"endMs"`
	FirstMs      *int64  `json:"firstMs"`
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

type expAgent struct {
	Project    string    `json:"project"`
	CwdPath    *string   `json:"cwdPath"`
	Model      *string   `json:"model"`
	Phase      string    `json:"phase"`
	InFlightMs *int64    `json:"inFlightStartMs"`
	LastLineMs int64     `json:"lastLineMs"`
	Responses  []expResp `json:"responses"`
}

type expDay struct {
	Requests    int64 `json:"requests"`
	Input       int64 `json:"input"`
	CachedInput int64 `json:"cachedInput"`
	CacheWrite  int64 `json:"cacheWriteInput"`
	Output      int64 `json:"output"`
	Reasoning   int64 `json:"reasoning"`
	MeasuredOut int64 `json:"measuredOutput"`
	MeasuredMs  int64 `json:"measuredMs"`
	Agents      int   `json:"agents"`
	Models      map[string]struct {
		Requests int64 `json:"requests"`
		Output   int64 `json:"output"`
	} `json:"models"`
}

type scenario struct {
	Locations []string            `json:"locations"`
	Databases []string            `json:"databases"`
	Agents    map[string]expAgent `json:"agents"`
	History   map[string]expDay   `json:"history"`
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

func TestFixtures(t *testing.T) {
	var exp struct {
		Resolve   map[string]*string  `json:"resolve"`
		Scenarios map[string]scenario `json:"scenarios"`
	}
	b, err := os.ReadFile("testdata/devin-expected.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &exp); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs("testdata")
	prefix := root + string(filepath.Separator)

	for loc, want := range exp.Resolve {
		got := ResolveLocation(filepath.Join(root, filepath.FromSlash(loc)))
		w := ""
		if want != nil {
			w = filepath.Join(root, filepath.FromSlash(*want))
		}
		if got != w {
			t.Errorf("resolve %s: %q want %q", loc, got, w)
		}
	}

	for name, sc := range exp.Scenarios {
		t.Run(name, func(t *testing.T) {
			var dbs []string
			for _, l := range sc.Locations {
				if p := ResolveLocation(filepath.Join(root, filepath.FromSlash(l))); p != "" {
					dbs = append(dbs, p)
				}
			}
			var want []string
			for _, d := range sc.Databases {
				want = append(want, filepath.Join(root, filepath.FromSlash(d)))
			}
			if strings.Join(dbs, "|") != strings.Join(want, "|") {
				t.Fatalf("databases %v want %v", dbs, want)
			}
			p := &poller{paths: func() []string { return dbs }}
			sink := sourcetest.NewSink()
			old := time.UnixMilli(0).Add(-24 * 365 * time.Hour)
			if err := p.Poll(context.Background(), old, old, source.NewDedup(), sink); err != nil {
				t.Fatal(err)
			}
			wantResp := map[string]expResp{}
			for id, a := range sc.Agents {
				full := prefix + filepath.FromSlash(id)
				st, ok := sink.Agents[full]
				if !ok {
					t.Errorf("agent %s missing", id)
					continue
				}
				if st.Source != Devin || st.Project != a.Project || st.Model != str(a.Model) ||
					string(st.Phase) != a.Phase || st.InFlightMs != val(a.InFlightMs) || st.LastLineMs != a.LastLineMs {
					t.Errorf("agent %s: got %+v want %+v", id, st, a)
				}
				if st.Subagent {
					t.Errorf("agent %s is a sub-agent", id)
				}
				for _, r := range a.Responses {
					wantResp[r.Key] = r
					if r.AgentID != id {
						t.Errorf("fixture agent id %s != %s", r.AgentID, id)
					}
				}
			}
			if len(sink.Agents) != len(sc.Agents) {
				t.Errorf("%d agents, want %d", len(sink.Agents), len(sc.Agents))
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
				if got.Agent != prefix+filepath.FromSlash(w.AgentID) || got.Source != Devin {
					t.Errorf("%s agent %s", k, got.Agent)
				}
				if val(w.StartMs) != got.StartMs || w.EndMs != got.EndMs || val(w.FirstMs) != got.FirstMs ||
					w.Input != got.Input || w.CachedInput != got.CachedInput || w.CacheWrite != got.CacheWrite ||
					w.CacheWrite1h != got.CacheWrite1h || w.Output != got.Output || w.Reasoning != got.Reasoning ||
					w.Final != got.Final || w.Fast != got.Fast || str(w.Model) != got.Model || str(w.Provider) != got.Provider {
					t.Errorf("%s: want %+v (model %q) got %+v", k, w, str(w.Model), got)
				}
			}
			checkHistory(t, sc.History, sink)
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

func checkHistory(t *testing.T, want map[string]expDay, sink *sourcetest.Sink) {
	t.Helper()
	got := map[string]*expDay{}
	agents := map[string]map[string]bool{}
	for _, r := range sink.Resp {
		day := time.UnixMilli(r.EndMs).UTC().Format("2006-01-02")
		d := got[day]
		if d == nil {
			d = &expDay{Models: map[string]struct {
				Requests int64 `json:"requests"`
				Output   int64 `json:"output"`
			}{}}
			got[day] = d
			agents[day] = map[string]bool{}
		}
		d.Requests++
		d.Input += r.Input
		d.CachedInput += r.CachedInput
		d.CacheWrite += r.CacheWrite
		d.Output += r.Output
		d.Reasoning += r.Reasoning
		if s, e, ok := r.Interval(); ok && r.Final {
			d.MeasuredOut += r.Output
			d.MeasuredMs += e - s
		}
		m := d.Models[r.Model]
		m.Requests++
		m.Output += r.Output
		d.Models[r.Model] = m
		agents[day][r.Agent] = true
	}
	if len(got) != len(want) {
		t.Errorf("history has %d days, want %d", len(got), len(want))
	}
	for day, w := range want {
		g := got[day]
		if g == nil {
			t.Errorf("history day %s missing", day)
			continue
		}
		g.Agents = len(agents[day])
		if g.Requests != w.Requests || g.Input != w.Input || g.CachedInput != w.CachedInput ||
			g.CacheWrite != w.CacheWrite || g.Output != w.Output || g.Reasoning != w.Reasoning ||
			g.MeasuredOut != w.MeasuredOut || g.MeasuredMs != w.MeasuredMs || g.Agents != w.Agents {
			t.Errorf("history %s: got %+v want %+v", day, *g, w)
		}
		if len(g.Models) != len(w.Models) {
			t.Errorf("history %s models %v want %v", day, g.Models, w.Models)
		}
		for m, wm := range w.Models {
			if g.Models[m] != wm {
				t.Errorf("history %s model %q: %+v want %+v", day, m, g.Models[m], wm)
			}
		}
	}
}

func TestAutoDatabase(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	j := filepath.Join
	if g := autoDatabase("darwin", get, "/h"); g != j("/h", ".local", "share", "devin", "cli", "sessions.db") {
		t.Error(g)
	}
	env["XDG_DATA_HOME"] = "/x"
	if g := autoDatabase("linux", get, "/h"); g != j("/x", "devin", "cli", "sessions.db") {
		t.Error(g)
	}
	env["CHISEL_SESSION_DB"] = "/c/s.db"
	if g := autoDatabase("linux", get, "/h"); g != "/c/s.db" {
		t.Error(g)
	}
	delete(env, "CHISEL_SESSION_DB")
	env["APPDATA"] = `C:\A`
	if g := autoDatabase("windows", get, `C:\Users\d`); g != j(`C:\A`, "devin", "cli", "sessions.db") {
		t.Error(g)
	}
}

func TestRegistered(t *testing.T) {
	s, ok := source.Lookup(Devin)
	if !ok || s.Label != "Devin" || s.Color != "#d946ef" || s.Poller == nil {
		t.Errorf("%+v", s)
	}
	var _ usage.Source = Devin
}

func TestSecondPollReadsNothingNew(t *testing.T) {
	root, _ := filepath.Abs("testdata")
	db := filepath.Join(root, "devin", "share", "devin", "cli", "sessions.db")
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
