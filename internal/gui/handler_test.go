package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/engine"
	"github.com/daemonx666/code-speed/internal/service"
	"github.com/daemonx666/code-speed/internal/settings"
	_ "github.com/daemonx666/code-speed/internal/source"
)

func get(t *testing.T, h http.Handler, path string, v any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if rec.Code != 200 {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}

func TestHandler(t *testing.T) {
	t.Setenv("CODE_SPEED_HOME", t.TempDir())
	setHome(t, t.TempDir()) // no real logs
	svc := service.New("v9")
	quit := make(chan struct{}, 1)
	var lang string
	h := Handler(svc, func() { quit <- struct{}{} }, func(l string) { lang = l })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "app.js") {
		t.Fatalf("page: %d", rec.Code)
	}

	var v view
	get(t, h, "/api/snapshot", &v)
	if v.Ready || v.Version != "v9" || len(v.Sources) < 2 {
		t.Fatalf("before seeding: ready=%v version=%q sources=%d", v.Ready, v.Version, len(v.Sources))
	}
	svc.SeedNow()
	get(t, h, "/api/snapshot", &v)
	if !v.Ready || len(v.Week) != engine.HistoryDays {
		t.Fatalf("after seeding: ready=%v week=%d", v.Ready, len(v.Week))
	}

	var p engine.PeriodView
	get(t, h, "/api/period?p=month", &p)
	if p.Period != engine.PeriodMonth || len(p.Series) != 30 {
		t.Fatalf("period: %s, %d points", p.Period, len(p.Series))
	}

	var sv settingsView
	get(t, h, "/api/settings", &sv)
	if sv.Settings.Language != settings.LangAuto || len(sv.Sources) < 2 {
		t.Fatalf("settings: %+v", sv.Settings)
	}
	body, _ := json.Marshal(settings.Settings{Language: "zh-CN", MenuBar: "cost", Theme: "nonsense"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("POST settings = %d %s", rec.Code, rec.Body.String())
	}
	if st := svc.Settings(); st.Language != "zh-CN" || st.MenuBar != "cost" || st.Theme != "auto" {
		t.Fatalf("saved %+v", st)
	}
	if again, err := settings.Load(strings.TrimSpace(sv.DataDir) + "/settings.json"); err != nil || again.MenuBar != "cost" {
		t.Fatalf("settings file: %+v %v", again, err)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/locale", strings.NewReader(`{"lang":"zh-TW"}`)))
	if rec.Code != http.StatusNoContent || lang != "zh-TW" {
		t.Fatalf("locale: %d %q", rec.Code, lang)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/quit", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/quit = %d", rec.Code)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/quit", nil))
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("POST /api/quit did not quit")
	}
}

func TestTrayText(t *testing.T) {
	sp := func(v float64) string { return "S" }
	usd := func(int64) string { return "$" }
	var s engine.Snapshot
	if TrayText(settings.MenuSpeed, s, sp, usd) != "" || TrayText(settings.MenuSpeedCost, s, sp, usd) != "$" {
		t.Fatal("idle")
	}
	s.Live = 40
	for mode, want := range map[string]string{settings.MenuSpeed: "S", settings.MenuSpeedCost: "S · $",
		settings.MenuCost: "$", settings.MenuIcon: ""} {
		if got := TrayText(mode, s, sp, usd); got != want {
			t.Errorf("%s: %q, want %q", mode, got, want)
		}
	}
	if Lang(settings.Settings{Language: "auto"}, "zh-CN") != "zh-CN" || Lang(settings.Settings{Language: "en"}, "zh") != "en" {
		t.Fatal("Lang")
	}
}

func TestSessionAPIReadsOnlyKnownSessions(t *testing.T) {
	t.Setenv("CODE_SPEED_HOME", t.TempDir())
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	dir := filepath.Join(home, ".claude", "projects", "p")
	os.MkdirAll(dir, 0o755)
	log := filepath.Join(dir, "s.jsonl")
	now := time.Now().UTC()
	line := func(id string, at time.Time, out int) string {
		return `{"type":"assistant","timestamp":"` + at.Format(time.RFC3339Nano) + `","cwd":"/w/shop","message":{"id":"` + id +
			`","model":"claude-sonnet-5-5","stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":` + strconv.Itoa(out) + `}}}` + "\n"
	}
	os.WriteFile(log, []byte(`{"type":"user","timestamp":"`+now.Add(-10*time.Second).Format(time.RFC3339Nano)+`"}`+"\n"+
		line("m1", now.Add(-6*time.Second), 400)+line("m2", now.Add(-time.Second), 50)), 0o600)

	svc := service.New("v9")
	svc.SeedNow()
	h := Handler(svc, nil, nil)

	var ss []engine.Session
	get(t, h, "/api/sessions", &ss)
	if len(ss) != 1 || ss[0].Agent != log || ss[0].Requests != 2 || ss[0].Project != "shop" {
		t.Fatalf("sessions %+v", ss)
	}
	var d service.SessionDetail
	get(t, h, "/api/session?id="+url.QueryEscape(log), &d)
	if d.From != "log" || len(d.Requests) != 2 || d.Requests[0].Output != 400 || d.Requests[0].Speed != 100 || d.Session.Requests != 2 {
		t.Fatalf("detail %+v", d)
	}
	for _, bad := range []string{"/etc/passwd", filepath.Join(dir, "other.jsonl"), ""} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/session?id="+url.QueryEscape(bad), nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%q: %d", bad, rec.Code)
		}
	}
}

func TestExportPricesAndOpen(t *testing.T) {
	t.Setenv("CODE_SPEED_HOME", t.TempDir())
	home := t.TempDir()
	setHome(t, home)
	os.MkdirAll(filepath.Join(home, "Downloads"), 0o755)
	svc := service.New("v9")
	svc.SeedNow()
	h := Handler(svc, nil, nil)
	do := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return rec
	}
	rec := do("/api/export", `{"period":"week","format":"csv"}`)
	var out struct{ Path string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || filepath.Dir(out.Path) != filepath.Join(home, "Downloads") {
		t.Fatalf("export %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(out.Path); !strings.HasPrefix(string(b), "date,source") {
		t.Fatalf("csv %q", b)
	}
	// custom prices: saved, reloaded, read back; a bad multiplier refused
	rec = do("/api/prices", `{"models":[{"id":"relay-x","prices":{"input":1,"output":2}}],"multipliers":{"sources":{"codex":0.5}}}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "relay-x") || svc.PricesCount != 1 {
		t.Fatalf("prices %d %s", rec.Code, rec.Body.String())
	}
	if rec = do("/api/prices", `{"multipliers":{"sources":{"codex":-2}}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad multiplier: %d", rec.Code)
	}
	// open: only the fixed targets
	if rec = do("/api/open", `{"target":"/etc"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("open anything: %d", rec.Code)
	}
	var alerts []json.RawMessage
	get(t, h, "/api/alerts", &alerts)
}

// setHome points the home folder at dir: HOME, and USERPROFILE on Windows.
func setHome(t *testing.T, dir string) {
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}
