// Package pricing turns a response's model and tokens into an
// API-equivalent cost in USD.
//
// The bundled table (prices.json) and the resolution and cost rules come
// from Agent Speed's specification (spec/METRICS.md §9, MIT,
// github.com/zoefix/agent-speed). Costs are integers: pico-USD while
// summing a response, micro-USD everywhere after, so every grouping of
// the same responses adds up to the same total.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/daemonx666/code-speed/internal/usage"
)

//go:embed prices.json
var bundledJSON []byte

// Prices are pico-USD per token (= micro-USD per million tokens).
type Prices struct {
	Input, CachedInput, CacheWrite, CacheWrite1h, Output int64
}

type tier struct {
	Above  int64
	Prices Prices
}

type offPeak struct {
	peak     []dayWindow
	weekdays map[int]bool
	dates    map[string]bool
	prices   Prices
	tiers    []tier
}

type dayWindow struct{ from, to int } // minutes of the UTC day, [from, to)

type period struct {
	from, to       int64 // Unix ms; 0 = unbounded
	hasFrom, hasTo bool
	prices         Prices
	tiers          []tier
	offPeak        *offPeak
}

// Model is one priced entry of the table.
type Model struct {
	ID       string
	Provider string
	Name     string
	Free     bool
	Custom   bool // from the user's custom-prices.json
	prices   Prices
	tiers    []tier
	offPeak  *offPeak
	periods  []period
	fast     *struct {
		prices Prices
		tiers  []tier
	}
}

// Table is a loaded price table.
type Table struct {
	Version int
	Updated string
	byKey   map[string]*Model

	freeProviders, cloudSuffixes, bedrockRegions, bedrockVendors []string
	freeSuffixes, variantSuffixes, fastSuffixes                  []string
	dateSuffixPrefixes, dotHyphenPrefixes                        []string
	rewrites                                                     []struct{ Prefix, To string }
	modifierSuffixes                                             []struct {
		Prefix   string
		Suffixes []string
	}
	unknownExact  map[string]string
	unknownPrefix []struct{ value, reason string }

	// from the user's custom prices (With)
	sourceMult   map[usage.Source]float64
	providerMult map[string]float64
}

// Bundled is the table compiled into the binary.
var Bundled = mustLoad(bundledJSON)

func mustLoad(b []byte) *Table {
	t, err := Load(b)
	if err != nil {
		panic("pricing: bundled table: " + err.Error())
	}
	return t
}

type rawPrices map[string]float64

type rawTier map[string]float64

type rawOffPeak struct {
	PeakUtc      []string  `json:"peakUtc"`
	PeakWeekdays []int     `json:"peakWeekdays"`
	OffPeakDates []string  `json:"offPeakDates"`
	Prices       rawPrices `json:"prices"`
	Tiers        []rawTier `json:"tiers"`
}

type rawModel struct {
	ID       string      `json:"id"`
	Provider string      `json:"provider"`
	Name     string      `json:"name"`
	Free     bool        `json:"free"`
	Aliases  []string    `json:"aliases"`
	Prices   rawPrices   `json:"prices"`
	Tiers    []rawTier   `json:"tiers"`
	OffPeak  *rawOffPeak `json:"offPeak"`
	Fast     *struct {
		Prices rawPrices `json:"prices"`
		Tiers  []rawTier `json:"tiers"`
	} `json:"fast"`
	Periods []struct {
		From    string      `json:"from"`
		To      string      `json:"to"`
		Prices  rawPrices   `json:"prices"`
		Tiers   []rawTier   `json:"tiers"`
		OffPeak *rawOffPeak `json:"offPeak"`
	} `json:"periods"`
}

type rawFile struct {
	Schema    int    `json:"schema"`
	Version   int    `json:"version"`
	Updated   string `json:"updated"`
	Normalize struct {
		FreeProviders      []string                      `json:"freeProviders"`
		CloudSuffixes      []string                      `json:"cloudSuffixes"`
		BedrockRegions     []string                      `json:"bedrockRegions"`
		BedrockVendors     []string                      `json:"bedrockVendors"`
		FreeSuffixes       []string                      `json:"freeSuffixes"`
		VariantSuffixes    []string                      `json:"variantSuffixes"`
		FastSuffixes       []string                      `json:"fastSuffixes"`
		Rewrites           []struct{ Prefix, To string } `json:"rewrites"`
		DateSuffixPrefixes []string                      `json:"dateSuffixPrefixes"`
		ModifierSuffixes   []struct {
			Prefix   string   `json:"prefix"`
			Suffixes []string `json:"suffixes"`
		} `json:"modifierSuffixes"`
		DotHyphenPrefixes []string `json:"dotHyphenPrefixes"`
	} `json:"normalize"`
	Models  []rawModel `json:"models"`
	Unknown []struct {
		Match, Value, Reason string
	} `json:"unknown"`
}

