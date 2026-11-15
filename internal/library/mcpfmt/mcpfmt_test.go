package mcpfmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var (
	stdioSrv = &Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs", "/tmp"}, Env: map[string]string{"TOKEN": "abc", "A": "b c"}}
	bareSrv  = &Server{Name: "bare", Transport: "stdio", Command: "/usr/bin/tool"}
	httpSrv  = &Server{Name: "gh", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"X-Key": "k1"}}
	sseSrv   = &Server{Name: "live", Transport: "sse", URL: "https://example.com/sse"}
	keepSrv  = &Server{Name: "keep", Transport: "stdio", Command: "keepcmd", Args: []string{"a"}}
)

// seed is a file as a user has it: a comment, a key of its own, and one
// server of their own, in the format's own shape.
func seed(t *testing.T, f Format) string {
	t.Helper()
	switch f {
	case Codex, Grok:
		return "# top comment\nmodel = \"gpt\" # my model\n\n# my server\n[mcp_servers.keep] # user's\ncommand = \"keepcmd\"\nargs = [\"a\"]\n\n[projects.\"/x\"]\ntrust = \"trusted\"\n"
	case Goose:
		return "# top comment\nGOOSE_MODEL: x # model\nextensions:\n  # my extension\n  keep:\n    enabled: true\n    name: keep\n    type: stdio\n    cmd: keepcmd\n    args: [a]\n    timeout: 300\n  developer:\n    enabled: true # builtin\n    type: builtin\n    name: developer\n"
	case Hermes:
		return "# top comment\nmodel: x # model\nmcp_servers:\n  # my server\n  keep:\n    command: keepcmd\n    args: [a] # args\ndisplay:\n  skin: y\n"
	}
	entry, _ := json.Marshal(f.encode(keepSrv))
	parts := strings.Split(f.key(), ".")
	var open, shut strings.Builder
	ind := "  "
	for _, p := range parts {
		fmt.Fprintf(&open, "%s// level %s\n%s%q: {\n", ind, p, ind, p)
		ind += "  "
	}
	fmt.Fprintf(&open, "%s%q: %s, // user's own\n", ind, "keep", entry)
	for i := len(parts) - 1; i >= 0; i-- {
		ind = ind[:len(ind)-2]
		fmt.Fprintf(&shut, "%s}\n", ind)
	}
	return "{\n  // top comment\n  \"foreign\": {\"x\": 1}, // keep this\n" + open.String() + shut.String() + "}\n"
}

func comments(f Format) []string {
	switch {
	case f.toml():
		return []string{"# top comment", "# my model", "# my server", "# user's", `[projects."/x"]`}
	case f == Goose:
		return []string{"# top comment", "# model", "# my extension", "# builtin"}
	case f == Hermes:
		return []string{"# top comment", "# model", "# my server", "# args"}
	}
	return []string{"// top comment", "// keep this", "// user's own", `"foreign": {"x": 1}`}
}

