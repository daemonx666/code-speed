// Package status reads the public status pages of the services the agents
// run on: Claude, OpenAI, Cursor and the rest. Every page is the same
// public file for everyone (Atlassian Statuspage's /api/v2/status.json or
// a page that serves the same form); nothing about the user is sent.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Kinds of status feed.
const (
	Statuspage = "statuspage" // {"status":{"indicator","description"}}
	GCloud     = "gcloud"     // Google Cloud's incidents.json, filtered to Gemini
)

// Provider is one service with a status page.
type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Page string `json:"page"`          // the page people read
	API  string `json:"api,omitempty"` // the feed; "" when the page has none (a link only)
	Kind string `json:"-"`
	// Sources are the agents that run on it, to pick the providers to show
	Sources []usage.Source `json:"sources,omitempty"`
}

// Providers is every service code-speed knows a status page of.
var Providers = []Provider{
	{ID: "claude", Name: "Claude", Page: "https://status.claude.com", API: "https://status.claude.com/api/v2/status.json", Kind: Statuspage,
		Sources: []usage.Source{usage.ClaudeCode}},
	{ID: "openai", Name: "OpenAI", Page: "https://status.openai.com", API: "https://status.openai.com/api/v2/status.json", Kind: Statuspage,
		Sources: []usage.Source{usage.Codex}},
	{ID: "github", Name: "GitHub", Page: "https://www.githubstatus.com", API: "https://www.githubstatus.com/api/v2/status.json", Kind: Statuspage},
	{ID: "cursor", Name: "Cursor", Page: "https://status.cursor.com", API: "https://status.cursor.com/api/v2/status.json", Kind: Statuspage,
		Sources: []usage.Source{"cursor"}},
	{ID: "xai", Name: "xAI", Page: "https://status.x.ai", API: "https://status.x.ai/api/compat/status.json", Kind: Statuspage,
		Sources: []usage.Source{"grok"}},
	{ID: "google", Name: "Google Gemini", Page: "https://aistudio.google.com/status", API: "https://status.cloud.google.com/incidents.json", Kind: GCloud,
		Sources: []usage.Source{"gemini", "antigravity"}},
	// DeepSeek's and Mistral's pages have no Statuspage feed: links only
	{ID: "deepseek", Name: "DeepSeek", Page: "https://status.deepseek.com", Sources: []usage.Source{"dsh"}},
	{ID: "moonshot", Name: "Moonshot (Kimi)", Page: "https://status.moonshot.cn", API: "https://status.moonshot.cn/api/v2/status.json", Kind: Statuspage,
		Sources: []usage.Source{"kimi"}},
	{ID: "minimax", Name: "MiniMax", Page: "https://status.minimax.io", API: "https://status.minimax.io/api/v2/status.json", Kind: Statuspage},
	{ID: "factory", Name: "Factory", Page: "https://status.factory.ai", API: "https://status.factory.ai/api/v2/status.json", Kind: Statuspage,
		Sources: []usage.Source{"droid"}},
	{ID: "windsurf", Name: "Windsurf", Page: "https://status.windsurf.com", API: "https://status.windsurf.com/api/v2/status.json", Kind: Statuspage},
	{ID: "augment", Name: "Augment Code", Page: "https://status.augmentcode.com", API: "https://status.augmentcode.com/api/v2/status.json", Kind: Statuspage},
	{ID: "mistral", Name: "Mistral", Page: "https://status.mistral.ai"},
	{ID: "groq", Name: "Groq", Page: "https://groqstatus.com", API: "https://groqstatus.com/api/v2/status.json", Kind: Statuspage},
	{ID: "fireworks", Name: "Fireworks", Page: "https://status.fireworks.ai", API: "https://status.fireworks.ai/api/v2/status.json", Kind: Statuspage},
	{ID: "openrouter", Name: "OpenRouter", Page: "https://status.openrouter.ai"},
}