// Load parses a price table.
func Load(b []byte) (*Table, error) {
	var f rawFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if f.Schema != 1 {
		return nil, fmt.Errorf("unsupported schema %d", f.Schema)
	}
	n := f.Normalize
	t := &Table{
		Version: f.Version, Updated: f.Updated, byKey: map[string]*Model{},
		freeProviders: n.FreeProviders, cloudSuffixes: n.CloudSuffixes,
		bedrockRegions: n.BedrockRegions, bedrockVendors: n.BedrockVendors,
		freeSuffixes: n.FreeSuffixes, variantSuffixes: n.VariantSuffixes, fastSuffixes: n.FastSuffixes,
		dateSuffixPrefixes: n.DateSuffixPrefixes, dotHyphenPrefixes: n.DotHyphenPrefixes,
		rewrites: n.Rewrites, unknownExact: map[string]string{},
	}
	for _, m := range n.ModifierSuffixes {
		t.modifierSuffixes = append(t.modifierSuffixes, struct {
			Prefix   string
			Suffixes []string
		}{m.Prefix, m.Suffixes})
	}
	for _, u := range f.Unknown {
		if u.Match == "prefix" {
			t.unknownPrefix = append(t.unknownPrefix, struct{ value, reason string }{u.Value, u.Reason})
		} else {
			t.unknownExact[u.Value] = u.Reason
		}
	}
	for _, rm := range f.Models {
		m, err := buildModel(rm)
		if err != nil {
			return nil, err
		}
		t.byKey[m.ID] = m
		for _, a := range rm.Aliases {
			t.byKey[a] = m
		}
	}
	return t, nil
}

func pico(v float64) int64 { return int64(math.Round(v * 1e6)) }

func convert(r rawPrices) Prices {
	p := Prices{Input: pico(r["input"]), Output: pico(r["output"])}
	p.CachedInput = p.Input
	if v, ok := r["cachedInput"]; ok {
		p.CachedInput = pico(v)
	}
	p.CacheWrite = p.Input
	if v, ok := r["cacheWrite"]; ok {
		p.CacheWrite = pico(v)
	}
	p.CacheWrite1h = p.CacheWrite
	if v, ok := r["cacheWrite1h"]; ok {
		p.CacheWrite1h = pico(v)
	}
	return p
}

func convertTiers(rs []rawTier) []tier {
	var ts []tier
	for _, r := range rs {
		ts = append(ts, tier{Above: int64(r["above"]), Prices: convert(rawPrices(r))})
	}
	return ts
}

func convertOffPeak(r *rawOffPeak) (*offPeak, error) {
	if r == nil {
		return nil, nil
	}
	o := &offPeak{weekdays: map[int]bool{}, dates: map[string]bool{}, prices: convert(r.Prices), tiers: convertTiers(r.Tiers)}
	for _, w := range r.PeakUtc {
		var h1, m1, h2, m2 int
		if _, err := fmt.Sscanf(w, "%d:%d-%d:%d", &h1, &m1, &h2, &m2); err != nil {
			return nil, fmt.Errorf("peakUtc %q: %w", w, err)
		}
		o.peak = append(o.peak, dayWindow{h1*60 + m1, h2*60 + m2})
	}
	for _, d := range r.PeakWeekdays {
		o.weekdays[d] = true
	}
	for _, d := range r.OffPeakDates {
		o.dates[d] = true
	}
	return o, nil
}

