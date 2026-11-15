// Ported from magpie (internal/library/envref.go), MIT License, (c) 2026 yetone.

package mcpfmt

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// References to environment variables in a server's headers and environment.
// The library writes one as ${NAME} and keeps it as written; each agent is
// given it in the syntax that agent reads, so a token can stay in the
// environment and out of every agent's config. An agent that reads no
// reference where the server has one isn't given the server: written as it
// is, the agent would send "${NAME}" itself, and written with the value, the
// token would be in its file after all.
//
// A command and its args are written as they are: the library layer above
// decides what to do about a ~ or a $VAR there.

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

type refSyntax int

const (
	// refNone: the agent reads no reference there
	refNone refSyntax = iota
	// refDollar: ${NAME}, as the library writes it
	refDollar
	// refOpenCode: {env:NAME}
	refOpenCode
	// refCursor: ${env:NAME}
	refCursor
	// refCodex: Codex's own keys, which name the variable; a value can only
	// be the variable whole (Authorization's Bearer aside)
	refCodex
	// refGoose: ${NAME} in a header, each name listed in env_keys; a
	// command's variable only through env_keys, under its own name
	refGoose
)

// refsOf is how each format reads a reference in a remote server's headers
// and a command's environment, from the agents' own docs and source as
// magpie recorded them. A refNone is an agent no reference was found in, so
// a server that has one is left out for it.
func refsOf(f Format) refSyntax {
	switch f {
	case Claude, Grok, Gemini, Copilot, PiNative, Omp, Hermes:
		return refDollar
	case Codex:
		return refCodex
	case OpenCode:
		return refOpenCode
	case Cursor:
		return refCursor
	case Goose:
		return refGoose
	}
	// Antigravity, ZCode, Kimi and Cline hand headers and env on as written
	return refNone
}

