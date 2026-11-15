// Package mcpfmt reads and writes MCP server entries in the config files of
// the coding agents the Skill & MCP library manages, one codec per file
// format, keeping everything of the file that is not the entry written.
//
// Ported from magpie (internal/library/mcp.go, envref.go, MIT License,
// (c) 2026 yetone), trimmed to the formats the library supports and with
// the library's own bookkeeping (hub, accounts, bundles...) left out.

package mcpfmt

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Server is one MCP server: a command an agent starts, or a URL it reaches.
type Server struct {
	Name string `json:"name"`
	// Transport is "stdio" for a command, "http" (streamable) or "sse" for a
	// URL.
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// Remote reports whether the server is reached by URL.
func (s *Server) Remote() bool { return s.Transport == "http" || s.Transport == "sse" }

// nameOK is what every agent takes as a server's name, and what is safe as a
// key path segment in all three file formats (no dot, no space).
var nameOK = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Validate says why the server can't be written, or nil.
func (s *Server) Validate() error {
	if err := checkName(s.Name); err != nil {
		return err
	}
	switch s.Transport {
	case "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("%s: a command is needed", s.Name)
		}
	case "http", "sse":
		u := strings.TrimSpace(s.URL)
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("%s: the URL has to start with http:// or https://", s.Name)
		}
	default:
		return fmt.Errorf("%s: unknown transport %q", s.Name, s.Transport)
	}
	for _, m := range []map[string]string{s.Env, s.Headers} {
		for k := range m {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("%s: an env or header name is empty", s.Name)
			}
		}
	}
	return nil
}

func checkName(name string) error {
	if !nameOK.MatchString(name) {
		return fmt.Errorf("server name %q: only letters, digits, - and _ are allowed", name)
	}
	return nil
}

// Hash is a stable hash of what the server is, normalized: the name is left
// out, and so is whatever its transport doesn't use (a URL server's command,
// a command server's headers), and spaces around the command and URL. Two
// agents holding the same server have the same Hash; an agent whose entry
// was edited by hand has another, which is how drift is told.
func (s *Server) Hash() string {
	h := sha256.New()
	field := func(k, v string) { fmt.Fprintf(h, "%s=%q\n", k, v) }
	pairs := func(k string, m map[string]string) {
		for _, name := range slices.Sorted(maps.Keys(m)) {
			field(k+"."+name, m[name])
		}
	}
	field("transport", s.Transport)
	if s.Remote() {
		field("url", strings.TrimSpace(s.URL))
		pairs("header", s.Headers)
	} else {
		field("command", strings.TrimSpace(s.Command))
		field("args", fmt.Sprint(len(s.Args)))
		for i, a := range s.Args {
			field(fmt.Sprintf("arg%d", i), a)
		}
		pairs("env", s.Env)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
