package pricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// SyncURLs is where a newer price table is published: Agent Speed's table
// (the one bundled here, same format and rules), with a mirror for
// networks where raw.githubusercontent.com is blocked. See Agent Speed's
// spec/NETWORK.md §3.
var SyncURLs = []string{
	"https://raw.githubusercontent.com/zoefix/agent-speed/main/spec/assets/prices.json",
	"https://cdn.jsdelivr.net/gh/zoefix/agent-speed@main/spec/assets/prices.json",
}

const maxTable = 1 << 20

// SyncState is what the last syncs did, kept beside the downloaded table.
type SyncState struct {
	Version       int    `json:"version,omitempty"` // of the stored table
	Updated       string `json:"updated,omitempty"`
	URL           string `json:"url,omitempty"`
	ETag          string `json:"etag,omitempty"`
	LastSuccessMs int64  `json:"lastSuccessMs,omitempty"`
	LastAttemptMs int64  `json:"lastAttemptMs,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

func label(r Resolution) string {
	switch r.Kind {
	case Free:
		return "free"
	case Unknown:
		return "unknown"
	}
	if r.Fast {
		return r.Model.ID + " fast"
	}
	return r.Model.ID
}

// Verify parses a downloaded table and checks it the way Agent Speed's
// apps do: it loads, isn't dated in the future, and every one of its own
// tests resolves as it says with this build's matching, so a table this
// build would read differently never becomes current.
func Verify(b []byte, now time.Time) (*Table, error) {
	if len(b) > maxTable {
		return nil, errors.New("table over 1 MiB")
	}
	t, err := Load(b)
	if err != nil {
		return nil, err
	}
	if day, err := time.Parse("2006-01-02", t.Updated); err != nil || day.After(now.AddDate(0, 0, 2)) {
		return nil, fmt.Errorf("bad updated date %q", t.Updated)
	}
	var f struct {
		Tests []struct {
			Model    string  `json:"model"`
			Provider *string `json:"provider"`
			Expect   string  `json:"expect"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if len(f.Tests) == 0 {
		return nil, errors.New("table has no tests")
	}
	for _, tc := range f.Tests {
		p := ""
		if tc.Provider != nil {
			p = *tc.Provider
		}
		if got := label(t.Resolve(tc.Model, p)); got != tc.Expect {
			return nil, fmt.Errorf("table test %q gives %q, wants %q", tc.Model, got, tc.Expect)
		}
	}
	for _, m := range t.byKey {
		for _, v := range []int64{m.prices.Input, m.prices.Output, m.prices.CachedInput} {
			if v < 0 || v > pico(10_000) {
				return nil, fmt.Errorf("%s: price out of range", m.ID)
			}
		}
	}
	return t, nil
}

// Synced is the price table folder: the downloaded table and its state.
type Synced struct{ Dir string }

func (s Synced) table() string { return filepath.Join(s.Dir, "prices.json") }
func (s Synced) state() string { return filepath.Join(s.Dir, "sync.json") }

// State reads what the last syncs did.
func (s Synced) State() SyncState {
	var st SyncState
	if b, err := os.ReadFile(s.state()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func (s Synced) saveState(st SyncState) error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return writeAtomic(s.state(), b)
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Current is the newer of the bundled table and a downloaded one. A stored
// table that no longer verifies (this build matches differently) is
// ignored.
func (s Synced) Current(now time.Time) *Table {
	b, err := os.ReadFile(s.table())
	if err != nil {
		return Bundled
	}
	t, err := Verify(b, now)
	if err != nil || t.Version <= Bundled.Version {
		return Bundled
	}
	return t
}

// Sync asks the URLs for a newer table and stores it. It reports whether
// the current table changed.
func (s Synced) Sync(ctx context.Context, client *http.Client, now time.Time) (bool, error) {
	st := s.State()
	st.LastAttemptMs = now.UnixMilli()
	current := s.Current(now).Version
	var errs []error
	for _, u := range SyncURLs {
		b, etag, err := fetch(ctx, client, u, etagFor(st, u))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			continue
		}
		st.LastSuccessMs, st.LastError = now.UnixMilli(), ""
		if b == nil { // 304: what we had is current
			return false, s.saveState(st)
		}
		t, err := Verify(b, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			continue
		}
		st.URL, st.ETag = u, etag
		if t.Version <= current {
			return false, s.saveState(st)
		}
		if err := writeAtomic(s.table(), b); err != nil {
			return false, err
		}
		st.Version, st.Updated = t.Version, t.Updated
		return true, s.saveState(st)
	}
	err := errors.Join(errs...)
	st.LastError = err.Error()
	_ = s.saveState(st)
	return false, err
}

func etagFor(st SyncState, u string) string {
	if st.URL == u {
		return st.ETag
	}
	return ""
}

func fetch(ctx context.Context, client *http.Client, u, etag string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "code-speed")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, nil
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxTable+1))
	if err != nil {
		return nil, "", err
	}
	return b, resp.Header.Get("ETag"), nil
}
