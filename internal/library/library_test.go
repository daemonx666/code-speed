package library

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daemonx666/code-speed/internal/library/mcpfmt"
)

// a home with Claude Code (JSON), Cursor (JSON), Codex (TOML) and the
// shared skills folder, and a library in its own folder
func testManager(t *testing.T) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	for _, d := range []string{".claude/skills", ".cursor", ".codex", ".agents/skills"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := New(filepath.Join(home, "code-speed"))
	n := 0
	m.now = func() time.Time { n++; return time.Date(2026, 11, 14, 10, 0, n, 0, time.UTC) }
	m.users = func() []*Target {
		return []*Target{
			{ID: "claude", Agent: "claude", Label: "Claude Code", Dir: filepath.Join(home, ".claude"), Installed: true,
				Skills: filepath.Join(home, ".claude", "skills"), MCP: &mcpfmt.File{Path: filepath.Join(home, ".claude.json"), Format: mcpfmt.Claude}},
			{ID: "cursor", Agent: "cursor", Label: "Cursor", Dir: filepath.Join(home, ".cursor"), Installed: true,
				Skills: filepath.Join(home, ".cursor", "skills"), MCP: &mcpfmt.File{Path: filepath.Join(home, ".cursor", "mcp.json"), Format: mcpfmt.Cursor}},
			{ID: "codex", Agent: "codex", Label: "Codex", Dir: filepath.Join(home, ".codex"), Installed: true,
				Skills: filepath.Join(home, ".codex", "skills"), MCP: &mcpfmt.File{Path: filepath.Join(home, ".codex", "config.toml"), Format: mcpfmt.Codex}},
			{ID: Shared, Agent: Shared, Label: "shared", Dir: filepath.Join(home, ".agents"), Installed: true, Skills: filepath.Join(home, ".agents", "skills")},
		}
	}
	return m, home
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cell(t *testing.T, st *State, kind, name, target string) *Cell {
	t.Helper()
	items := st.MCP
	if kind == KindSkill {
		items = st.Skills
	}
	for _, it := range items {
		if it.Name == name {
			if c := it.Cells[target]; c != nil {
				return c
			}
		}
	}
	t.Fatalf("no %s %s in %s", kind, name, target)
	return nil
}

const gh = `"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": {"GITHUB_TOKEN": "t"}}`

func TestImportUntickAndGive(t *testing.T) {
	m, home := testManager(t)
	write(t, filepath.Join(home, ".claude.json"), `{"numStartups": 3, "mcpServers": {`+gh+`}}`)
	write(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": {`+gh+`, "own": {"command": "own"}}}`)
	write(t, filepath.Join(home, ".codex", "config.toml"), "# my codex\nmodel = \"gpt\"\n")

	st, err := m.State()
	if err != nil {
		t.Fatal(err)
	}
	var github *Found
	for _, f := range st.Found {
		if f.Name == "github" {
			github = f
		}
	}
	if github == nil || !slices.Equal(github.In, []string{"claude", "cursor"}) || github.Conflict {
		t.Fatalf("github found as %+v, want one item in claude and cursor", github)
	}

	if err := m.Import(nil); err != nil {
		t.Fatal(err)
	}
	st, _ = m.State()
	if c := cell(t, st, KindMCP, "github", "claude"); c.State != CellOn || !c.Want {
		t.Fatalf("after import, claude cell %+v", c)
	}
	if st.Pending != 0 {
		t.Fatalf("pending %d after import, want 0", st.Pending)
	}

	// untick Cursor, tick Codex
	if err := m.SetAgents(KindMCP, "github", []string{"claude", "codex"}); err != nil {
		t.Fatal(err)
	}
	st, _ = m.State()
	if c := cell(t, st, KindMCP, "github", "cursor"); c.State != CellRemove {
		t.Fatalf("cursor cell %+v, want remove", c)
	}
	if c := cell(t, st, KindMCP, "github", "codex"); c.State != CellAdd {
		t.Fatalf("codex cell %+v, want add", c)
	}
	res, err := m.Apply(ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasResult(res, "cursor", "github", "removed") || !hasResult(res, "codex", "github", "written") {
		t.Fatalf("results %+v", res)
	}
	cursor := read(t, filepath.Join(home, ".cursor", "mcp.json"))
	if strings.Contains(cursor, "github") || !strings.Contains(cursor, `"own"`) {
		t.Fatalf("cursor file after removing github:\n%s", cursor)
	}
	codex := read(t, filepath.Join(home, ".codex", "config.toml"))
	if !strings.HasPrefix(codex, "# my codex\nmodel = \"gpt\"\n") || !strings.Contains(codex, "[mcp_servers.github]") {
		t.Fatalf("codex file:\n%s", codex)
	}
	if claude := read(t, filepath.Join(home, ".claude.json")); !strings.Contains(claude, `"numStartups": 3`) {
		t.Fatalf("claude's other settings lost:\n%s", claude)
	}
	st, _ = m.State()
	if st.Pending != 0 {
		t.Fatalf("pending %d after apply", st.Pending)
	}
}

func hasResult(res []Result, target, name, action string) bool {
	return slices.ContainsFunc(res, func(r Result) bool { return r.Target == target && r.Name == name && r.Action == action })
}

func TestDriftIsLeftAloneUntilOverwritten(t *testing.T) {
	m, home := testManager(t)
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, `{"mcpServers": {`+gh+`}}`)
	if err := m.Import(nil); err != nil {
		t.Fatal(err)
	}
	// the user changes it in Claude Code, then edits the library's
	write(t, claude, `{"mcpServers": {"github": {"command": "npx", "args": ["-y", "@other/github"]}}}`)
	s := mcpfmt.Server{Name: "github", Transport: "stdio", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github", "--v2"}}
	if err := m.PutServer("github", s, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	st, _ := m.State()
	if c := cell(t, st, KindMCP, "github", "claude"); c.State != CellDrift {
		t.Fatalf("claude cell %+v, want drift", c)
	}
	res, _ := m.Apply(ApplyOptions{})
	if !hasResult(res, "claude", "github", "skipped") || !strings.Contains(read(t, claude), "@other/github") {
		t.Fatalf("drifted entry overwritten: %+v\n%s", res, read(t, claude))
	}
	res, _ = m.Apply(ApplyOptions{Overwrite: []string{"mcp/github/claude"}})
	if !hasResult(res, "claude", "github", "written") || !strings.Contains(read(t, claude), "--v2") {
		t.Fatalf("overwrite didn't write: %+v\n%s", res, read(t, claude))
	}
}

func TestAdoptTakesTheAgentsVersion(t *testing.T) {
	m, home := testManager(t)
	claude := filepath.Join(home, ".claude.json")
	write(t, claude, `{"mcpServers": {`+gh+`}}`)
	m.Import(nil)
	write(t, claude, `{"mcpServers": {"github": {"command": "gh-mcp"}}}`)
	if err := m.Adopt(KindMCP, "github", "claude"); err != nil {
		t.Fatal(err)
	}
	st, _ := m.State()
	if c := cell(t, st, KindMCP, "github", "claude"); c.State != CellOn {
		t.Fatalf("after adopt: %+v", c)
	}
	if st.MCP[0].Server.Command != "gh-mcp" {
		t.Fatalf("library server %+v", st.MCP[0].Server)
	}
}

func TestConflictNeedsAName(t *testing.T) {
	m, home := testManager(t)
	write(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"fs": {"command": "a"}}}`)
	write(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": {"fs": {"command": "b"}}}`)
	st, _ := m.State()
	if len(st.Found) != 2 || !st.Found[0].Conflict || !st.Found[1].Conflict {
		t.Fatalf("found %+v, want two conflicting versions", st.Found)
	}
	if err := m.Import(nil); err != nil {
		t.Fatal(err)
	}
	if st, _ = m.State(); len(st.MCP) != 0 {
		t.Fatalf("import all took a conflicting item: %+v", st.MCP)
	}
	var a, b *Found
	for _, f := range st.Found {
		if f.In[0] == "claude" {
			a = f
		} else {
			b = f
		}
	}
	if err := m.Import([]ImportReq{{Kind: KindMCP, Name: "fs", Hash: a.Hash}, {Kind: KindMCP, Name: "fs", Hash: b.Hash, As: "fs-cursor"}}); err != nil {
		t.Fatal(err)
	}
	st, _ = m.State()
	if len(st.MCP) != 2 {
		t.Fatalf("library %+v", st.MCP)
	}
	// the renamed one is given to no target, so Cursor's own fs stays
	if c := cell(t, st, KindMCP, "fs-cursor", "cursor"); c.Want {
		t.Fatalf("renamed import ticked: %+v", c)
	}
}

func TestUnreadableFileFailsAloneUntouched(t *testing.T) {
	m, home := testManager(t)
	bad := filepath.Join(home, ".cursor", "mcp.json")
	write(t, bad, `{"mcpServers": {`)
	if err := m.PutServer("", mcpfmt.Server{Name: "fs", Transport: "stdio", Command: "fs"}, []string{"claude", "cursor"}); err != nil {
		t.Fatal(err)
	}
	res, err := m.Apply(ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasResult(res, "cursor", "fs", "failed") || !hasResult(res, "claude", "fs", "written") {
		t.Fatalf("results %+v", res)
	}
	if read(t, bad) != `{"mcpServers": {` {
		t.Fatal("unreadable file was written")
	}
}

func TestSkillsCopyDeleteAndRestore(t *testing.T) {
	m, home := testManager(t)
	write(t, filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md"), "---\nname: pdf\ndescription: Read PDFs\n---\nUse it.\n")
	write(t, filepath.Join(home, ".claude", "skills", "pdf", "scripts", "x.py"), "print(1)\n")
	if err := m.Import(nil); err != nil {
		t.Fatal(err)
	}
	if err := m.SetAgents(KindSkill, "pdf", []string{"claude", "codex", Shared}); err != nil {
		t.Fatal(err)
	}
	res, err := m.Apply(ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".codex/skills/pdf/scripts/x.py", ".agents/skills/pdf/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(home, d)); err != nil {
			t.Fatalf("%s not copied (%+v)", d, res)
		}
	}
	st, _ := m.State()
	if st.Skills[0].Description != "Read PDFs" || cell(t, st, KindSkill, "pdf", "codex").State != CellOn {
		t.Fatalf("skill state %+v", st.Skills[0])
	}

	res, err = m.Delete(KindSkill, "pdf")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".claude/skills/pdf", ".codex/skills/pdf", ".agents/skills/pdf", "code-speed/library/skills/pdf"} {
		if _, err := os.Stat(filepath.Join(home, d)); err == nil {
			t.Fatalf("%s left after delete (%+v)", d, res)
		}
	}

	// the delete's backup puts every copy and the library back
	bs, _ := m.Backups()
	if len(bs) == 0 || !strings.HasPrefix(bs[0].What, "删除") {
		t.Fatalf("backups %+v", bs)
	}
	// the newest is the apply the delete ran; the one before holds the store
	for _, b := range bs[:2] {
		if err := m.Restore(b.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{".claude/skills/pdf/SKILL.md", ".codex/skills/pdf/SKILL.md", ".agents/skills/pdf/SKILL.md", "code-speed/library/skills/pdf/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(home, d)); err != nil {
			t.Fatalf("%s not restored", d)
		}
	}
	if st, _ = m.State(); len(st.Skills) != 1 {
		t.Fatalf("library not restored: %+v", st.Skills)
	}
}

func TestSkillLinkIsNotReplaced(t *testing.T) {
	m, home := testManager(t)
	other := filepath.Join(home, "elsewhere", "pdf")
	write(t, filepath.Join(other, "SKILL.md"), "other tool's\n")
	if err := os.Symlink(other, filepath.Join(home, ".codex", "skills", "pdf")); err != nil {
		if mkErr := os.MkdirAll(filepath.Join(home, ".codex", "skills"), 0o755); mkErr == nil {
			if err = os.Symlink(other, filepath.Join(home, ".codex", "skills", "pdf")); err != nil {
				t.Skip("no symlinks here:", err)
			}
		}
	}
	src := filepath.Join(home, "mine")
	write(t, filepath.Join(src, "SKILL.md"), "mine\n")
	if err := m.PutSkill("pdf", src, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	res, _ := m.Apply(ApplyOptions{})
	if !hasResult(res, "codex", "pdf", "skipped") || read(t, filepath.Join(other, "SKILL.md")) != "other tool's\n" {
		t.Fatalf("link replaced: %+v", res)
	}
}

func TestProjectTargets(t *testing.T) {
	m, home := testManager(t)
	proj := filepath.Join(home, "code", "app")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.AddProject(proj); err != nil {
		t.Fatal(err)
	}
	id := ProjectTargetID(proj, "claude")
	if err := m.PutServer("", mcpfmt.Server{Name: "fs", Transport: "stdio", Command: "fs"}, []string{id}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(proj, ".mcp.json")), `"fs"`) {
		t.Fatal("not written into the project's .mcp.json")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		t.Fatal("the user-wide file was written too")
	}
	res, err := m.RemoveProject(proj)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, filepath.Join(proj, ".mcp.json")), `"fs"`) || !hasResult(res, id, "fs", "removed") {
		t.Fatalf("project not cleaned: %+v", res)
	}
	if st, _ := m.State(); len(st.Projects) != 0 {
		t.Fatalf("project kept: %v", st.Projects)
	}
}

func TestUserTargetsHonourEnv(t *testing.T) {
	h := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(h, "cx"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	var codex, claude *Target
	for _, x := range userTargets(h) {
		switch x.ID {
		case "codex":
			codex = x
		case "claude":
			claude = x
		}
	}
	if codex.MCP.Path != filepath.Join(h, "cx", "config.toml") || codex.Skills != filepath.Join(h, "cx", "skills") {
		t.Fatalf("codex %+v", codex)
	}
	if claude.MCP.Path != filepath.Join(h, ".claude.json") || claude.Installed {
		t.Fatalf("claude %+v", claude)
	}
}

func TestCheckName(t *testing.T) {
	for _, ok := range []string{"github", "fs_2", "a-b_c"} {
		if CheckName(ok) != nil {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "a b", "a.b", "../x", "-x", strings.Repeat("a", 65)} {
		if CheckName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The window applies one item as soon as it is ticked: other items' changes
// are left for their own apply.
func TestApplyLimitedToAnItem(t *testing.T) {
	m, home := testManager(t)
	for _, n := range []string{"a", "b"} {
		if err := m.PutServer("", mcpfmt.Server{Name: n, Transport: "stdio", Command: n}, []string{"claude"}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := m.Apply(ApplyOptions{Items: []string{"mcp/a"}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasResult(res, "claude", "a", "written") || hasResult(res, "claude", "b", "written") {
		t.Fatalf("results %+v", res)
	}
	claude := read(t, filepath.Join(home, ".claude.json"))
	if !strings.Contains(claude, `"a"`) || strings.Contains(claude, `"b"`) {
		t.Fatalf("claude file:\n%s", claude)
	}
	st, _ := m.State()
	if st.Pending != 1 {
		t.Fatalf("pending %d, want b's 1", st.Pending)
	}
	// unticking a takes it out, and still leaves b alone
	if err := m.SetAgents(KindMCP, "a", nil); err != nil {
		t.Fatal(err)
	}
	res, _ = m.Apply(ApplyOptions{Items: []string{"mcp/a"}})
	if !hasResult(res, "claude", "a", "removed") || len(res) != 1 {
		t.Fatalf("results %+v", res)
	}
}
