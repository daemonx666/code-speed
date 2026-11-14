package cursor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sourcetest"
)

func fakeJWT() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(`{"sub":"auth0|user_ABC123"}`)) + ".sig"
}

func makeDB(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if token != "" {
		if _, err := db.Exec(`INSERT INTO ItemTable VALUES (?, ?)`, tokenKey, token); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

type fake struct {
	mu      sync.Mutex
	cookies []string
	starts  []string
	status  int
}

func (f *fake) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/usage-summary", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cookies = append(f.cookies, r.Header.Get("Cookie"))
		st := f.status
		f.mu.Unlock()
		if st != 0 {
			w.WriteHeader(st)
			return
		}
		io.WriteString(w, `{"billingCycleStart":"2026-11-01T00:00:00.000Z","billingCycleEnd":"2026-12-01T00:00:00.000Z","membershipType":"pro",
		"individualUsage":{"plan":{"used":1000,"limit":2000,"totalPercentUsed":42.5}}}`)
	})
	mux.HandleFunc("/api/dashboard/get-filtered-usage-events", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Page      int    `json:"page"`
			StartDate string `json:"startDate"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.starts = append(f.starts, req.StartDate)
		f.mu.Unlock()
		if r.Header.Get("Origin") == "" {
			w.WriteHeader(403)
			return
		}
		if req.Page > 1 {
			io.WriteString(w, `{"totalUsageEventsCount":3,"usageEventsDisplay":[]}`)
			return
		}
		io.WriteString(w, `{"totalUsageEventsCount":3,"usageEventsDisplay":[
		{"timestamp":"1790000000000","model":"claude-4-sonnet","tokenUsage":{"inputTokens":100,"outputTokens":50,"cacheReadTokens":"400","cacheWriteTokens":10}},
		{"timestamp":"1790000100000","model":"gpt-5","tokenUsage":{"inputTokens":7,"outputTokens":3}},
		{"timestamp":"1000","model":"old","tokenUsage":{"inputTokens":1}},
		{"timestamp":"1790000200000","model":"","tokenUsage":{"inputTokens":1}}]}`)
	})
	return mux
}

func TestPoll(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	tok := fakeJWT()
	p := &Poller{BaseURL: srv.URL, DBPaths: []string{makeDB(t, tok)}}
	sink := sourcetest.NewSink()
	now := time.UnixMilli(1790001000000)
	since := time.UnixMilli(1789000000000)
	if err := p.Poll(context.Background(), since, now, source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	if want := "WorkosCursorSessionToken=user_ABC123%3A%3A" + tok; f.cookies[0] != want {
		t.Errorf("cookie = %q", f.cookies[0])
	}
	if len(sink.Quotas) != 1 {
		t.Fatalf("quotas = %d", len(sink.Quotas))
	}
	q := sink.Quotas[0]
	w := q.Windows[0]
	if q.Plan != "pro" || w.UsedPercent != 42.5 || w.Kind != "monthly" || w.WindowMinutes != 30*24*60 ||
		w.ResetsAtMs != time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("quota = %+v", q)
	}
	rs := sink.Responses()
	if len(rs) != 2 {
		t.Fatalf("responses = %d: %+v", len(rs), rs)
	}
	r := rs[0]
	if r.Model != "claude-4-sonnet" || r.EndMs != 1790000000000 || r.StartMs != 0 || !r.Final || r.Agent != "cursor" ||
		r.Input != 510 || r.CachedInput != 400 || r.CacheWrite != 10 || r.Output != 50 {
		t.Errorf("response = %+v", r)
	}
	// second poll starts at the high-water mark
	if err := p.Poll(context.Background(), since, now, source.NewDedup(), sink); err != nil {
		t.Fatal(err)
	}
	if got := f.starts[len(f.starts)-1]; got != "1790000100000" {
		t.Errorf("second startDate = %s", got)
	}
	if len(sink.Responses()) != 2 {
		t.Errorf("upsert duplicated responses")
	}
}

func TestNotSignedIn(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	for _, paths := range [][]string{{makeDB(t, "")}, {filepath.Join(t.TempDir(), "none.vscdb")}} {
		p := &Poller{BaseURL: srv.URL, DBPaths: paths}
		sink := sourcetest.NewSink()
		if err := p.Poll(context.Background(), time.Time{}, time.Now(), source.NewDedup(), sink); err != nil {
			t.Fatal(err)
		}
		if len(f.cookies) != 0 || len(sink.Quotas) != 0 {
			t.Fatal("made a request without a token")
		}
	}
}

func TestUnauthorizedDoesNotLeakToken(t *testing.T) {
	f := &fake{status: 401}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	tok := fakeJWT()
	p := &Poller{BaseURL: srv.URL, DBPaths: []string{makeDB(t, tok)}}
	err := p.Poll(context.Background(), time.Time{}, time.Now(), source.NewDedup(), sourcetest.NewSink())
	if err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("err = %v", err)
	}
}

func TestStatePaths(t *testing.T) {
	if got := StatePaths("darwin", "/h", "")[0]; got != filepath.FromSlash("/h/Library/Application Support/Cursor/User/globalStorage/state.vscdb") {
		t.Error(got)
	}
	if got := StatePaths("linux", "/h", "")[0]; got != filepath.FromSlash("/h/.config/Cursor/User/globalStorage/state.vscdb") {
		t.Error(got)
	}
	if got := StatePaths("windows", "/h", "/ad"); len(got) != 2 || !strings.HasPrefix(got[0], filepath.FromSlash("/ad")) {
		t.Error(got)
	}
}

func TestRegistered(t *testing.T) {
	s, ok := source.Lookup(Source)
	if !ok || !s.Online || s.Every != 5*time.Minute || s.Label != "Cursor" || s.Color != "#f43f5e" {
		t.Fatalf("%+v", s)
	}
}
