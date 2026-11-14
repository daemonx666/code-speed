package droid

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/daemonx666/code-speed/internal/source"
)

// counters are the five cumulative fields of a sidecar's tokenUsage, in
// the order input, cache write, cache read, output, thinking.
type counters [5]int64

const (
	cIn = iota
	cCacheWrite
	cCacheRead
	cOut
	cThink
)

const maxSidecar = 1 << 20

type sidecar struct {
	model    string // the session model ("" for none)
	counters counters
	has      bool // tokenUsage is an object
}

type sidecarStatus int

const (
	sideOK sidecarStatus = iota
	sideMissing
	sideUnreadable
)

// readSidecar reads a whole sidecar: a regular file (a symlink is not
// followed) of at most 1 MiB, valid UTF-8 (one leading U+FEFF removed)
// and a JSON object.
func readSidecar(path string) (sidecar, sidecarStatus) {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return sidecar{}, sideMissing
		}
		return sidecar{}, sideUnreadable
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxSidecar {
		return sidecar{}, sideUnreadable
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return sidecar{}, sideMissing
		}
		return sidecar{}, sideUnreadable
	}
	sc, ok := parseSidecar(b)
	if !ok {
		return sidecar{}, sideUnreadable
	}
	return sc, sideOK
}

func parseSidecar(b []byte) (sidecar, bool) {
	if len(b) > maxSidecar || !utf8.Valid(b) {
		return sidecar{}, false
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	var doc struct {
		Model     json.RawMessage `json:"model"`
		Router    json.RawMessage `json:"effectiveFactoryRouterModel"`
		TokenUses json.RawMessage `json:"tokenUsage"`
	}
	if t := bytes.TrimSpace(b); len(t) == 0 || t[0] != '{' {
		return sidecar{}, false
	}
	if json.Unmarshal(b, &doc) != nil {
		return sidecar{}, false
	}
	var sc sidecar
	var router struct {
		ModelID json.RawMessage `json:"modelId"`
	}
	if t := bytes.TrimSpace(doc.Router); len(t) > 0 && t[0] == '{' {
		_ = json.Unmarshal(t, &router)
	}
	if m := strings.TrimSpace(rawString(router.ModelID)); m != "" {
		sc.model = m
	} else if m := strings.TrimSpace(rawString(doc.Model)); m != "" && !strings.EqualFold(m, "auto") {
		sc.model = m
	}
	if t := bytes.TrimSpace(doc.TokenUses); len(t) > 0 && t[0] == '{' {
		var raw map[string]source.Tok
		if json.Unmarshal(t, &raw) == nil {
			sc.has = true
			sc.counters = counters{
				int64(raw["inputTokens"]), int64(raw["cacheCreationTokens"]), int64(raw["cacheReadTokens"]),
				int64(raw["outputTokens"]), int64(raw["thinkingTokens"]),
			}
		}
	}
	return sc, true
}

// rawString is a JSON string's value, "" for anything else.
func rawString(r json.RawMessage) string {
	var s string
	if len(r) == 0 || r[0] != '"' || json.Unmarshal(r, &s) != nil {
		return ""
	}
	return s
}
