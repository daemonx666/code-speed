package cline

import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/daemonx666/code-speed/internal/usage"
)

type uiMessage struct {
	Ts        int64  `json:"ts"`
	Type      string `json:"type"`
	Say       string `json:"say"`
	Text      string `json:"text"`
	Partial   bool   `json:"partial"`
	ModelInfo *struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	} `json:"modelInfo"`
}

type apiReqInfo struct {
	TokensIn    *int64 `json:"tokensIn"`
	TokensOut   *int64 `json:"tokensOut"`
	CacheWrites int64  `json:"cacheWrites"`
	CacheReads  int64  `json:"cacheReads"`
}

type apiEntry struct {
	Role      string `json:"role"`
	Ts        int64  `json:"ts"`
	ModelInfo *struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	} `json:"modelInfo"`
	Metrics *struct {
		Tokens struct {
			Prompt     int64 `json:"prompt"`
			Completion int64 `json:"completion"`
			Cached     int64 `json:"cached"`
		} `json:"tokens"`
	} `json:"metrics"`
}

type taskMeta struct {
	ModelUsage []struct {
		Ts         int64  `json:"ts"`
		ModelID    string `json:"model_id"`
		ProviderID string `json:"model_provider_id"`
	} `json:"model_usage"`
}

type reqA struct {
	ts   int64
	info apiReqInfo
	msg  uiMessage
}

// readTaskA turns one legacy task folder into responses.
func readTaskA(k kind, id, uiPath, apiPath, metaPath, project string) []usage.Response {
	b, err := os.ReadFile(uiPath)
	if err != nil {
		return nil
	}
	var ui []uiMessage
	if json.Unmarshal(b, &ui) != nil {
		return nil
	}
	var reqs []reqA
	for _, m := range ui {
		if m.Say != "api_req_started" || m.Partial || m.Ts <= 0 || m.Text == "" {
			continue
		}
		var info apiReqInfo
		if json.Unmarshal([]byte(m.Text), &info) != nil {
			continue
		}
		// a request still in flight has no token counts yet
		if info.TokensIn == nil && info.TokensOut == nil {
			continue
		}
		reqs = append(reqs, reqA{m.Ts, info, m})
	}
	if len(reqs) == 0 {
		return nil
	}

	var api []apiEntry
	if b, err := os.ReadFile(apiPath); err == nil {
		_ = json.Unmarshal(b, &api)
	}
	var meta taskMeta
	if b, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(b, &meta)
	}

	// Pair the k-th request with the k-th assistant entry. Cline's entries
	// carry metrics (and a completion count to check against); the forks'
	// don't, so their entries must match the requests one to one.
	var asst []apiEntry
	for _, a := range api {
		if a.Role != "assistant" || a.Ts <= 0 {
			continue
		}
		if k.sdk && a.Metrics == nil {
			continue
		}
		asst = append(asst, a)
	}
	paired := k.sdk || len(asst) == len(reqs)

	out := make([]usage.Response, 0, len(reqs))
	for i, q := range reqs {
		in := q.info
		var tin, tout int64
		if in.TokensIn != nil {
			tin = *in.TokensIn
		}
		if in.TokensOut != nil {
			tout = *in.TokensOut
		}
		r := usage.Response{
			Key:     string(k.src) + ":a:" + id + ":" + strconv.FormatInt(q.ts, 10),
			Source:  k.src,
			Agent:   uiPath,
			Project: project,
			EndMs:   q.ts, // unmeasured: the start is all that is known
			Final:   true,
			Tokens: usage.Tokens{
				// ASSUMPTION: tokensIn excludes cache reads and writes (the
				// Anthropic convention). UNVERIFIED for OpenAI-compatible
				// providers, whose tokensIn may already include the cache.
				Input:       tin + in.CacheReads + in.CacheWrites,
				CachedInput: in.CacheReads,
				CacheWrite:  in.CacheWrites,
				Output:      tout,
			},
		}
		var a *apiEntry
		if paired && i < len(asst) {
			a = &asst[i]
			if k.sdk && (a.Metrics == nil || a.Metrics.Tokens.Completion != tout) {
				a = nil
			}
		}
		if a != nil && a.Ts >= q.ts {
			r.StartMs, r.EndMs = q.ts, a.Ts
		}
		if mi := q.msg.ModelInfo; mi != nil && mi.ModelID != "" {
			r.Model, r.Provider = mi.ModelID, mi.ProviderID
		} else if a != nil && a.ModelInfo != nil && a.ModelInfo.ModelID != "" {
			r.Model, r.Provider = a.ModelInfo.ModelID, a.ModelInfo.ProviderID
		} else {
			r.Model, r.Provider = metaModel(meta, q.ts)
		}
		out = append(out, r)
	}
	return out
}

// metaModel is the model of the latest task_metadata model_usage entry at
// or before ts, else the first one.
func metaModel(m taskMeta, ts int64) (model, provider string) {
	for i, u := range m.ModelUsage {
		if u.ModelID == "" {
			continue
		}
		if i == 0 || u.Ts <= ts {
			model, provider = u.ModelID, u.ProviderID
		}
	}
	return
}