var (
	openCodeRef = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
	cursorRef   = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// errNoEnvRef is why an agent that reads no reference isn't given a server
// with one.
var errNoEnvRef = errors.New("it reads no environment variable from its MCP config, so a server with ${NAME} in its headers or environment isn't given to it: written there, the token would be in its file as plain text")

func refNames(v string) []string {
	var out []string
	for _, m := range envRef.FindAllStringSubmatch(v, -1) {
		out = append(out, m[1])
	}
	return out
}

func hasRef(m map[string]string) bool {
	for _, v := range m {
		if envRef.MatchString(v) {
			return true
		}
	}
	return false
}

// whole is the variable a value is in whole (${NAME}), or "".
func whole(v string) string {
	if m := envRef.FindStringSubmatch(v); m != nil && m[0] == v {
		return m[1]
	}
	return ""
}

// bearerOf is the variable Authorization's value "Bearer ${NAME}" names.
func bearerOf(k, v string) string {
	if !strings.EqualFold(k, "Authorization") {
		return ""
	}
	rest, ok := strings.CutPrefix(v, "Bearer ")
	if !ok {
		return ""
	}
	return whole(rest)
}

// refsProblem says why the format can't be given the server's references,
// or nil when it can (or the server has none).
func refsProblem(f Format, s *Server) error {
	r := refsOf(f)
	headers, env := s.Headers, s.Env
	if s.Remote() {
		env = nil
	} else {
		headers = nil
	}
	if r == refNone && (hasRef(headers) || hasRef(env)) {
		return errNoEnvRef
	}
	if r == refCodex {
		for _, k := range slices.Sorted(maps.Keys(headers)) {
			v := headers[k]
			if envRef.MatchString(v) && whole(v) == "" && bearerOf(k, v) == "" {
				return fmt.Errorf("Codex reads a header from a variable only whole (%s: ${NAME}), or as Authorization: Bearer ${NAME}; %s isn't one", k, k)
			}
		}
	}
	if r == refCodex || r == refGoose {
		agent := "Codex"
		if r == refGoose {
			agent = "Goose"
		}
		for _, k := range slices.Sorted(maps.Keys(env)) {
			v := env[k]
			if envRef.MatchString(v) && whole(v) != k {
				return fmt.Errorf("%s passes a variable on only under its own name (%s: ${%s}); %s isn't one", agent, k, k, k)
			}
		}
	}
	if r == refGoose {
		// Goose reads an env_keys name upper-cased, so a lower-case one
		// would be some other variable
		for _, n := range gooseKeys(headers, env) {
			if n != strings.ToUpper(n) {
				return fmt.Errorf("Goose reads a variable's name upper-cased; ${%s} would be %s", n, strings.ToUpper(n))
			}
		}
	}
	return nil
}

// gooseKeys are the variables Goose is told to read (env_keys): those the
// headers reference and those a command is given.
func gooseKeys(headers, env map[string]string) []string {
	var out []string
	for _, m := range []map[string]string{headers, env} {
		for _, v := range m {
			for _, n := range refNames(v) {
				if !slices.Contains(out, n) {
					out = append(out, n)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// refKeys are the keys of an agent's entry that name the variables the
// server references, ours to write only while it references one there.
func refKeys(f Format, s *Server) []string {
	switch f {
	case Codex:
		if s.Remote() && hasRef(s.Headers) {
			return []string{"bearer_token_env_var", "env_http_headers"}
		}
		if !s.Remote() && hasRef(s.Env) {
			return []string{"env_vars"}
		}
	case Goose:
		if s.Remote() && hasRef(s.Headers) || !s.Remote() && hasRef(s.Env) {
			return []string{"env_keys"}
		}
	}
	return nil
}

// mapTo is m with every value in the agent's syntax; nil stays nil.
func mapTo(syntax refSyntax, m map[string]string) map[string]string {
	if m == nil || syntax != refOpenCode && syntax != refCursor {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch syntax {
		case refOpenCode:
			v = envRef.ReplaceAllString(v, "{env:$1}")
		case refCursor:
			v = envRef.ReplaceAllString(v, "$${env:$1}")
		}
		out[k] = v
	}
	return out
}

// mapFrom is m with the agent's references as the library writes them.
func mapFrom(syntax refSyntax, m map[string]string) map[string]string {
	if m == nil || syntax != refOpenCode && syntax != refCursor {
		return m
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		switch syntax {
		case refOpenCode:
			v = openCodeRef.ReplaceAllString(v, "$${$1}")
		case refCursor:
			v = cursorRef.ReplaceAllString(v, "$${$1}")
		}
		out[k] = v
	}
	return out
}

// codexHeaders splits headers into those Codex sends as written, the
// variable Authorization's bearer token is read from, and the headers it
// reads whole from a variable.
func codexHeaders(h map[string]string) (plain map[string]string, bearer string, fromEnv map[string]string) {
	for _, k := range slices.Sorted(maps.Keys(h)) {
		v := h[k]
		switch {
		case !envRef.MatchString(v):
			if plain == nil {
				plain = map[string]string{}
			}
			plain[k] = v
		case bearerOf(k, v) != "" && bearer == "":
			bearer = bearerOf(k, v)
		default:
			if fromEnv == nil {
				fromEnv = map[string]string{}
			}
			fromEnv[k] = whole(v)
		}
	}
	return
}

// codexEnv splits a command's environment into what is set as written and
// the variables passed on by name.
func codexEnv(e map[string]string) (plain map[string]string, pass []string) {
	for _, k := range slices.Sorted(maps.Keys(e)) {
		if v := e[k]; envRef.MatchString(v) {
			pass = append(pass, k)
		} else {
			if plain == nil {
				plain = map[string]string{}
			}
			plain[k] = v
		}
	}
	return
}

// putRef sets k to v in *into, made if need be, unless k is there already
// (a value written out wins over the variable it's read from).
func putRef(into *map[string]string, k, v string) {
	if *into == nil {
		*into = map[string]string{}
	}
	if _, ok := (*into)[k]; !ok {
		(*into)[k] = v
	}
}

// envVarNames are the names a Codex entry's env_vars passes on: each a name,
// or a table with one; Goose's env_keys is a list of names too.
func envVarNames(v any) []string {
	a, _ := v.([]any)
	var out []string
	for _, x := range a {
		switch e := x.(type) {
		case string:
			out = append(out, e)
		case map[string]any:
			if n, _ := e["name"].(string); n != "" {
				out = append(out, n)
			}
		}
	}
	return out
}
