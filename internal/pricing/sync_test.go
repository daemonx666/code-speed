package pricing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// bumped is the bundled table at version v, dated updated.
func bumped(v int, updated string) []byte {
	b := bytes.Replace(bundledJSON, []byte(`"version": 8,`), []byte(`"version": `+strconv.Itoa(v)+`,`), 1)
	return bytes.Replace(b, []byte(`"updated": "2026-11-10"`), []byte(`"updated": "`+updated+`"`), 1)
}

func TestSyncKeepsOnlyNewerVerifiedTables(t *testing.T) {
	if Bundled.Version != 8 {
		t.Skip("fixture assumes the bundled table is version 8")
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	body, etag, hits := bumped(9, "2026-11-13"), `"v9"`, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Write(body)
	}))
	defer srv.Close()
	old := SyncURLs
	defer func() { SyncURLs = old }()
	SyncURLs = []string{srv.URL}

	s := Synced{Dir: t.TempDir()}
	if s.Current(now) != Bundled {
		t.Fatal("no download: the bundled table")
	}
	changed, err := s.Sync(context.Background(), srv.Client(), now)
	if err != nil || !changed || s.Current(now).Version != 9 || s.State().Version != 9 {
		t.Fatalf("first sync: %v %v %+v", changed, err, s.State())
	}
	// the server says nothing changed
	if changed, err := s.Sync(context.Background(), srv.Client(), now); err != nil || changed {
		t.Fatalf("304: %v %v", changed, err)
	}

	// a table dated in the future, one whose tests fail, or an older one is
	// never taken
	for name, b := range map[string][]byte{
		"future": bumped(10, "2026-12-30"),
		"tests":  bytes.Replace(bumped(10, "2026-11-13"), []byte(`"expect": "claude-opus-5-5"`), []byte(`"expect": "nope"`), 1),
		"older":  bumped(7, "2026-11-01"),
	} {
		body, etag = b, `"`+name+`"`
		changed, err := s.Sync(context.Background(), srv.Client(), now)
		if changed || s.Current(now).Version != 9 {
			t.Fatalf("%s: changed %v, now v%d", name, changed, s.Current(now).Version)
		}
		if name != "older" && err == nil {
			t.Fatalf("%s: no error", name)
		}
	}
	// a server that fails is reported
	SyncURLs = []string{srv.URL + "/missing", "http://127.0.0.1:1/x"}
	srv.Config.Handler = http.NotFoundHandler()
	if _, err := s.Sync(context.Background(), srv.Client(), now); err == nil || s.State().LastError == "" {
		t.Fatal("failure not reported")
	}
}
