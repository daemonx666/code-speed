package pricing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

// The cases are Agent Speed's spec/fixtures/price-cases.json, copied
// unchanged, so this implementation is held to the same answers.
type caseTokens struct {
	Input             int64 `json:"input"`
	CachedInput       int64 `json:"cachedInput"`
	CacheWriteInput   int64 `json:"cacheWriteInput"`
	CacheWrite1hInput int64 `json:"cacheWrite1hInput"`
	Output            int64 `json:"output"`
	Reasoning         int64 `json:"reasoning"`
}

func (c caseTokens) tokens() usage.Tokens {
	return usage.Tokens{Input: c.Input, CachedInput: c.CachedInput, CacheWrite: c.CacheWriteInput,
		CacheWrite1h: c.CacheWrite1hInput, Output: c.Output, Reasoning: c.Reasoning}
}

type costCase struct {
	Name     string     `json:"name"`
	Table    string     `json:"table"`
	Model    string     `json:"model"`
	Provider *string    `json:"provider"`
	Tokens   caseTokens `json:"tokens"`
	At       string     `json:"at"`
	Fast     bool       `json:"fast"`
	Expect   struct {
		Pico  *int64 `json:"costPicoUsd"`
		Micro *int64 `json:"costMicroUsd"`
		Tier  *int   `json:"tier"`
	} `json:"expect"`
}

type cases struct {
	Tables  map[string]string `json:"tables"`
	Resolve []struct {
		Table    string  `json:"table"`
		Model    string  `json:"model"`
		Provider *string `json:"provider"`
		Expect   string  `json:"expect"`
	} `json:"resolve"`
	Cost    []costCase `json:"cost"`
	Bundled struct {
		Cost []costCase `json:"cost"`
	} `json:"bundled"`
}

