package cline

import (
	"encoding/json"
	"os"

	"github.com/daemonx666/code-speed/internal/usage"
)

type sessionFile struct {
	Messages []struct {
		ID        string `json:"id"`
		Role      string `json:"role"`
		Ts        int64  `json:"ts"`
		ModelInfo *struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"modelInfo"`
		Metrics *struct {
			InputTokens      int64 `json:"inputTokens"`
			OutputTokens     int64 `json:"outputTokens"`
			CacheReadTokens  int64 `json:"cacheReadTokens"`
			CacheWriteTokens int64 `json:"cacheWriteTokens"`
		} `json:"metrics"`
	} `json:"messages"`
}

// readSessionB turns one <stem>.messages.json (the main agent's, or a
// sub-agent's: the stem differs from the session id) into responses.
// UNVERIFIED: where the VS Code extension 4.x writes new sessions; the CLI
// and the SDK use <dataDir>/sessions, which is all that is read here.
func readSessionB(k kind, stem, path string) []usage.Response {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f sessionFile
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	var out []usage.Response
	var prev int64
	for _, m := range f.Messages {
		start := prev
		if m.Ts > 0 {
			prev = m.Ts
		}
		if m.Role != "assistant" || m.Metrics == nil || m.Ts <= 0 || m.ID == "" {
			continue
		}
		r := usage.Response{
			Key:    string(k.src) + ":b:" + stem + ":" + m.ID,
			Source: k.src,
			Agent:  path,
			EndMs:  m.Ts,
			Final:  true,
			Tokens: usage.Tokens{
				Input:       m.Metrics.InputTokens, // already includes the cache
				CachedInput: m.Metrics.CacheReadTokens,
				CacheWrite:  m.Metrics.CacheWriteTokens,
				Output:      m.Metrics.OutputTokens,
			},
		}
		if start > 0 && start <= m.Ts {
			r.StartMs = start
		}
		if m.ModelInfo != nil {
			r.Model, r.Provider = m.ModelInfo.ID, m.ModelInfo.Provider
		}
		out = append(out, r)
	}
	return out
}
