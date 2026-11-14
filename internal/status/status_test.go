package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

var now = time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)

func TestFetchStatusAndTodaysIncidents(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sp/status.json":
			w.Write([]byte(`{"page":{"name":"X"},"status":{"indicator":"minor","description":"Partially Degraded Service"}}`))
		case "/sp/incidents.json":
			w.Write([]byte(`{"incidents":[
			  {"name":"Errors on Opus","status":"investigating","impact":"major","created_at":"2026-11-14T13:00:00Z","shortlink":"https://stspg.io/a"},
			  {"name":"Slow logins","status":"resolved","impact":"minor","created_at":"2026-11-14T02:00:00Z","resolved_at":"2026-11-14T03:00:00Z"},
			  {"name":"Overnight","status":"resolved","impact":"minor","created_at":"2026-11-13T22:00:00Z","resolved_at":"2026-11-14T01:00:00Z"},
			  {"name":"Old","status":"resolved","impact":"critical","created_at":"2026-11-10T02:00:00Z","resolved_at":"2026-11-10T03:00:00Z"}]}`))
		case "/odd/status.json":
			w.Write([]byte(`{"status":{"indicator":"purple"}}`))
		case "/gc":
			w.Write([]byte(`[
			 {"begin":"2026-11-01T00:00:00Z","end":"2026-11-01T02:00:00Z","severity":"high","external_desc":"old","affected_products":[{"title":"Gemini API"}]},
			 {"begin":"2026-11-14T10:00:00Z","severity":"high","external_desc":"BigQuery slow","affected_products":[{"title":"BigQuery"}]},
			 {"begin":"2026-11-14T11:00:00Z","severity":"medium","external_desc":"Gemini errors in us-central1","uri":"incidents/x","affected_products":[{"title":"Vertex Gemini API"}]}]`))
		case "/slow/status.json":
			time.Sleep(Timeout + 2*time.Second)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	get := func(path, kind string, inc bool) State {
		return Fetch(context.Background(), srv.Client(), Provider{ID: "t", API: srv.URL + path, Kind: kind}, State{}, now, inc)
	}
	st := get("/sp/status.json", Statuspage, true)
	if st.Indicator != Minor || st.Description != "Partially Degraded Service" || st.Error != "" || st.CheckedMs == 0 {
		t.Fatalf("statuspage %+v", st)
	}
	if len(st.Today) != 3 || st.Today[0].Name != "Errors on Opus" || st.Today[0].Resolved || !st.Today[1].Resolved {
		t.Fatalf("today %+v", st.Today)
	}
	if st := get("/odd/status.json", Statuspage, false); st.Indicator != Unknown || st.Error == "" || st.Fails != 1 {
		t.Fatalf("odd %+v", st)
	}
	if st := get("/gc", GCloud, false); st.Indicator != Major || !strings.Contains(st.Description, "Gemini errors") ||
		len(st.Today) != 1 || !strings.HasPrefix(st.Today[0].Shortlink, "https://status.cloud.google.com/") {
		t.Fatalf("gcloud %+v", st)
	}
	if testing.Short() {
		return
	}
	start := time.Now()
	if st := get("/slow/status.json", Statuspage, false); st.Error != "timed out" || time.Since(start) > Timeout+time.Second {
		t.Fatalf("slow %+v after %v", st, time.Since(start))
	}
}

func TestFailuresBackOffAndKeepTheLastState(t *testing.T) {
	var hits atomic.Int32
	fail := atomic.Bool{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "down", 503)
			return
		}
		if strings.HasSuffix(r.URL.Path, "incidents.json") {
			w.Write([]byte(`{"incidents":[]}`))
			return
		}
		w.Write([]byte(`{"status":{"indicator":"none","description":"All Systems Operational"}}`))
	}))
	defer srv.Close()
	old := Providers
	defer func() { Providers = old }()
	Providers = []Provider{{ID: "t", Name: "T", Page: srv.URL, API: srv.URL + "/api/v2/status.json", Kind: Statuspage}}
	c := NewChecker()
	c.client = srv.Client()

	c.Check(context.Background(), []string{"t"}, false)
	if st := c.States([]string{"t"})[0]; st.Indicator != None || hits.Load() != 2 {
		t.Fatalf("first %+v, %d requests", st, hits.Load())
	}
	// failing: the last state stays, with the error, and it waits
	fail.Store(true)
	c.Check(context.Background(), []string{"t"}, true)
	st := c.States([]string{"t"})[0]
	if st.Indicator != None || st.Fails != 1 || !strings.Contains(st.Error, "503") || st.NextMs <= time.Now().UnixMilli() {
		t.Fatalf("failed %+v", st)
	}
	n := hits.Load()
	c.Check(context.Background(), []string{"t"}, false)
	if hits.Load() != n {
		t.Fatal("asked again while backing off")
	}
	if backoff(1) != 10*time.Minute || backoff(2) != 20*time.Minute || backoff(10) != 6*time.Hour {
		t.Fatalf("backoff %v %v %v", backoff(1), backoff(2), backoff(10))
	}
	// the user asks: it tries now; success clears the failures
	fail.Store(false)
	c.Check(context.Background(), []string{"t"}, true)
	if st := c.States([]string{"t"})[0]; st.Fails != 0 || st.Error != "" || st.NextMs != 0 {
		t.Fatalf("recovered %+v", st)
	}
}

func TestProvidersFor(t *testing.T) {
	got := For([]usage.Source{usage.Codex, usage.ClaudeCode, "gemini", "antigravity"})
	if strings.Join(got, ",") != "claude,openai,google" {
		t.Fatalf("for: %v", got)
	}
	seen := map[string]bool{}
	for _, p := range Providers {
		if seen[p.ID] || !strings.HasPrefix(p.Page, "https://") || (p.API != "" && !strings.HasPrefix(p.API, "https://")) {
			t.Fatalf("bad provider %+v", p)
		}
		seen[p.ID] = true
	}
	if !allowedHost("status.x.ai") || allowedHost("evil.example") {
		t.Fatal("redirect hosts")
	}
}