func parseBound(s string) (int64, error) {
	for _, layout := range []string{"2006-01-02T15:04Z07:00", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("bad period bound %q", s)
}

// Kind is what resolving a model gave.
type Kind int

const (
	Unknown Kind = iota
	Free
	Priced
)

// Resolution is a model id matched against the table.
type Resolution struct {
	Kind   Kind
	Model  *Model
	Fast   bool   // the id itself selects fast mode
	Reason string // why it is unknown, when the table says
}

var bracketGroup = regexp.MustCompile(`\[[^\]]*\]`)
var trailingIndex = regexp.MustCompile(`-[0-9]+$`)
var bedrockVersion = regexp.MustCompile(`-v[0-9]+(:[0-9]+)?$`)
var atDate = regexp.MustCompile(`@([0-9]{8})$`)
var dateSuffix = regexp.MustCompile(`-[0-9]{8}$`)

// Resolve matches a model id (and the provider its agent named, if any)
// to a table entry. There is no fuzzy matching: an id the rules don't
// reach is unknown, never priced as something close to it.
func (t *Table) Resolve(model, provider string) Resolution {
	p := strings.ToLower(strings.TrimSpace(provider))
	lm := strings.ToLower(model)
	if contains(t.freeProviders, p) && !hasAnySuffix(lm, t.cloudSuffixes) {
		return Resolution{Kind: Free}
	}
	if strings.TrimSpace(model) == "" {
		return Resolution{Kind: Unknown}
	}
	fast := false
	// try looks a key up; done reports that the key decided the result
	try := func(key string) (Resolution, bool) {
		if m, ok := t.byKey[key]; ok {
			if m.Free {
				return Resolution{Kind: Free, Model: m}, true
			}
			if fast && m.fast == nil {
				return Resolution{Kind: Unknown, Reason: "no fast-mode price"}, true
			}
			return Resolution{Kind: Priced, Model: m, Fast: fast}, true
		}
		if r, ok := t.unknownExact[key]; ok {
			return Resolution{Kind: Unknown, Reason: r}, true
		}
		for _, u := range t.unknownPrefix {
			if strings.HasPrefix(key, u.value) {
				return Resolution{Kind: Unknown, Reason: u.reason}, true
			}
		}
		return Resolution{}, false
	}

	// exact
	key := strings.ToLower(strings.Join(strings.Fields(bracketGroup.ReplaceAllString(model, "")), " "))
	if r, ok := try(key); ok {
		return r
	}
	// custom
	if rest, ok := strings.CutPrefix(key, "custom:"); ok {
		key = strings.Trim(trailingIndex.ReplaceAllString(rest, ""), " -")
		if r, ok := try(key); ok {
			return r
		}
	}
	// route
	if i := strings.LastIndex(key, "/"); i >= 0 {
		key = key[i+1:]
		if r, ok := try(key); ok {
			return r
		}
	}
	// cloud
	if k := t.cloud(key); k != key {
		key = k
		if r, ok := try(key); ok {
			return r
		}
	}
	// free
	for _, s := range t.freeSuffixes {
		if len(key) > len(s) && strings.HasSuffix(key, s) {
			return Resolution{Kind: Free}
		}
	}
	// variant
	if k := trimSuffixes(key, t.variantSuffixes, ""); k != key {
		key = k
		if r, ok := try(key); ok {
			return r
		}
	}
	// rewrite
	for _, rw := range t.rewrites {
		if strings.HasPrefix(key, rw.Prefix) {
			key = rw.To + key[len(rw.Prefix):]
			if r, ok := try(key); ok {
				return r
			}
			break
		}
	}
	// fast
	for _, s := range t.fastSuffixes {
		if len(key) > len(s) && strings.HasSuffix(key, s) {
			key, fast = key[:len(key)-len(s)], true
			if r, ok := try(key); ok {
				return r
			}
			break
		}
	}
	// date
	if hasAnyPrefix(key, t.dateSuffixPrefixes) && dateSuffix.MatchString(key) {
		key = dateSuffix.ReplaceAllString(key, "")
		if r, ok := try(key); ok {
			return r
		}
	}
	// modifier
	for _, ms := range t.modifierSuffixes {
		if strings.HasPrefix(key, ms.Prefix) {
			if k := trimSuffixes(key, ms.Suffixes, ms.Prefix); k != key {
				key = k
				if r, ok := try(key); ok {
					return r
				}
			}
			break
		}
	}
	// dotHyphen
	if hasAnyPrefix(key, t.dotHyphenPrefixes) && strings.Contains(key, ".") {
		key = strings.ReplaceAll(key, ".", "-")
		if r, ok := try(key); ok {
			return r
		}
	}
	return Resolution{Kind: Unknown}
}

// cloud strips Bedrock and Vertex decorations: a region before a vendor,
// a leading vendor with its version suffix, and an @ suffix.
func (t *Table) cloud(key string) string {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) >= 3 && contains(t.bedrockRegions, parts[0]) && contains(t.bedrockVendors, parts[1]) {
		key = strings.TrimPrefix(key, parts[0]+".")
	}
	if i := strings.Index(key, "."); i > 0 && contains(t.bedrockVendors, key[:i]) {
		key = bedrockVersion.ReplaceAllString(key[i+1:], "")
	}
	if m := atDate.FindStringSubmatch(key); m != nil {
		key = key[:len(key)-len(m[0])] + "-" + m[1]
	} else if i := strings.LastIndex(key, "@"); i >= 0 {
		key = key[:i]
	}
	return key
}

