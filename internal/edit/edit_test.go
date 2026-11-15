package edit

import (
	"os"
	"strings"
	"testing"
)

func TestJSONSetKeepsCommentsOrderIndent(t *testing.T) {
	in := "{\n    // my servers\n    \"mcpServers\": {\n        \"a\": {\"command\": \"x\"}, // keep\n        \"b\": 1\n    },\n    \"z\": true,\n}\n"
	p := tmpFile(t, "s.jsonc", in)
	if err := SetJSON(p, KV{Path: "mcpServers.c", Value: map[string]any{"command": "y"}}); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{"// my servers", "// keep", `"z": true,`, "\"c\": {\n            \"command\": \"y\"\n        }"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if err := DelJSON(p, "mcpServers.c"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != in {
		t.Errorf("delete did not restore the file:\n%s", got)
	}
}

func TestJSONCreatesMissingFileAndDirs(t *testing.T) {
	p := tmpFile(t, "a/b/c.json", "")
	if err := SetJSON(p, KV{Path: "mcp.servers.x", Value: 1}); err != nil {
		t.Fatal(err)
	}
	if v, ok := GetJSON(p, "mcp.servers.x"); !ok || v != "1" {
		t.Errorf("got %q %v", v, ok)
	}
}

func TestJSONInvalidRefused(t *testing.T) {
	in := "{ \"a\": [1, 2 "
	p := tmpFile(t, "bad.json", in)
	if err := SetJSON(p, KV{Path: "b", Value: 1}); err == nil {
		t.Error("SetJSON accepted an unparseable file")
	}
	if err := DelJSON(p, "a"); err == nil {
		t.Error("DelJSON accepted an unparseable file")
	}
	if got := read(t, p); got != in {
		t.Errorf("file changed: %q", got)
	}
}

func TestJSONCRLFKept(t *testing.T) {
	p := tmpFile(t, "s.json", "{\r\n  \"a\": 1\r\n}\r\n")
	if err := SetJSON(p, KV{Path: "b", Value: map[string]any{"c": 1}}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); strings.Count(got, "\r\n") != strings.Count(got, "\n") {
		t.Errorf("mixed line endings: %q", got)
	}
}

const tomlIn = `# Codex config
model = "gpt"

# the fs server
[mcp_servers.fs]
command = "npx" # old
args = ["a"]

[mcp_servers.fs.env]
K = "v"

# keep me, I belong to the next table
[profiles.x]
model = "y"

[projects."/a"]
trust = "trusted"
`

func TestTOMLSetTableKeepsRestAndCommentAbove(t *testing.T) {
	p := tmpFile(t, "config.toml", tomlIn)
	if err := DelTOMLTablesUnder(p, "mcp_servers.fs."); err != nil {
		t.Fatal(err)
	}
	if err := SetTOMLTable(p, "mcp_servers.fs", KV{"command", Raw(`"uvx"`)}, KV{"args", Raw(`["b", "c"]`)}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(tomlIn, "command = \"npx\" # old\nargs = [\"a\"]\n\n[mcp_servers.fs.env]\nK = \"v\"\n", "command = \"uvx\"\nargs = [\"b\", \"c\"]\n", 1)
	if got := read(t, p); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTOMLDelTableLeavesNeighbours(t *testing.T) {
	p := tmpFile(t, "config.toml", tomlIn)
	if err := DelTOMLTablesUnder(p, "mcp_servers.fs."); err != nil {
		t.Fatal(err)
	}
	if err := DelTOMLTable(p, "mcp_servers.fs"); err != nil {
		t.Fatal(err)
	}
	want := "# Codex config\nmodel = \"gpt\"\n\n# keep me, I belong to the next table\n[profiles.x]\nmodel = \"y\"\n\n[projects.\"/a\"]\ntrust = \"trusted\"\n"
	if got := read(t, p); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// absent: untouched
	before := read(t, p)
	if err := DelTOMLTable(p, "mcp_servers.nope"); err != nil || read(t, p) != before {
		t.Errorf("absent table changed the file: %v", err)
	}
}

func TestTOMLAppendAndCreate(t *testing.T) {
	p := tmpFile(t, "d/config.toml", "")
	if err := SetTOMLTable(p, "mcp_servers.a", KV{"url", Raw(TOMLString("http://x"))}); err != nil {
		t.Fatal(err)
	}
	if err := SetTOMLTable(p, "mcp_servers.b", KV{"command", Raw(`"c"`)}); err != nil {
		t.Fatal(err)
	}
	want := "[mcp_servers.a]\nurl = \"http://x\"\n\n[mcp_servers.b]\ncommand = \"c\"\n"
	if got := read(t, p); got != want {
		t.Errorf("got:\n%s", got)
	}
}

func TestTOMLBrokenRefusedAndDuplicateRefused(t *testing.T) {
	for _, in := range []string{"[a\nx = 1\n", "[a]\nx = 1\n[a]\ny = 2\n"} {
		p := tmpFile(t, "c.toml", in)
		if err := SetTOMLTable(p, "mcp_servers.z", KV{"command", Raw(`"c"`)}); err == nil {
			t.Errorf("accepted %q", in)
		}
		if got := read(t, p); got != in {
			t.Errorf("file changed: %q", got)
		}
	}
}

func TestTOMLStringEscapes(t *testing.T) {
	if got := TOMLString("a\"b\\c\n\x01é"); got != `"a\"b\\c\n\u0001é"` {
		t.Errorf("got %s", got)
	}
}

func TestTOMLWithoutProjects(t *testing.T) {
	got := string(TOMLWithout([]byte(tomlIn), "projects"))
	if strings.Contains(got, "projects") || !strings.Contains(got, "[profiles.x]") {
		t.Errorf("got:\n%s", got)
	}
}

func TestTOMLArrayTableCannotBeSet(t *testing.T) {
	p := tmpFile(t, "c.toml", "[[a]]\nx = 1\n")
	if err := SetTOMLTable(p, "a", KV{"y", 1}); err == nil {
		t.Error("an array table was replaced")
	}
}

const yamlIn = "# goose config\nGOOSE_MODEL: x # model\nextensions:\n  developer:\n    enabled: true # builtin\n    type: builtin\n  fs:\n    enabled: true\n    cmd: npx\n"

func TestYAMLSetDelKeepsComments(t *testing.T) {
	p := tmpFile(t, "config.yaml", yamlIn)
	if err := SetYAML(p, KV{Path: "extensions.gh", Value: map[string]any{"enabled": true, "type": "sse"}}); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, want := range []string{"# goose config", "# model", "# builtin", "  gh:\n    enabled: true\n    type: sse"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if err := DelYAML(p, "extensions.gh"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != yamlIn {
		t.Errorf("not restored:\n%s", got)
	}
}

func TestYAMLInvalidRefused(t *testing.T) {
	for _, in := range []string{"a: [1, 2\n", "a: 1\na: 2\n", "- x\n- y\n"} {
		p := tmpFile(t, "c.yaml", in)
		if err := SetYAML(p, KV{Path: "b.c", Value: 1}); err == nil {
			t.Errorf("accepted %q", in)
		}
		if err := DelYAML(p, "a"); err == nil {
			t.Errorf("delete accepted %q", in)
		}
		if got := read(t, p); got != in {
			t.Errorf("file changed: %q", got)
		}
	}
}

func TestYAMLMissingFileDelIsNoop(t *testing.T) {
	p := tmpFile(t, "c.yaml", "")
	if err := DelYAML(p, "a.b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err == nil {
		t.Error("DelYAML made the file")
	}
}

func TestAtomicallyRestoresOnFailure(t *testing.T) {
	p := tmpFile(t, "c.json", "{\"a\": 1}\n")
	n := tmpFile(t, "new.json", "")
	err := Atomically(func() error {
		if err := SetJSON(p, KV{Path: "b", Value: 2}); err != nil {
			return err
		}
		if err := SetJSON(n, KV{Path: "b", Value: 2}); err != nil {
			return err
		}
		return os.ErrInvalid
	}, p, n)
	if err == nil {
		t.Fatal("no error")
	}
	if got := read(t, p); got != "{\"a\": 1}\n" {
		t.Errorf("not restored: %q", got)
	}
	if _, err := os.Stat(n); err == nil {
		t.Error("created file not removed")
	}
}
