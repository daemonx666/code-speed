// Package cursor reads Cursor's plan quota and per-request token usage.
//
// Cursor writes no token usage to local logs. As Token Monitor
// (providers/cursor/desktopState.js, auth.js, probe.js; MIT) and CodexBar
// (Providers/Cursor/CursorAppAuth.swift, CursorUsageEventsFetcher.swift;
// MIT) do, the signed-in user's access token is read from Cursor's own
// state.vscdb and sent to cursor.com as the WorkosCursorSessionToken
// cookie. The token lives in memory for one poll and is never logged.
package cursor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/source"
	"github.com/daemonx666/code-speed/internal/source/sqlitedb"
	"github.com/daemonx666/code-speed/internal/usage"
)

// Source is Cursor's source id.
const Source usage.Source = "cursor"

const (
	defaultBase    = "https://cursor.com"
	tokenKey       = "cursorAuth/accessToken"
	reqTimeout     = 10 * time.Second
	pageSize       = 1000
	maxPages       = 50
	maxBody        = 32 << 20
	defaultWindow  = 30 * 24 * 60 // minutes, when the billing cycle start is unknown
	userAgent      = "code-speed"
	errUnauthorize = "cursor: not signed in (HTTP %d)"
)

func init() {
	source.Register(source.Spec{
		Source: Source,
		Label:  "Cursor",
		Color:  "#f43f5e",
		Detect: func(home string) bool {
			for _, p := range StatePaths(runtime.GOOS, home, os.Getenv("APPDATA")) {
				if _, err := os.Stat(p); err == nil {
					return true
				}
			}
			return false
		},
		Poller: func() source.Poller { return New() },
		Every:  5 * time.Minute,
		Online: true,
	})
}

// StatePaths lists where Cursor keeps state.vscdb on an OS
// (token-monitor desktopState.js:6-18).
func StatePaths(goos, home, appData string) []string {
	const tail = "state.vscdb"
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", tail)}
	case "windows":
		var out []string
		if appData != "" {
			out = append(out, filepath.Join(appData, "Cursor", "User", "globalStorage", tail))
		}
		return append(out, filepath.Join(home, "AppData", "Roaming", "Cursor", "User", "globalStorage", tail))
	}
	return []string{filepath.Join(home, ".config", "Cursor", "User", "globalStorage", tail)}
}

// Poller polls Cursor's dashboard API.
type Poller struct {
	BaseURL string       // https://cursor.com; tests point it at httptest
	DBPaths []string     // candidate state.vscdb files; nil means the OS default
	Client  *http.Client // nil means a client with a 10 s timeout

	mu   sync.Mutex
	mark int64 // newest event time (Unix ms) already reported
}

func New() *Poller { return &Poller{} }

func (p *Poller) paths() []string {
	if p.DBPaths != nil {
		return p.DBPaths
	}
	home, _ := os.UserHomeDir()
	return StatePaths(runtime.GOOS, home, os.Getenv("APPDATA"))
}

func (p *Poller) base() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	return defaultBase
}

func (p *Poller) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: reqTimeout}
}

// readToken returns the access token from the first state.vscdb that has
// one. A missing file or key is "not signed in", not an error.
func (p *Poller) readToken() (string, error) {
	for _, path := range p.paths() {
		db, _, err := sqlitedb.Open(path)
		if err != nil {
			continue
		}
		tok, err := queryToken(db)
		db.Close()
		if err == nil && tok != "" {
			return tok, nil
		}
	}
	return "", nil
}

func queryToken(db *sql.DB) (string, error) {
	var v []byte
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, tokenKey).Scan(&v); err != nil {
		return "", err
	}
	return strings.Trim(strings.TrimSpace(string(v)), `"`), nil
}

// cookie builds WorkosCursorSessionToken=<user id>%3A%3A<jwt>; the user
// id is the last "|" part of the JWT's sub (CodexBar CursorAppAuth.swift
// :141-158, token-monitor auth.js:80-128).
func cookie(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("cursor: access token is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", errors.New("cursor: access token payload unreadable")
	}
	sub := gjson.GetBytes(raw, "sub").String()
	segs := strings.Split(sub, "|")
	id := segs[len(segs)-1]
	if id == "" {
		return "", errors.New("cursor: access token has no user id")
	}
	return "WorkosCursorSessionToken=" + id + "%3A%3A" + token, nil
}

type authError struct{ code int }

func (e authError) Error() string { return fmt.Sprintf(errUnauthorize, e.code) }