// trimSuffixes removes any of suffixes from the end repeatedly, as long as
// more than keep remains.
func trimSuffixes(key string, suffixes []string, keep string) string {
	for changed := true; changed; {
		changed = false
		for _, s := range suffixes {
			if strings.HasSuffix(key, s) && len(key)-len(s) > len(keep) {
				key, changed = key[:len(key)-len(s)], true
			}
		}
	}
	return key
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func hasAnySuffix(s string, list []string) bool {
	for _, v := range list {
		if strings.HasSuffix(s, v) {
			return true
		}
	}
	return false
}

func hasAnyPrefix(s string, list []string) bool {
	for _, v := range list {
		if strings.HasPrefix(s, v) {
			return true
		}
	}
	return false
}

// Cost is a response's API-equivalent cost.
type Cost struct {
	Known bool  // false: the model has no price, never 0
	Pico  int64 // pico-USD
	Micro int64 // micro-USD, rounded half up
	Tier  int   // index of the tier used, -1 for the base prices
}

const classCap = 100_000_000

// CostOf prices tokens for a model at atMs (Unix ms; 0 = no period or
// off-peak rule applies). fast is the response's own fast flag.
func (t *Table) CostOf(model, provider string, tk usage.Tokens, fast bool, atMs int64) Cost {
	res := t.Resolve(model, provider)
	switch res.Kind {
	case Free:
		return Cost{Known: true, Tier: -1}
	case Unknown:
		return Cost{Tier: -1}
	}
	m := res.Model
	prices, tiers := m.prices, m.tiers
	if fast || res.Fast {
		if m.fast == nil {
			return Cost{Tier: -1}
		}
		prices, tiers = m.fast.prices, m.fast.tiers
	} else {
		op := m.offPeak
		if atMs != 0 {
			for _, p := range m.periods {
				if (!p.hasFrom || p.from <= atMs) && (!p.hasTo || atMs < p.to) {
					prices, tiers, op = p.prices, p.tiers, p.offPeak
					break
				}
			}
			if op != nil && !op.isPeak(atMs) {
				prices, tiers = op.prices, op.tiers
			}
		}
	}
	tierIdx := -1
	for i, tr := range tiers {
		if tr.Above < tk.Input {
			prices, tierIdx = tr.Prices, i
		}
	}
	clamp := func(v int64) int64 { return min(max(v, 0), classCap) }
	uncached := clamp(tk.Input - tk.CachedInput - tk.CacheWrite)
	read := clamp(tk.CachedInput)
	w1h := clamp(min(tk.CacheWrite1h, tk.CacheWrite))
	w := clamp(tk.CacheWrite - min(tk.CacheWrite1h, tk.CacheWrite))
	out := clamp(tk.Output)
	p := uncached*prices.Input + read*prices.CachedInput + w*prices.CacheWrite + w1h*prices.CacheWrite1h + out*prices.Output
	return Cost{Known: true, Pico: p, Micro: (p + 500_000) / 1_000_000, Tier: tierIdx}
}

// ResponseCost prices a response at its end time, scaled by the user's
// multiplier for its agent or provider, if any.
func (t *Table) ResponseCost(r usage.Response) Cost {
	c := t.CostOf(r.Model, r.Provider, r.Tokens, r.Fast, r.EndMs)
	if f, ok := t.multiplier(r); ok && c.Known {
		c.Pico = int64(float64(c.Pico) * f)
		c.Micro = (c.Pico + 500_000) / 1_000_000
	}
	return c
}

func (o *offPeak) isPeak(atMs int64) bool {
	at := time.UnixMilli(atMs).UTC()
	if o.dates[at.Format("2006-01-02")] {
		return false
	}
	wd := int(at.Weekday())
	if wd == 0 {
		wd = 7 // ISO: Monday 1 … Sunday 7
	}
	if !o.weekdays[wd] {
		return false
	}
	min := at.Hour()*60 + at.Minute()
	for _, w := range o.peak {
		if min >= w.from && min < w.to {
			return true
		}
	}
	return false
}

// buildModel turns a price-table entry into a Model.
func buildModel(rm rawModel) (*Model, error) {
	m := &Model{ID: rm.ID, Provider: rm.Provider, Name: rm.Name, Free: rm.Free}
	if m.Free {
		return m, nil
	}
	m.prices = convert(rm.Prices)
	m.tiers = convertTiers(rm.Tiers)
	op, err := convertOffPeak(rm.OffPeak)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", rm.ID, err)
	}
	m.offPeak = op
	if rm.Fast != nil {
		m.fast = &struct {
			prices Prices
			tiers  []tier
		}{convert(rm.Fast.Prices), convertTiers(rm.Fast.Tiers)}
	}
	for _, rp := range rm.Periods {
		p := period{prices: convert(rp.Prices), tiers: convertTiers(rp.Tiers)}
		if rp.From != "" {
			p.hasFrom = true
			if p.from, err = parseBound(rp.From); err != nil {
				return nil, fmt.Errorf("%s: %w", rm.ID, err)
			}
		}
		if rp.To != "" {
			p.hasTo = true
			if p.to, err = parseBound(rp.To); err != nil {
				return nil, fmt.Errorf("%s: %w", rm.ID, err)
			}
		}
		if p.offPeak, err = convertOffPeak(rp.OffPeak); err != nil {
			return nil, fmt.Errorf("%s: %w", rm.ID, err)
		}
		m.periods = append(m.periods, p)
	}
	return m, nil
}