// Lookup finds a provider by id.
func Lookup(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// For lists the providers the agents in use run on, in list order.
func For(srcs []usage.Source) []string {
	var out []string
	for _, p := range Providers {
		for _, s := range p.Sources {
			if slices.Contains(srcs, s) {
				out = append(out, p.ID)
				break
			}
		}
	}
	return out
}

// Indicators, Statuspage's words.
const (
	None        = "none" // all good
	Minor       = "minor"
	Major       = "major"
	Critical    = "critical"
	Maintenance = "maintenance"
	Unknown     = "unknown" // never read yet, or couldn't be
)

var rank = map[string]int{None: 0, Maintenance: 1, Minor: 1, Major: 2, Critical: 3}

// Worse reports whether indicator a is worse than b.
func Worse(a, b string) bool { return rank[a] > rank[b] }

// Incident is one of a provider's incidents of the day.
type Incident struct {
	Name      string `json:"name"`
	Impact    string `json:"impact"` // none, minor, major, critical, maintenance
	Status    string `json:"status"` // investigating … resolved
	StartMs   int64  `json:"startMs"`
	Resolved  bool   `json:"resolved"`
	Shortlink string `json:"shortlink,omitempty"`
}

// State is a provider's status as last read.
type State struct {
	ID          string `json:"id"`
	Indicator   string `json:"indicator"` // the last one read; Unknown before any
	Description string `json:"description,omitempty"`
	CheckedMs   int64  `json:"checkedMs,omitempty"` // when it was last read
	// Today are the incidents opened, open or resolved today
	Today []Incident `json:"today"`
	// Error is why the last attempt failed; Fails how many in a row, and
	// NextMs when the next attempt is (backing off while it fails)
	Error  string `json:"error,omitempty"`
	Fails  int    `json:"fails,omitempty"`
	NextMs int64  `json:"nextMs,omitempty"`

	incidentsMs int64 // when today's incidents were last read
}

// hosts are where a feed may redirect to: the providers' own hosts (and
// x.ai, which Cursor's page sends to)
func allowedHost(h string) bool {
	for _, p := range Providers {
		for _, u := range []string{p.Page, p.API} {
			if pu, err := url.Parse(u); err == nil && u != "" && pu.Hostname() == h {
				return true
			}
		}
	}
	return false
}

// Timeout bounds one request; a slow page fails rather than hangs.
const Timeout = 8 * time.Second

// Client fetches the feeds: https only, redirects only to a provider's host.
var Client = &http.Client{
	Timeout: Timeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || req.URL.Scheme != "https" || !allowedHost(req.URL.Hostname()) {
			return errors.New("redirect refused")
		}
		return nil
	},
}

const maxFeed = 4 << 20 // Google's incident list is the large one

func get(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "code-speed")
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
			return nil, errors.New("timed out")
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxFeed))
}

// incidentsURL is a Statuspage feed's incident list, beside its status.
func incidentsURL(api string) string {
	return strings.TrimSuffix(api, "status.json") + "incidents.json"
}

// Fetch reads one provider's feed into st (its last state): the current
// status, and today's incidents when withIncidents. On failure st keeps
// what it knew and records the error.
func Fetch(ctx context.Context, c *http.Client, p Provider, st State, now time.Time, withIncidents bool) State {
	st.ID = p.ID
	if st.Indicator == "" {
		st.Indicator = Unknown
	}
	if p.API == "" {
		return st
	}
	b, err := get(ctx, c, p.API)
	if err == nil {
		switch p.Kind {
		case GCloud:
			err = readGCloud(b, &st, now)
		default:
			err = readStatuspage(b, &st)
			if err == nil && withIncidents {
				var ib []byte
				if ib, err = get(ctx, c, incidentsURL(p.API)); err == nil {
					err = readIncidents(ib, &st, now)
				}
			}
		}
	}
	if err != nil {
		st.Error, st.Fails = err.Error(), st.Fails+1
		st.NextMs = now.Add(backoff(st.Fails)).UnixMilli()
		return st
	}
	st.Error, st.Fails, st.NextMs, st.CheckedMs = "", 0, 0, now.UnixMilli()
	if withIncidents || p.Kind == GCloud {
		st.incidentsMs = now.UnixMilli()
	}
	return st
}

// backoff is how long to wait after n failures in a row: 10 minutes,
// doubling, at most 6 hours.
func backoff(n int) time.Duration {
	d := 10 * time.Minute
	for i := 1; i < n && d < 6*time.Hour; i++ {
		d *= 2
	}
	return min(d, 6*time.Hour)
}

