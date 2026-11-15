// Ported from magpie (internal/library/mcp.go: encode, decode, merged,
// ordered, TOML literals), MIT License, (c) 2026 yetone.

package mcpfmt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"

	"github.com/daemonx666/code-speed/internal/edit"
)

// ordered is an object that keeps its keys in the order given, so an entry
// reads the way the agent's own would.
type ordered []kv

type kv struct {
	k string
	v any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, e := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(e.k)
		v, err := json.Marshal(e.v)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// MarshalYAML writes the object as a YAML mapping in the same order.
func (o ordered) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, e := range o {
		var v yaml.Node
		if err := v.Encode(e.v); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.k}, &v)
	}
	return n, nil
}

func strs(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func list(a []string) []string {
	if a == nil {
		return []string{}
	}
	return a
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func strList(v any) []string {
	a, _ := v.([]any)
	var out []string
	for _, x := range a {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, x := range m {
		out[k] = fmt.Sprint(x)
	}
	return out
}

// encode is the server as this format writes it.
func (f Format) encode(s *Server) ordered {
	var o ordered
	r := refsOf(f)
	c := *s
	c.Headers, c.Env = mapTo(r, s.Headers), mapTo(r, s.Env)
	s = &c
	add := func(k string, v any) { o = append(o, kv{k, v}) }
	optional := func(k string, m map[string]string) {
		if len(m) > 0 {
			add(k, m)
		}
	}
	switch f {
	case Claude, ZCode:
		add("type", s.Transport)
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			add("env", strs(s.Env))
		}
	case Gemini:
		switch s.Transport {
		case "http":
			add("httpUrl", s.URL)
			optional("headers", s.Headers)
		case "sse":
			add("url", s.URL)
			optional("headers", s.Headers)
		default:
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case OpenCode:
		if s.Remote() {
			add("type", "remote")
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("type", "local")
			add("command", append([]string{s.Command}, s.Args...))
			optional("environment", s.Env)
		}
		add("enabled", true)
	case Cursor, PiNative, Grok:
		// no type: a url is streamable HTTP, a command stdio
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case Copilot:
		if s.Remote() {
			add("type", s.Transport)
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("type", "local")
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
		add("tools", []string{"*"})
	case Goose:
		add("enabled", true)
		add("name", s.Name)
		switch s.Transport {
		case "http":
			add("type", "streamable_http")
			add("uri", s.URL)
			optional("headers", s.Headers)
			// Goose fills a header's ${NAME} only from a name listed here
			if keys := gooseKeys(s.Headers, nil); len(keys) > 0 {
				add("env_keys", keys)
			}
		default:
			// a command's variable comes through env_keys, envs are given
			// as written
			plain, pass := codexEnv(s.Env)
			add("type", "stdio")
			add("cmd", s.Command)
			add("args", list(s.Args))
			optional("envs", plain)
			if len(pass) > 0 {
				add("env_keys", pass)
			}
		}
		add("timeout", 300)
	case Hermes:
		// transport: sse is the only other transport besides a plain url
		if s.Remote() {
			add("url", s.URL)
			optional("headers", s.Headers)
			if s.Transport == "sse" {
				add("transport", "sse")
			}
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case Omp:
		if s.Remote() {
			add("type", s.Transport)
			add("url", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case Kimi:
		// as `kimi mcp add` writes them; transport is left out for a command
		if s.Remote() {
			add("url", s.URL)
			add("transport", s.Transport)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case Cline:
		// as `cline mcp add` writes it; a url with no type would be SSE
		if s.Remote() {
			t := ordered{{"type", "streamableHttp"}, {"url", s.URL}}
			if s.Transport == "sse" {
				t[0].v = "sse"
			}
			if len(s.Headers) > 0 {
				t = append(t, kv{"headers", s.Headers})
			}
			add("transport", t)
		} else {
			t := ordered{{"type", "stdio"}, {"command", s.Command}, {"args", list(s.Args)}}
			if len(s.Env) > 0 {
				t = append(t, kv{"env", s.Env})
			}
			add("transport", t)
		}
	case Antigravity:
		// Antigravity tells SSE from streamable HTTP itself; "type" only
		// lets us read an SSE server back as one (agy keeps the key)
		if s.Remote() {
			if s.Transport == "sse" {
				add("type", "sse")
			}
			add("serverUrl", s.URL)
			optional("headers", s.Headers)
		} else {
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", s.Env)
		}
	case Codex:
		// a reference is a variable Codex reads by name: Authorization's
		// bearer token, a header whole, a variable passed on to a command
		if s.Remote() {
			plain, bearer, fromEnv := codexHeaders(s.Headers)
			add("url", s.URL)
			optional("http_headers", plain)
			if bearer != "" {
				add("bearer_token_env_var", bearer)
			}
			optional("env_http_headers", fromEnv)
		} else {
			plain, pass := codexEnv(s.Env)
			add("command", s.Command)
			add("args", list(s.Args))
			optional("env", plain)
			if len(pass) > 0 {
				add("env_vars", pass)
			}
		}
	}
	return o
}

// decode reads one of the agent's entries; false for one that can't be read
// as a server (a Goose builtin, say).
func (f Format) decode(name string, m map[string]any) (*Server, bool) {
	s := &Server{Name: name}
	remote := func(transport, url string, headers any) {
		s.Transport, s.URL, s.Headers = transport, url, strMap(headers)
	}
	local := func(cmd string, args any, env any) {
		s.Transport, s.Command, s.Args, s.Env = "stdio", cmd, strList(args), strMap(env)
	}
	switch f {
	case OpenCode:
		if str(m, "type") == "remote" {
			remote("http", str(m, "url"), m["headers"])
		} else if c := strList(m["command"]); len(c) > 0 {
			local(c[0], nil, m["environment"])
			s.Args = c[1:]
		}
	case Goose:
		switch str(m, "type") {
		case "stdio":
			local(str(m, "cmd"), m["args"], m["envs"])
			// a variable Goose reads by name comes back as a reference
			for _, n := range envVarNames(m["env_keys"]) {
				putRef(&s.Env, n, "${"+n+"}")
			}
		case "streamable_http":
			remote("http", str(m, "uri"), m["headers"])
		case "sse":
			remote("sse", str(m, "uri"), m["headers"])
		}
	case Antigravity:
		// agy reads a url where there's no serverUrl too
		u := str(m, "serverUrl")
		if u == "" {
			u = str(m, "url")
		}
		if u != "" {
			t := "http"
			if str(m, "type") == "sse" {
				t = "sse"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case Codex:
		// the variables Codex reads by name come back as references
		if u := str(m, "url"); u != "" {
			remote("http", u, m["http_headers"])
			if b := str(m, "bearer_token_env_var"); b != "" {
				putRef(&s.Headers, "Authorization", "Bearer ${"+b+"}")
			}
			for k, v := range strMap(m["env_http_headers"]) {
				putRef(&s.Headers, k, "${"+v+"}")
			}
		} else {
			local(str(m, "command"), m["args"], m["env"])
			for _, n := range envVarNames(m["env_vars"]) {
				putRef(&s.Env, n, "${"+n+"}")
			}
		}
	case Gemini:
		if u := str(m, "httpUrl"); u != "" {
			remote("http", u, m["headers"])
		} else if u := str(m, "url"); u != "" {
			t := "sse"
			if str(m, "type") == "http" {
				t = "http"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case Hermes, Kimi:
		// each takes a url over a command when an entry has both
		if u := str(m, "url"); u != "" {
			t := "http"
			if str(m, "transport") == "sse" {
				t = "sse"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	case Cline:
		// the flat shape Cline's extension wrote is read too, a url with no
		// type being SSE there
		t, nested := m["transport"].(map[string]any)
		if !nested {
			t = m
		}
		ty := str(t, "type")
		if ty == "" && !nested {
			ty = str(t, "transportType")
		}
		if u := str(t, "url"); u != "" {
			tr := "sse"
			if ty == "streamableHttp" || ty == "http" {
				tr = "http"
			}
			remote(tr, u, t["headers"])
		} else {
			local(str(t, "command"), t["args"], t["env"])
		}
	case PiNative:
		// an entry moved from mcp-adapter.json keeps its httpTransport, so
		// an SSE server still reads as one, which Pi's own can't reach
		if u := str(m, "url"); u != "" {
			t := "http"
			if str(m, "type") == "sse" || str(m, "transport") == "sse" || str(m, "httpTransport") == "sse" {
				t = "sse"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	default: // Claude Code, Cursor, Copilot, ZCode, omp, Grok
		t := str(m, "type")
		if u := str(m, "url"); u != "" {
			if t != "sse" {
				t = "http"
			}
			remote(t, u, m["headers"])
		} else {
			local(str(m, "command"), m["args"], m["env"])
		}
	}
	if len(s.Args) == 0 {
		s.Args = nil
	}
	r := refsOf(f)
	s.Headers, s.Env = mapFrom(r, s.Headers), mapFrom(r, s.Env)
	if s.Transport == "" || (s.Transport == "stdio" && s.Command == "") || (s.Remote() && s.URL == "") {
		return nil, false
	}
	return s, true
}

// current is the agent's entry read as the server, for comparing with s:
// what decode reads, but for the variables a Codex or Goose entry names in
// keys that are the user's rather than ours (refKeys).
func (f Format) current(name string, m map[string]any, s *Server) (*Server, bool) {
	cur, ok := f.decode(name, m)
	if !ok || cur == nil || len(refKeys(f, s)) > 0 {
		return cur, ok
	}
	keep := func(got map[string]string, key string) map[string]string {
		plain, _ := m[key].(map[string]any)
		for k := range got {
			if _, set := plain[k]; !set {
				delete(got, k)
			}
		}
		if len(got) == 0 {
			return nil
		}
		return got
	}
	switch f {
	case Codex:
		if cur.Remote() {
			cur.Headers = keep(cur.Headers, "http_headers")
		} else {
			cur.Env = keep(cur.Env, "env")
		}
	case Goose:
		if !cur.Remote() {
			cur.Env = keep(cur.Env, "envs")
		}
	}
	return cur, true
}

// merged is the entry to write, with what the user added to the old one
// kept; a default we give (Copilot's tools, Goose's timeout) yields to the
// user's.
func (f Format) merged(s *Server, old map[string]any) ordered {
	o := f.encode(s)
	mine := slices.Clone(owned[f])
	mine = append(mine, refKeys(f, s)...)
	for i, e := range o {
		if v, ok := old[e.k]; ok && !slices.Contains(mine, e.k) {
			o[i].v = v
		}
		// Codex's env_vars as the user wrote them ({ name, source }), or
		// Goose's env_keys in their order, while they name the same
		// variables
		if v, ok := old[e.k]; ok && (f == Codex && e.k == "env_vars" || f == Goose && e.k == "env_keys") {
			if want, _ := e.v.([]string); slices.Equal(slices.Sorted(slices.Values(envVarNames(v))), want) {
				o[i].v = v
			}
		}
	}
	var refused []string
	if f == Codex {
		refused = codexRefused(s)
	}
	for _, k := range slices.Sorted(maps.Keys(old)) {
		if slices.Contains(mine, k) || slices.ContainsFunc(o, func(e kv) bool { return e.k == k }) || slices.Contains(refused, k) {
			continue
		}
		o = append(o, kv{k, old[k]})
	}
	return o
}

// codexRefused are the keys of the other transport, which Codex refuses in
// an entry of s's: a command's beside a URL, a URL's beside a command. Codex
// then won't load its config at all ("env_vars is not supported for
// streamable_http"), so such a key, left from when the server was the other
// kind, goes when the entry is written; the user's keys of s's own transport
// stay (codex-rs/config/src/mcp_types.rs).
func codexRefused(s *Server) []string {
	if s.Remote() {
		return []string{"args", "env", "env_vars", "cwd"}
	}
	return []string{"url", "bearer_token_env_var", "http_headers", "env_http_headers", "http_headers_helper", "oauth", "oauth_resource", "auth"}
}

// tomlKey spells a key bare when TOML allows it, else quoted.
func tomlKey(k string) string {
	if k == "" {
		return `""`
	}
	for _, r := range k {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return edit.TOMLString(k)
		}
	}
	return k
}

// tomlValue spells a value as TOML: ours, or one decoded from the file.
func tomlValue(v any) string {
	switch x := v.(type) {
	case string:
		return edit.TOMLString(x)
	case bool, int, int64:
		return fmt.Sprint(x)
	case float64:
		return tomlFloat(x)
	case json.Number:
		return x.String()
	case time.Time:
		return x.Format(time.RFC3339Nano)
	case toml.LocalDate, toml.LocalTime, toml.LocalDateTime:
		return fmt.Sprint(x)
	case []string:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = edit.TOMLString(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(x))
		for i, s := range x {
			parts[i] = tomlValue(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]string:
		m := map[string]any{}
		for k, s := range x {
			m[k] = s
		}
		return tomlValue(m)
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = tomlKey(k) + " = " + tomlValue(x[k])
		}
		if len(parts) == 0 {
			return "{}"
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	return edit.TOMLString(fmt.Sprint(v))
}

// tomlFloat writes a float as one, so a user's tool_timeout_sec = 120.0
// stays a float and doesn't come back as an integer.
func tomlFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}