func write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func slurp(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func supported(f File, ss ...*Server) []*Server {
	var out []*Server
	for _, s := range ss {
		if f.Supports(s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func TestRoundTripEveryFormat(t *testing.T) {
	for _, format := range Formats {
		t.Run(string(format), func(t *testing.T) {
			p := write(t, "cfg", seed(t, format))
			f := File{p, format}
			before, err := f.Read()
			if err != nil || before["keep"] == nil || !reflect.DeepEqual(before["keep"], keepSrv) {
				t.Fatalf("seed reads %v (%v), want keep", before, err)
			}
			put := supported(f, stdioSrv, bareSrv, httpSrv, sseSrv)
			if len(put) < 3 {
				t.Fatalf("only %d servers supported", len(put))
			}
			if err := f.Put(put...); err != nil {
				t.Fatal(err)
			}
			got, err := f.Read()
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range put {
				want := f.Format.flat(s)
				if !reflect.DeepEqual(got[s.Name], want) || !f.Same(got[s.Name], s) {
					t.Errorf("%s reads back as %+v, want %+v\n%s", s.Name, got[s.Name], want, slurp(t, p))
				}
			}
			if !reflect.DeepEqual(got["keep"], keepSrv) {
				t.Errorf("the user's own server changed: %+v", got["keep"])
			}
			text := slurp(t, p)
			for _, c := range comments(format) {
				if !strings.Contains(text, c) {
					t.Errorf("lost %q:\n%s", c, text)
				}
			}
			// a second Put of the same servers writes nothing
			st1, _ := os.Stat(p)
			if err := f.Put(put...); err != nil || slurp(t, p) != text {
				t.Errorf("second Put changed the file: %v", err)
			}
			st2, _ := os.Stat(p)
			if !st2.ModTime().Equal(st1.ModTime()) {
				t.Error("second Put rewrote the file")
			}
			// removing them again leaves the user's own
			var names []string
			for _, s := range put {
				names = append(names, s.Name)
			}
			if err := f.Remove(append(names, "absent")...); err != nil {
				t.Fatal(err)
			}
			after, err := f.Read()
			if err != nil || len(after) != 1 || !reflect.DeepEqual(after["keep"], keepSrv) {
				t.Fatalf("after Remove: %v %v", after, err)
			}
			end := slurp(t, p)
			for _, c := range comments(format) {
				if !strings.Contains(end, c) {
					t.Errorf("Remove lost %q:\n%s", c, end)
				}
			}
			if !format.yaml() && end != seed(t, format) {
				t.Errorf("Put then Remove did not restore the file:\n%s\nwant:\n%s", end, seed(t, format))
			}
		})
	}
}

func TestRemoveLeavesOthersByteIdentical(t *testing.T) {
	for _, format := range Formats {
		if format.yaml() {
			continue // YAML is re-rendered from its node tree
		}
		t.Run(string(format), func(t *testing.T) {
			p := write(t, "cfg", seed(t, format))
			f := File{p, format}
			if err := f.Put(stdioSrv, httpSrv); err != nil {
				t.Fatal(err)
			}
			mid := slurp(t, p)
			if err := f.Put(bareSrv); err != nil {
				t.Fatal(err)
			}
			if err := f.Remove("bare"); err != nil {
				t.Fatal(err)
			}
			if got := slurp(t, p); got != mid {
				t.Errorf("Put+Remove of one server changed the others:\n%s\nwant:\n%s", got, mid)
			}
		})
	}
}

func TestMissingFile(t *testing.T) {
	for _, format := range Formats {
		t.Run(string(format), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "a", "b", "cfg")
			f := File{p, format}
			got, err := f.Read()
			if err != nil || len(got) != 0 {
				t.Fatalf("Read of a missing file: %v %v", got, err)
			}
			if err := f.Remove("fs"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(p); err == nil {
				t.Fatal("Remove made the file")
			}
			if err := f.Put(stdioSrv, httpSrv); err != nil {
				t.Fatal(err)
			}
			got, err = f.Read()
			if err != nil || !reflect.DeepEqual(got["fs"], stdioSrv) || !reflect.DeepEqual(got["gh"], httpSrv) {
				t.Fatalf("after Put: %+v %v\n%s", got, err, slurp(t, p))
			}
		})
	}
}

func TestUnparseableFileIsNeverWritten(t *testing.T) {
	bad := map[string][]string{
		"json": {"{ \"a\": [1, ", "[1,2]", "null", "{\"mcpServers\": 5, \"mcp\": 5}", "{} {}"},
		"toml": {"[a\nx = 1\n", "[a]\nx = 1\n[a]\ny = 2\n", "mcp_servers = 5\n"},
		"yaml": {"a: [1, 2\n", "a: 1\na: 2\n", "- x\n", "mcp_servers: 5\nextensions: 5\n"},
	}
	for _, format := range Formats {
		kind := "json"
		switch {
		case format.toml():
			kind = "toml"
		case format.yaml():
			kind = "yaml"
		}
		for _, in := range bad[kind] {
			t.Run(fmt.Sprintf("%s/%q", format, in), func(t *testing.T) {
				p := write(t, "cfg", in)
				f := File{p, format}
				if _, err := f.Read(); err == nil {
					t.Error("Read accepted it")
				}
				if err := f.Put(stdioSrv); err == nil {
					t.Error("Put accepted it")
				}
				if err := f.Remove("fs"); err == nil {
					t.Error("Remove accepted it")
				}
				if got := slurp(t, p); got != in {
					t.Errorf("the file was changed: %q", got)
				}
				entries, _ := os.ReadDir(filepath.Dir(p))
				if len(entries) != 1 {
					t.Errorf("left files beside it: %v", entries)
				}
			})
		}
	}
}

func TestReplaceKeepsUsersKeysOfTheEntry(t *testing.T) {
	cases := map[Format]string{
		Claude:      `{"mcpServers": {"fs": {"type": "stdio", "command": "old", "args": [], "env": {}, "alwaysAllow": ["x"], "timeout": 1.5e0}}}`,
		Gemini:      `{"mcpServers": {"fs": {"command": "old", "trust": true, "timeout": 5000}}}`,
		Cursor:      `{"mcpServers": {"fs": {"command": "old", "disabled": false}}}`,
		Copilot:     `{"mcpServers": {"fs": {"type": "local", "command": "old", "tools": ["only_this"]}}}`,
		Cline:       `{"mcpServers": {"fs": {"command": "old", "args": ["x"], "disabled": true, "autoApprove": ["t"]}}}`,
		OpenCode:    `{"mcp": {"fs": {"type": "local", "command": ["old"], "enabled": true, "timeout": 9}}}`,
		Antigravity: `{"mcpServers": {"fs": {"command": "old", "disabled": true}}}`,
		Kimi:        `{"mcpServers": {"fs": {"command": "old", "auth": "oauth"}}}`,
		Omp:         `{"mcpServers": {"fs": {"command": "old", "timeout": 3}}}`,
		PiNative:    `{"mcpServers": {"fs": {"command": "old", "lifecycle": "lazy"}}}`,
		ZCode:       `{"mcp": {"servers": {"fs": {"type": "stdio", "command": "old", "note": "n"}}}}`,
		Codex:       "[mcp_servers.fs]\ncommand = \"old\"\ntool_timeout_sec = 120.0\nenabled = true\n\n[mcp_servers.fs.env]\nOLD = \"1\"\n",
		Grok:        "[mcp_servers.fs]\ncommand = \"old\"\nenabled = false\n",
		Hermes:      "mcp_servers:\n  fs:\n    command: old\n    timeout: 120\n    tools:\n      include: [a]\n",
		Goose:       "extensions:\n  fs:\n    enabled: false\n    name: fs\n    type: stdio\n    cmd: old\n    timeout: 900\n",
	}
	extra := map[Format][]string{
		Claude: {"alwaysAllow", "timeout"}, Gemini: {"trust", "5000"}, Cursor: {"disabled"}, Copilot: {"only_this"},
		Cline: {"disabled", "autoApprove"}, OpenCode: {"timeout"}, Antigravity: {"disabled"}, Kimi: {"auth"},
		Omp: {"timeout"}, PiNative: {"lifecycle"}, ZCode: {"note"}, Codex: {"tool_timeout_sec = 120.0", "enabled = true"},
		Grok: {"enabled = false"}, Hermes: {"timeout: 120", "include"}, Goose: {"timeout: 900"},
	}
	for _, format := range Formats {
		t.Run(string(format), func(t *testing.T) {
			p := write(t, "cfg", cases[format])
			f := File{p, format}
			if err := f.Put(stdioSrv); err != nil {
				t.Fatal(err)
			}
			text := slurp(t, p)
			for _, w := range extra[format] {
				if !strings.Contains(text, w) {
					t.Errorf("lost the user's %q:\n%s", w, text)
				}
			}
			got, err := f.Read()
			if err != nil || !reflect.DeepEqual(got["fs"], stdioSrv) {
				t.Errorf("reads %+v (%v)\n%s", got["fs"], err, text)
			}
			if strings.Contains(text, "old") && format != Hermes {
				t.Errorf("the old command is still there:\n%s", text)
			}
		})
	}
}

func TestSupports(t *testing.T) {
	noSSE := map[Format]bool{Codex: true, Goose: true, PiNative: true, Grok: true}
	for _, format := range Formats {
		f := File{Format: format}
		err := f.Supports(sseSrv)
		if noSSE[format] != errors.Is(err, ErrNoSSE) || (!noSSE[format] && err != nil) {
			t.Errorf("%s: sse -> %v", format, err)
		}
		for _, s := range []*Server{stdioSrv, httpSrv} {
			if err := f.Supports(s); err != nil {
				t.Errorf("%s: %s -> %v", format, s.Name, err)
			}
		}
	}
	if err := (File{Format: "nope"}).Supports(stdioSrv); err == nil {
		t.Error("an unknown format supports a server")
	}
	if err := (File{Format: OpenCode}).Supports(&Server{Name: "servers", Transport: "stdio", Command: "x"}); err == nil {
		t.Error("OpenCode accepts the name servers")
	}
	if err := (File{Format: Goose}).Put(sseSrv); !errors.Is(err, ErrNoSSE) {
		t.Errorf("Put of an unsupported server: %v", err)
	}
}

func TestEnvReferences(t *testing.T) {
	ref := &Server{Name: "r", Transport: "http", URL: "https://x.example/mcp", Headers: map[string]string{"Authorization": "Bearer ${GH_TOKEN}", "X-Plain": "p"}}
	refCmd := &Server{Name: "c", Transport: "stdio", Command: "x", Env: map[string]string{"API_KEY": "${API_KEY}", "P": "v"}}
	for _, format := range []Format{Claude, Codex, Gemini, OpenCode, Cursor, Copilot, Goose, PiNative, Omp, Hermes, Grok} {
		t.Run(string(format), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "cfg")
			f := File{p, format}
			if err := f.Put(ref, refCmd); err != nil {
				t.Fatal(err)
			}
			got, _ := f.Read()
			if !reflect.DeepEqual(got["r"], ref) || !reflect.DeepEqual(got["c"], refCmd) {
				t.Errorf("reads %+v %+v\n%s", got["r"], got["c"], slurp(t, p))
			}
			text := slurp(t, p)
			switch format {
			case OpenCode:
				if !strings.Contains(text, "{env:GH_TOKEN}") || strings.Contains(text, "${") {
					t.Errorf("OpenCode syntax:\n%s", text)
				}
			case Cursor:
				if !strings.Contains(text, "${env:GH_TOKEN}") {
					t.Errorf("Cursor syntax:\n%s", text)
				}
			case Codex:
				if !strings.Contains(text, `bearer_token_env_var = "GH_TOKEN"`) || !strings.Contains(text, `env_vars = ["API_KEY"]`) || strings.Contains(text, "${") {
					t.Errorf("Codex syntax:\n%s", text)
				}
			case Goose:
				if !strings.Contains(text, "env_keys") {
					t.Errorf("Goose env_keys:\n%s", text)
				}
			}
		})
	}
	// an agent that reads no reference isn't given one, and nothing is made
	for _, format := range []Format{Antigravity, ZCode, Kimi, Cline} {
		p := filepath.Join(t.TempDir(), "cfg")
		f := File{p, format}
		if f.Supports(ref) == nil || f.Put(ref) == nil {
			t.Errorf("%s accepted a reference", format)
		}
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s: a file was made", format)
		}
	}
	// Codex reads a header from a variable only whole
	odd := &Server{Name: "o", Transport: "http", URL: "https://x.example", Headers: map[string]string{"X": "a-${V}"}}
	if (File{Format: Codex}).Supports(odd) == nil {
		t.Error("Codex accepted a header with a variable in the middle")
	}
	if (File{Format: Goose}).Supports(&Server{Name: "o", Transport: "stdio", Command: "x", Env: map[string]string{"a": "${a}"}}) == nil {
		t.Error("Goose accepted a lower-case variable")
	}
}