func loadCases(t *testing.T) (cases, map[string]*Table) {
	t.Helper()
	b, err := os.ReadFile("testdata/price-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c cases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	tables := map[string]*Table{}
	for name, path := range c.Tables {
		raw, err := os.ReadFile(filepath.Join("testdata", path))
		if err != nil {
			t.Fatal(err)
		}
		if tables[name], err = Load(raw); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return c, tables
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func describe(r Resolution) string {
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

func TestResolveCases(t *testing.T) {
	c, tables := loadCases(t)
	for _, rc := range c.Resolve {
		got := describe(tables[rc.Table].Resolve(rc.Model, str(rc.Provider)))
		if got != rc.Expect {
			t.Errorf("%s: Resolve(%q, %q) = %q, want %q", rc.Table, rc.Model, str(rc.Provider), got, rc.Expect)
		}
	}
}

func atMs(t *testing.T, s string) int64 {
	if s == "" {
		return 0
	}
	ms, err := parseBound(s)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func checkCost(t *testing.T, tb *Table, cc costCase) {
	t.Helper()
	got := tb.CostOf(cc.Model, str(cc.Provider), cc.Tokens.tokens(), cc.Fast, atMs(t, cc.At))
	if cc.Expect.Micro == nil {
		if got.Known {
			t.Errorf("%s: cost %d µUSD, want unknown", cc.Name, got.Micro)
		}
		return
	}
	if !got.Known || got.Pico != *cc.Expect.Pico || got.Micro != *cc.Expect.Micro {
		t.Errorf("%s: cost %+v, want pico %d micro %d", cc.Name, got, *cc.Expect.Pico, *cc.Expect.Micro)
	}
	wantTier := -1
	if cc.Expect.Tier != nil {
		wantTier = *cc.Expect.Tier
	}
	if got.Tier != wantTier {
		t.Errorf("%s: tier %d, want %d", cc.Name, got.Tier, wantTier)
	}
}

func TestCostCases(t *testing.T) {
	c, tables := loadCases(t)
	for _, cc := range c.Cost {
		checkCost(t, tables[cc.Table], cc)
	}
}

func TestBundledCostCases(t *testing.T) {
	c, _ := loadCases(t)
	for _, cc := range c.Bundled.Cost {
		checkCost(t, Bundled, cc)
	}
}

// The bundled table's own tests list.
func TestBundledTableTests(t *testing.T) {
	var f struct {
		Tests []struct {
			Model    string  `json:"model"`
			Provider *string `json:"provider"`
			Expect   string  `json:"expect"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(bundledJSON, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Tests) == 0 {
		t.Fatal("bundled table has no tests")
	}
	for _, tc := range f.Tests {
		if got := describe(Bundled.Resolve(tc.Model, str(tc.Provider))); got != tc.Expect {
			t.Errorf("Resolve(%q) = %q, want %q", tc.Model, got, tc.Expect)
		}
	}
}

func TestResponseCostUsesEndTime(t *testing.T) {
	r := usage.Response{Model: "claude-opus-5-5", EndMs: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
		Tokens: usage.Tokens{Input: 1_000_000, Output: 1_000_000}}
	c := Bundled.ResponseCost(r)
	if !c.Known || c.Micro != 24_000_000 { // $4 in + $20 out
		t.Fatalf("cost = %+v, want 24 USD", c)
	}
}

func TestCustomPrices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-prices.json")
	os.WriteFile(path, []byte(`{
	  "models": [{"id": "Relay-Opus", "aliases": ["relay-opus-latest"], "prices": {"input": 1, "output": 5}},
	             {"id": "claude-opus-5-5", "prices": {"input": 2, "output": 10}}],
	  "multipliers": {"sources": {"codex": 0.5}, "providers": {"myrelay": 0.25}}
	}`), 0o600)
	c, err := LoadCustom(path)
	if err != nil {
		t.Fatal(err)
	}
	tb, err := Bundled.With(c)
	if err != nil {
		t.Fatal(err)
	}
	tk := usage.Tokens{Input: 1_000_000, Output: 1_000_000}
	// a model the bundled table lacks, by id and alias
	for _, m := range []string{"relay-opus", "relay-opus-latest"} {
		if got := tb.ResponseCost(usage.Response{Model: m, Tokens: tk}); !got.Known || got.Micro != 6_000_000 {
			t.Fatalf("%s: %+v", m, got)
		}
	}
	// a custom price wins over the bundled one, which is untouched
	if got := tb.ResponseCost(usage.Response{Model: "claude-opus-5-5", Tokens: tk}); got.Micro != 12_000_000 {
		t.Fatalf("override: %+v", got)
	}
	if got := Bundled.ResponseCost(usage.Response{Model: "claude-opus-5-5", Tokens: tk}); got.Micro != 24_000_000 {
		t.Fatalf("bundled changed: %+v", got)
	}
	// multipliers: the agent's, else the provider's
	if got := tb.ResponseCost(usage.Response{Source: usage.Codex, Model: "relay-opus", Tokens: tk}); got.Micro != 3_000_000 {
		t.Fatalf("source multiplier: %+v", got)
	}
	if got := tb.ResponseCost(usage.Response{Provider: "MyRelay", Model: "relay-opus", Tokens: tk}); got.Micro != 1_500_000 {
		t.Fatalf("provider multiplier: %+v", got)
	}
	// unknown stays unknown, multiplied or not
	if got := tb.ResponseCost(usage.Response{Source: usage.Codex, Model: "nobody-prices-this", Tokens: tk}); got.Known {
		t.Fatalf("unknown priced: %+v", got)
	}
	if c, err := LoadCustom(filepath.Join(t.TempDir(), "missing.json")); c != nil || err != nil {
		t.Fatal("a missing file is no custom prices")
	}
	os.WriteFile(path, []byte(`{"multipliers": {"sources": {"codex": -1}}}`), 0o600)
	if _, err := LoadCustom(path); err == nil {
		t.Fatal("a negative multiplier was accepted")
	}
}
