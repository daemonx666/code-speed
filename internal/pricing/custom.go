package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"strings"

	"github.com/daemonx666/code-speed/internal/usage"
)

// Custom is the user's own prices (custom-prices.json in code-speed's
// folder): models the bundled table lacks or prices differently (an API
// relay, a private deployment), and multipliers for what an agent or a
// provider really charges against the list price.
//
//	{
//	  "models": [{"id": "my-model", "aliases": ["my-model-latest"],
//	              "prices": {"input": 1, "cachedInput": 0.1, "output": 4}}],
//	  "multipliers": {"sources": {"claude-code": 0.3}, "providers": {"custom": 0.5}}
//	}
//
// Model entries have the bundled table's form (USD per million tokens,
// with optional tiers, fast and periods) and win over it. A multiplier
// scales a response's cost: its source's, else its provider's.
type Custom struct {
	Models      []rawModel `json:"models"`
	Multipliers struct {
		Sources   map[usage.Source]float64 `json:"sources"`
		Providers map[string]float64       `json:"providers"`
	} `json:"multipliers"`
}

// LoadCustom reads a custom prices file; a missing file is none.
func LoadCustom(path string) (*Custom, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c Custom
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	for _, m := range c.Models {
		if strings.TrimSpace(m.ID) == "" {
			return nil, fmt.Errorf("%s: a model without id", path)
		}
	}
	for k, v := range c.Multipliers.Sources {
		if !(v >= 0 && v <= 100) {
			return nil, fmt.Errorf("%s: multiplier for %s must be 0…100", path, k)
		}
	}
	for k, v := range c.Multipliers.Providers {
		if !(v >= 0 && v <= 100) {
			return nil, fmt.Errorf("%s: multiplier for %s must be 0…100", path, k)
		}
	}
	return &c, nil
}

// Models is how many models the file prices.
func (c *Custom) Count() int {
	if c == nil {
		return 0
	}
	return len(c.Models)
}

// With returns a copy of t with c's models and multipliers on top.
func (t *Table) With(c *Custom) (*Table, error) {
	if c == nil {
		return t, nil
	}
	n := *t
	n.byKey = maps.Clone(t.byKey)
	for _, rm := range c.Models {
		m, err := buildModel(rm)
		if err != nil {
			return nil, err
		}
		m.Custom = true
		id := strings.ToLower(strings.TrimSpace(rm.ID))
		m.ID = id
		n.byKey[id] = m
		for _, a := range rm.Aliases {
			n.byKey[strings.ToLower(strings.TrimSpace(a))] = m
		}
	}
	n.sourceMult = map[usage.Source]float64{}
	maps.Copy(n.sourceMult, c.Multipliers.Sources)
	n.providerMult = map[string]float64{}
	for k, v := range c.Multipliers.Providers {
		n.providerMult[strings.ToLower(k)] = v
	}
	return &n, nil
}

// multiplier is what a response's cost is scaled by.
func (t *Table) multiplier(r usage.Response) (float64, bool) {
	if v, ok := t.sourceMult[r.Source]; ok {
		return v, true
	}
	if v, ok := t.providerMult[strings.ToLower(r.Provider)]; ok {
		return v, true
	}
	return 1, false
}