// Shapes each agent documents, written by hand rather than by this package.
func TestDecodeNativeShapes(t *testing.T) {
	cases := []struct {
		format Format
		in     string
		want   map[string]*Server
	}{
		{Gemini, `{"mcpServers": {"a": {"httpUrl": "https://h/mcp", "headers": {"K": "v"}}, "b": {"url": "https://s/sse"}, "c": {"url": "https://t/mcp", "type": "http"}, "d": {"command": "x", "args": ["1"], "env": {"E": "1"}}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h/mcp", Headers: map[string]string{"K": "v"}},
			"b": {Name: "b", Transport: "sse", URL: "https://s/sse"},
			"c": {Name: "c", Transport: "http", URL: "https://t/mcp"},
			"d": {Name: "d", Transport: "stdio", Command: "x", Args: []string{"1"}, Env: map[string]string{"E": "1"}},
		}},
		{OpenCode, `{"mcp": {"a": {"type": "remote", "url": "https://h", "headers": {"K": "{env:T}"}}, "b": {"type": "local", "command": ["npx", "-y", "p"], "environment": {"E": "1"}}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h", Headers: map[string]string{"K": "${T}"}},
			"b": {Name: "b", Transport: "stdio", Command: "npx", Args: []string{"-y", "p"}, Env: map[string]string{"E": "1"}},
		}},
		{OpenCode, `{"mcp": {"servers": {"n": {"type": "local", "command": ["x"]}}, "v1": {"type": "local", "command": ["y"]}}}`, map[string]*Server{
			"n":  {Name: "n", Transport: "stdio", Command: "x"},
			"v1": {Name: "v1", Transport: "stdio", Command: "y"},
		}},
		{Cline, `{"mcpServers": {"a": {"transport": {"type": "sse", "url": "https://s"}}, "b": {"transport": {"type": "streamableHttp", "url": "https://h"}}, "c": {"url": "https://flat", "type": "streamableHttp", "disabled": false, "autoApprove": []}, "d": {"command": "x", "args": [], "disabled": true}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "sse", URL: "https://s"},
			"b": {Name: "b", Transport: "http", URL: "https://h"},
			"c": {Name: "c", Transport: "http", URL: "https://flat"},
			"d": {Name: "d", Transport: "stdio", Command: "x"},
		}},
		{Copilot, `{"mcpServers": {"a": {"type": "local", "command": "x", "args": [], "tools": ["*"]}, "b": {"type": "http", "url": "https://h", "tools": ["*"]}, "c": {"type": "sse", "url": "https://s"}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "stdio", Command: "x"},
			"b": {Name: "b", Transport: "http", URL: "https://h"},
			"c": {Name: "c", Transport: "sse", URL: "https://s"},
		}},
		{Antigravity, `{"mcpServers": {"a": {"serverUrl": "https://h"}, "b": {"url": "https://u", "type": "sse"}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h"},
			"b": {Name: "b", Transport: "sse", URL: "https://u"},
		}},
		{Kimi, `{"mcpServers": {"a": {"url": "https://h", "transport": "http"}, "b": {"url": "https://s", "transport": "sse"}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h"},
			"b": {Name: "b", Transport: "sse", URL: "https://s"},
		}},
		{Hermes, "mcp_servers:\n  a:\n    url: https://h\n  b:\n    url: https://s\n    transport: sse\n  c:\n    command: x\n    args: [\"1\"]\n", map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h"},
			"b": {Name: "b", Transport: "sse", URL: "https://s"},
			"c": {Name: "c", Transport: "stdio", Command: "x", Args: []string{"1"}},
		}},
		{Goose, "extensions:\n  developer:\n    type: builtin\n    name: developer\n  a:\n    type: streamable_http\n    uri: https://h\n    headers: {K: v}\n  b:\n    type: sse\n    uri: https://s\n  c:\n    type: stdio\n    cmd: x\n    envs: {E: '1'}\n", map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h", Headers: map[string]string{"K": "v"}},
			"b": {Name: "b", Transport: "sse", URL: "https://s"},
			"c": {Name: "c", Transport: "stdio", Command: "x", Env: map[string]string{"E": "1"}},
		}},
		{Codex, "[mcp_servers.a]\nurl = \"https://h\"\nhttp_headers = { K = \"v\" }\n\n[mcp_servers.b]\ncommand = \"x\"\nargs = [\"1\"]\n[mcp_servers.b.env]\nE = \"1\"\n", map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h", Headers: map[string]string{"K": "v"}},
			"b": {Name: "b", Transport: "stdio", Command: "x", Args: []string{"1"}, Env: map[string]string{"E": "1"}},
		}},
		{ZCode, `{"mcp": {"servers": {"a": {"type": "http", "url": "https://h"}}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "http", URL: "https://h"},
		}},
		{Claude, `{"numbers": 1, "mcpServers": {"a": {"type": "sse", "url": "https://s"}, "b": {"command": "x", "env": {"N": 5}}}}`, map[string]*Server{
			"a": {Name: "a", Transport: "sse", URL: "https://s"},
			"b": {Name: "b", Transport: "stdio", Command: "x", Env: map[string]string{"N": "5"}},
		}},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("%d-%s", i, c.format), func(t *testing.T) {
			got, err := File{write(t, "cfg", c.in), c.format}.Read()
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v (%v)\nwant %+v", got, err, c.want)
			}
		})
	}
}

func TestOpenCodeLayouts(t *testing.T) {
	// V1 file: new servers go to mcp.<name>
	p := write(t, "opencode.jsonc", "{\n  // c\n  \"mcp\": {\n    \"old\": {\"type\": \"local\", \"command\": [\"o\"], \"enabled\": true}\n  }\n}\n")
	f := File{p, OpenCode}
	if err := f.Put(stdioSrv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(slurp(t, p), `"fs": {`) || strings.Contains(slurp(t, p), `"servers"`) || !strings.Contains(slurp(t, p), `"enabled": true`) {
		t.Errorf("V1:\n%s", slurp(t, p))
	}
	// native file: new servers go to mcp.servers.<name>, with no enabled
	p = write(t, "opencode.json", `{"mcp": {"servers": {"n": {"type": "local", "command": ["x"]}}, "v1": {"type": "local", "command": ["y"], "enabled": true}}}`)
	f = File{p, OpenCode}
	if err := f.Put(stdioSrv, &Server{Name: "v1", Transport: "stdio", Command: "z"}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.Read()
	if len(got) != 3 || got["fs"] == nil || got["v1"].Command != "z" || got["n"] == nil {
		t.Fatalf("native: %+v\n%s", got, slurp(t, p))
	}
	var doc struct {
		MCP struct {
			Servers map[string]map[string]any `json:"servers"`
			V1      map[string]any            `json:"v1"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal([]byte(slurp(t, p)), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.MCP.Servers["fs"]["enabled"]; ok || doc.MCP.Servers["fs"] == nil || doc.MCP.V1 == nil {
		t.Errorf("native layout:\n%s", slurp(t, p))
	}
	if err := f.Remove("fs", "n", "v1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Read(); len(got) != 0 {
		t.Errorf("after Remove: %+v\n%s", got, slurp(t, p))
	}
}

func TestCodexSubTablesAndOtherTables(t *testing.T) {
	in := "model = \"m\"\n\n[mcp_servers.fs]\ncommand = \"old\"\n\n[mcp_servers.fs.env]\nOLD = \"1\"\n\n# trusted dirs\n[projects.\"/a\"]\ntrust_level = \"trusted\"\n\n[mcp_servers.other]\nurl = \"https://o\"\n"
	p := write(t, "config.toml", in)
	f := File{p, Codex}
	if err := f.Put(stdioSrv); err != nil {
		t.Fatal(err)
	}
	text := slurp(t, p)
	if strings.Contains(text, "OLD") || !strings.Contains(text, "# trusted dirs\n[projects.\"/a\"]") || !strings.Contains(text, "[mcp_servers.other]\nurl = \"https://o\"") {
		t.Errorf("got:\n%s", text)
	}
	if err := f.Remove("fs"); err != nil {
		t.Fatal(err)
	}
	want := "model = \"m\"\n\n# trusted dirs\n[projects.\"/a\"]\ntrust_level = \"trusted\"\n\n[mcp_servers.other]\nurl = \"https://o\"\n"
	if got := slurp(t, p); got != want {
		t.Errorf("after Remove:\n%s\nwant:\n%s", got, want)
	}
	// a server defined inline can't be taken out by table: refused, file kept
	inline := "[mcp_servers]\nfs = { command = \"x\" }\n"
	p = write(t, "config.toml", inline)
	if err := (File{p, Codex}).Remove("fs"); err == nil {
		t.Error("an inline server was reported removed")
	}
	if slurp(t, p) != inline {
		t.Error("file changed")
	}
}

func TestCodexSwitchingTransportDropsRefusedKeys(t *testing.T) {
	p := write(t, "config.toml", "[mcp_servers.x]\ncommand = \"c\"\nargs = [\"a\"]\nenv_vars = [\"K\"]\ntool_timeout_sec = 30\n")
	f := File{p, Codex}
	if err := f.Put(&Server{Name: "x", Transport: "http", URL: "https://h"}); err != nil {
		t.Fatal(err)
	}
	text := slurp(t, p)
	if strings.Contains(text, "env_vars") || strings.Contains(text, "args") || !strings.Contains(text, "tool_timeout_sec = 30") {
		t.Errorf("got:\n%s", text)
	}
}

func TestCRLFAndBOMKept(t *testing.T) {
	in := "\xef\xbb\xbf{\r\n  \"mcpServers\": {\r\n    \"keep\": {\"command\": \"k\"}\r\n  }\r\n}\r\n"
	p := write(t, "cfg", in)
	f := File{p, Cursor}
	if err := f.Put(stdioSrv); err != nil {
		t.Fatal(err)
	}
	text := slurp(t, p)
	if !strings.HasPrefix(text, "\xef\xbb\xbf") || strings.Count(text, "\r\n") != strings.Count(text, "\n") {
		t.Errorf("BOM or CRLF lost: %q", text)
	}
	if got, err := f.Read(); err != nil || got["fs"] == nil || got["keep"] == nil {
		t.Errorf("%v %v", got, err)
	}
	tomlIn := "\xef\xbb\xbfmodel = \"m\"\r\n\r\n[mcp_servers.k]\r\ncommand = \"k\"\r\n"
	p = write(t, "config.toml", tomlIn)
	f = File{p, Codex}
	if err := f.Put(stdioSrv); err != nil {
		t.Fatal(err)
	}
	text = slurp(t, p)
	if !strings.HasPrefix(text, "\xef\xbb\xbf") || strings.Count(text, "\r\n") != strings.Count(text, "\n") {
		t.Errorf("TOML BOM or CRLF lost: %q", text)
	}
}

func TestSymlinkedConfigKeepsLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "mcp.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := (File{link, Cursor}).Put(stdioSrv); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced")
	}
	if !strings.Contains(slurp(t, target), `"fs"`) {
		t.Error("the target was not written")
	}
}

