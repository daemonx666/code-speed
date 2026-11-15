// Ported from magpie (internal/library/mcp.go: mcpFormat, key, supports,
// owned), MIT License, (c) 2026 yetone.

package mcpfmt

import (
	"errors"
	"fmt"
)

// Format is how an agent lays out its MCP servers in its file.
type Format string

const (
	Claude      Format = "claude"      // mcpServers; type stdio|http|sse (also Droid, WorkBuddy)
	Codex       Format = "codex"       // TOML [mcp_servers.<name>]
	Gemini      Format = "gemini"      // mcpServers; httpUrl or url (also Qwen Code)
	OpenCode    Format = "opencode"    // "mcp": type local|remote, command is a list
	Cursor      Format = "cursor"      // mcpServers; command or url
	Copilot     Format = "copilot"     // mcpServers; type local|http|sse, tools ["*"]
	Goose       Format = "goose"       // YAML extensions: map
	PiNative    Format = "piNative"    // mcpServers in Pi's own mcp.json; no sse
	Omp         Format = "omp"         // mcpServers; type http|sse beside a url (also Qoder)
	Antigravity Format = "antigravity" // mcpServers; serverUrl
	ZCode       Format = "zcode"       // mcp.servers
	Hermes      Format = "hermes"      // YAML mcp_servers
	Kimi        Format = "kimi"        // mcpServers; transport http|sse beside a url
	Grok        Format = "grok"        // TOML [mcp_servers.<name>]; no sse
	Cline       Format = "cline"       // mcpServers; transport object (stdio|streamableHttp|sse)
)

// Formats lists every format, in the order above.
var Formats = []Format{Claude, Codex, Gemini, OpenCode, Cursor, Copilot, Goose, PiNative, Omp, Antigravity, ZCode, Hermes, Kimi, Grok, Cline}

func (f Format) known() bool {
	for _, k := range Formats {
		if f == k {
			return true
		}
	}
	return false
}

func (f Format) toml() bool { return f == Codex || f == Grok }
func (f Format) yaml() bool { return f == Goose || f == Hermes }

// key is the object the servers are kept under, as a dot path.
func (f Format) key() string {
	switch f {
	case Codex, Hermes, Grok:
		return "mcp_servers"
	case OpenCode:
		return "mcp"
	case Goose:
		return "extensions"
	case ZCode:
		return "mcp.servers"
	}
	return "mcpServers"
}

var (
	// ErrNoSSE is why an agent that can't reach a server over SSE (Codex,
	// Goose, Pi's own MCP, Grok Build: its type = "sse" is taken, but it
	// speaks streamable HTTP all the same) isn't given one.
	ErrNoSSE = errors.New("it can't reach a server over SSE")
	// errOpenCodeName: mcp.servers is OpenCode 2's own object.
	errOpenCodeName = errors.New(`"servers" is the name OpenCode 2 keeps its own servers under`)
)

// supports says why the format can't hold the server, or nil.
func (f Format) supports(s *Server) error {
	if !f.known() {
		return fmt.Errorf("unknown format %q", string(f))
	}
	if s.Transport == "sse" && (f == Codex || f == Goose || f == PiNative || f == Grok) {
		return ErrNoSSE
	}
	if f == OpenCode && s.Name == "servers" {
		return errOpenCodeName
	}
	return refsProblem(f, s)
}

// owned are the keys of an entry that say what the server is: a write sets
// them. An entry's other keys (a timeout, the tools it may call, disabled,
// autoApprove) are the user's, and kept when the server is written again.
var owned = map[Format][]string{
	Claude:   {"type", "url", "headers", "command", "args", "env"},
	Gemini:   {"type", "httpUrl", "url", "headers", "command", "args", "env"},
	OpenCode: {"type", "url", "headers", "command", "environment", "enabled"},
	Cursor:   {"type", "url", "headers", "command", "args", "env"},
	Copilot:  {"type", "url", "headers", "command", "args", "env"},
	Goose:    {"enabled", "name", "type", "uri", "headers", "cmd", "args", "envs"},
	// and bearer_token_env_var, env_http_headers or env_vars while the
	// server references a variable there (refKeys)
	Codex: {"url", "http_headers", "command", "args", "env"},
	// the adapter's transport keys too, dropped when the entry is written
	// again
	PiNative: {"type", "transport", "httpTransport", "url", "headers", "command", "args", "env"},
	ZCode:    {"type", "url", "headers", "command", "args", "env"},
	// timeout, enabled, tools, sampling, auth are the user's
	Hermes: {"transport", "url", "headers", "command", "args", "env"},
	Omp:    {"type", "url", "headers", "command", "args", "env"},
	Kimi:   {"transport", "type", "url", "headers", "command", "args", "env"},
	// enabled and the timeouts are the user's
	Grok: {"type", "url", "headers", "command", "args", "env"},
	// the flat shape's keys go when the entry is written again; disabled and
	// autoApprove are the user's
	Cline: {"transport", "type", "transportType", "url", "headers", "command", "args", "cwd", "env"},
	// a url agy read in place of serverUrl goes when one is written
	Antigravity: {"type", "serverUrl", "url", "headers", "command", "args", "env"},
}