func readStatuspage(b []byte, st *State) error {
	var f struct {
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	if _, ok := rank[f.Status.Indicator]; !ok {
		return fmt.Errorf("unknown indicator %q", f.Status.Indicator)
	}
	st.Indicator, st.Description = f.Status.Indicator, clip(f.Status.Description)
	return nil
}

// today reports whether an incident belongs to the day of now: opened
// today, still open, or resolved today.
func today(now time.Time, start, end time.Time, open bool) bool {
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	return open || !start.Before(day) || (!end.IsZero() && !end.Before(day))
}

func readIncidents(b []byte, st *State, now time.Time) error {
	var f struct {
		Incidents []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Impact     string `json:"impact"`
			CreatedAt  string `json:"created_at"`
			ResolvedAt string `json:"resolved_at"`
			Shortlink  string `json:"shortlink"`
		} `json:"incidents"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	st.Today = []Incident{}
	for _, in := range f.Incidents {
		start, err := time.Parse(time.RFC3339, in.CreatedAt)
		if err != nil {
			continue
		}
		end, _ := time.Parse(time.RFC3339, in.ResolvedAt)
		resolved := in.Status == "resolved" || in.Status == "postmortem" || in.Status == "completed"
		if !today(now, start, end, !resolved) || len(st.Today) >= 10 {
			continue
		}
		link := in.Shortlink
		if !strings.HasPrefix(link, "https://") {
			link = ""
		}
		st.Today = append(st.Today, Incident{Name: clip(in.Name), Impact: in.Impact, Status: in.Status,
			StartMs: start.UnixMilli(), Resolved: resolved, Shortlink: link})
	}
	return nil
}

// readGCloud keeps the Gemini incidents of Google's list: the open ones
// set the indicator, today's are listed.
func readGCloud(b []byte, st *State, now time.Time) error {
	var incs []struct {
		Begin    string `json:"begin"`
		End      string `json:"end"`
		Severity string `json:"severity"`
		Desc     string `json:"external_desc"`
		URI      string `json:"uri"`
		Products []struct {
			Title string `json:"title"`
		} `json:"affected_products"`
	}
	if err := json.Unmarshal(b, &incs); err != nil {
		return err
	}
	st.Indicator, st.Description, st.Today = None, "", []Incident{}
	for _, in := range incs {
		gemini := false
		for _, p := range in.Products {
			t := strings.ToLower(p.Title)
			if strings.Contains(t, "gemini") || strings.Contains(t, "vertex ai") || strings.Contains(t, "ai studio") {
				gemini = true
			}
		}
		if !gemini {
			continue
		}
		ind := map[string]string{"low": Minor, "medium": Major, "high": Critical}[in.Severity]
		if ind == "" {
			ind = Minor
		}
		open := in.End == ""
		if open && Worse(ind, st.Indicator) {
			st.Indicator, st.Description = ind, clip(in.Desc)
		}
		start, err := time.Parse(time.RFC3339, in.Begin)
		if err != nil {
			continue
		}
		end, _ := time.Parse(time.RFC3339, in.End)
		if today(now, start, end, open) && len(st.Today) < 10 {
			link := ""
			if in.URI != "" {
				link = "https://status.cloud.google.com/" + strings.TrimPrefix(in.URI, "/")
			}
			st.Today = append(st.Today, Incident{Name: clip(in.Desc), Impact: ind, StartMs: start.UnixMilli(),
				Resolved: !open, Shortlink: link})
		}
	}
	return nil
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

// Every is how often the feeds are read; incidents, the larger file, at
// most every IncidentsEvery.
const (
	Every          = 10 * time.Minute
	IncidentsEvery = 30 * time.Minute
)

// Checker keeps the last state of every provider it was asked about.
type Checker struct {
	mu     sync.Mutex
	states map[string]State
	busy   bool
	client *http.Client
}

// NewChecker returns an empty checker.
func NewChecker() *Checker { return &Checker{states: map[string]State{}, client: Client} }

// Check reads the providers' feeds, at most four at once. A provider
// backing off after failures is skipped until its time, unless force (the
// user asked). Only one check runs at a time.
func (c *Checker) Check(ctx context.Context, ids []string, force bool) {
	c.mu.Lock()
	if c.busy {
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.busy = false
		c.mu.Unlock()
	}()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	now := time.Now()
	for _, id := range ids {
		p, ok := Lookup(id)
		if !ok || p.API == "" {
			continue
		}
		c.mu.Lock()
		st := c.states[id]
		c.mu.Unlock()
		if !force && st.NextMs > now.UnixMilli() {
			continue
		}
		withIncidents := force || now.UnixMilli()-st.incidentsMs >= IncidentsEvery.Milliseconds()
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			next := Fetch(ctx, c.client, p, st, now, withIncidents)
			c.mu.Lock()
			c.states[id] = next
			c.mu.Unlock()
		}()
	}
	wg.Wait()
}

// States is the last state of each id; a provider never read is unknown.
func (c *Checker) States(ids []string) []State {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]State, 0, len(ids))
	for _, id := range ids {
		st, ok := c.states[id]
		if !ok {
			st = State{ID: id, Indicator: Unknown}
		}
		if st.Today == nil {
			st.Today = []Incident{}
		}
		out = append(out, st)
	}
	return out
}