func TestPutRestoresWhenItCannotRead(t *testing.T) {
	// a Codex file whose servers are written as dotted keys under [mcp_servers]
	// can't take a [mcp_servers.fs] table: the write is refused and the file
	// stays as it was
	in := "[mcp_servers]\nfs.command = \"x\"\n"
	p := write(t, "config.toml", in)
	if err := (File{p, Codex}).Put(stdioSrv); err == nil {
		t.Error("no error")
	}
	if slurp(t, p) != in {
		t.Errorf("file changed: %q", slurp(t, p))
	}
}

func TestValidateAndHash(t *testing.T) {
	bad := []*Server{
		{Name: "", Transport: "stdio", Command: "x"},
		{Name: "a b", Transport: "stdio", Command: "x"},
		{Name: "a.b", Transport: "stdio", Command: "x"},
		{Name: "a", Transport: "stdio"},
		{Name: "a", Transport: "stdio", Command: "  "},
		{Name: "a", Transport: "http", URL: "ftp://x"},
		{Name: "a", Transport: "sse"},
		{Name: "a", Transport: "ws", URL: "https://x"},
		{Name: "a", Transport: "stdio", Command: "x", Env: map[string]string{" ": "1"}},
	}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%+v is valid", s)
		}
	}
	for _, s := range []*Server{stdioSrv, bareSrv, httpSrv, sseSrv, keepSrv} {
		if err := s.Validate(); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
	}
	a := &Server{Name: "a", Transport: "stdio", Command: "x", Args: []string{"1"}, Env: map[string]string{"K": "v"}, URL: "ignored", Headers: map[string]string{"h": "1"}}
	b := &Server{Name: "b", Transport: "stdio", Command: " x ", Args: []string{"1"}, Env: map[string]string{"K": "v"}}
	if a.Hash() != b.Hash() {
		t.Error("name and the other transport's fields changed the hash")
	}
	for _, c := range []*Server{
		{Transport: "stdio", Command: "y", Args: []string{"1"}, Env: map[string]string{"K": "v"}},
		{Transport: "stdio", Command: "x", Args: []string{"1", "2"}, Env: map[string]string{"K": "v"}},
		{Transport: "stdio", Command: "x", Args: []string{"1"}, Env: map[string]string{"K": "w"}},
		{Transport: "stdio", Command: "x", Args: []string{"1"}},
		{Transport: "http", URL: "https://x"},
		{Transport: "sse", URL: "https://x"},
	} {
		if c.Hash() == a.Hash() {
			t.Errorf("%+v hashes like a", c)
		}
	}
	if (&Server{Transport: "stdio", Command: "x", Args: []string{"a b"}}).Hash() == (&Server{Transport: "stdio", Command: "x", Args: []string{"a", "b"}}).Hash() {
		t.Error("args are ambiguous in the hash")
	}
	if len(a.Hash()) != 32 || a.Hash() != a.Hash() {
		t.Error("hash is not stable")
	}
}

func TestPutManyAndDuplicates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg")
	f := File{p, Claude}
	second := *stdioSrv
	second.Command = "second"
	if err := f.Put(stdioSrv, httpSrv, &second); err != nil {
		t.Fatal(err)
	}
	got, _ := f.Read()
	if len(got) != 2 || got["fs"].Command != "second" {
		t.Errorf("%+v", got)
	}
	if err := f.Put(&Server{Name: "bad name", Transport: "stdio", Command: "x"}); err == nil {
		t.Error("an invalid server was written")
	}
	if err := f.Remove("bad.name"); err == nil {
		t.Error("an invalid name was removed")
	}
	if err := f.Put(); err != nil {
		t.Error(err)
	}
}

func TestYAMLRemoveToEmptyAndFlowMap(t *testing.T) {
	p := write(t, "config.yaml", "extensions: {}\nother: 1\n")
	f := File{p, Goose}
	if err := f.Put(stdioSrv); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Read(); got["fs"] == nil {
		t.Fatalf("%s", slurp(t, p))
	}
	if err := f.Remove("fs"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(slurp(t, p), "other: 1") {
		t.Errorf("%s", slurp(t, p))
	}
}