func (p *Poller) do(ctx context.Context, method, path, ck string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base()+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", ck)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		// Cursor enforces CSRF on its POST endpoints with a matching Origin.
		req.Header.Set("Origin", p.base())
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, errors.New("cursor: request failed: " + path)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, authError{resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("cursor: %s: HTTP %d", path, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, errors.New("cursor: read failed: " + path)
	}
	return b, nil
}

// Poll implements source.Poller.
func (p *Poller) Poll(ctx context.Context, since, now time.Time, d *source.Dedup, sink source.Sink) error {
	token, err := p.readToken()
	if err != nil || token == "" {
		return nil // Cursor isn't installed or signed in
	}
	ck, err := cookie(token)
	if err != nil {
		return err
	}
	qerr := p.pollQuota(ctx, ck, now, sink)
	if _, isAuth := qerr.(authError); isAuth {
		return qerr
	}
	uerr := p.pollEvents(ctx, ck, since, sink)
	if qerr != nil {
		return qerr
	}
	return uerr
}

func clampPct(f float64) float64 { return math.Max(0, math.Min(100, f)) }

func parseISO(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// pollQuota reads GET /api/usage-summary (token-monitor probe.js:7,
// parseUsageSummary; CodexBar CursorStatusProbe+UsageSummary.swift).
func (p *Poller) pollQuota(ctx context.Context, ck string, now time.Time, sink source.Sink) error {
	b, err := p.do(ctx, http.MethodGet, "/api/usage-summary", ck, nil)
	if err != nil {
		return err
	}
	if !gjson.ValidBytes(b) {
		return errors.New("cursor: usage-summary is not JSON")
	}
	plan := gjson.GetBytes(b, "individualUsage.plan")
	var pct float64
	switch t := plan.Get("totalPercentUsed"); {
	case t.Type == gjson.Number:
		pct = t.Float()
	case plan.Get("limit").Float() > 0:
		pct = plan.Get("used").Float() / plan.Get("limit").Float() * 100
	default:
		return nil // no included-usage figure to show
	}
	end := parseISO(gjson.GetBytes(b, "billingCycleEnd").String())
	start := parseISO(gjson.GetBytes(b, "billingCycleStart").String())
	minutes := int64(defaultWindow)
	if start > 0 && end > start {
		minutes = (end - start) / 60_000
	}
	sink.Quota(usage.Quota{
		Source:     Source,
		Plan:       gjson.GetBytes(b, "membershipType").String(),
		ObservedMs: now.UnixMilli(),
		Windows: []usage.QuotaWindow{{
			Kind: "monthly", UsedPercent: clampPct(pct), WindowMinutes: minutes, ResetsAtMs: end,
		}},
	})
	return nil
}

// tokenInt reads a count that Cursor may serialize as number or string.
func tokenInt(r gjson.Result) int64 { return min(numInt(r), source.TokCap) }

func numInt(r gjson.Result) int64 {
	var f float64
	switch r.Type {
	case gjson.Number:
		f = r.Float()
	case gjson.String:
		f, _ = strconv.ParseFloat(r.String(), 64)
	}
	if math.IsNaN(f) || f <= 0 {
		return 0
	}
	return int64(min(f, math.MaxInt64/2))
}

// pollEvents pages POST /api/dashboard/get-filtered-usage-events
// (CodexBar CursorUsageEventsFetcher.swift:467-485; the body takes epoch
// milliseconds as strings). Events are upserted by key, so the newest
// time seen is re-read next poll without double counting.
func (p *Poller) pollEvents(ctx context.Context, ck string, since time.Time, sink source.Sink) error {
	p.mu.Lock()
	from := max(since.UnixMilli(), p.mark, 0)
	p.mu.Unlock()

	newest := from
	seen := map[string]int{}
	for page := 1; page <= maxPages; page++ {
		body, _ := json.Marshal(map[string]any{
			"page": page, "pageSize": pageSize, "startDate": strconv.FormatInt(from, 10),
		})
		b, err := p.do(ctx, http.MethodPost, "/api/dashboard/get-filtered-usage-events", ck, body)
		if err != nil {
			p.advance(newest)
			return err
		}
		evs := gjson.GetBytes(b, "usageEventsDisplay").Array()
		if len(evs) == 0 {
			break
		}
		for _, ev := range evs {
			if ms, ok := emit(ev, from, seen, sink); ok {
				newest = max(newest, ms)
			}
		}
		if total := gjson.GetBytes(b, "totalUsageEventsCount"); total.Exists() && int64(page*pageSize) >= numInt(total) {
			break
		}
	}
	p.advance(newest)
	return nil
}

func (p *Poller) advance(ms int64) {
	p.mu.Lock()
	p.mark = max(p.mark, ms)
	p.mu.Unlock()
}

func emit(ev gjson.Result, from int64, seen map[string]int, sink source.Sink) (int64, bool) {
	ms := numInt(ev.Get("timestamp"))
	model := strings.TrimSpace(ev.Get("model").String())
	if ms <= 0 || ms < from || model == "" {
		return 0, false
	}
	tu := ev.Get("tokenUsage")
	in, out := tokenInt(tu.Get("inputTokens")), tokenInt(tu.Get("outputTokens"))
	cr, cw := tokenInt(tu.Get("cacheReadTokens")), tokenInt(tu.Get("cacheWriteTokens"))
	if in+out+cr+cw == 0 {
		return 0, false
	}
	// Events carry no id: time, model and counts identify one, with a
	// counter for identical ones in the same millisecond.
	key := fmt.Sprintf("cursor:%d:%s:%d:%d:%d:%d", ms, model, in, out, cr, cw)
	n := seen[key]
	seen[key]++
	if n > 0 {
		key += "#" + strconv.Itoa(n)
	}
	sink.Response(usage.Response{
		Key: key, Source: Source, Agent: "cursor", Model: model, EndMs: ms,
		Tokens: usage.Tokens{
			Input:       min(in+cr+cw, source.TokCap),
			CachedInput: cr,
			CacheWrite:  cw,
			Output:      out,
		},
		Final: true,
	})
	return ms, true
}
