package library

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/daemonx666/code-speed/internal/library/mcpfmt"
)

// A Target is one place the library can give skills and MCP servers to:
// an agent's own user-wide folders, or its folders in one project. The
// places and formats are magpie's (github.com/yetone/magpie, MIT,
// internal/library/targets.go and project_mcp.go), trimmed to the agents
// code-speed knows.
type Target struct {
	ID      string `json:"id"`                // "claude", or "proj:<root>#claude"
	Agent   string `json:"agent"`             // the agent's id
	Label   string `json:"label"`             // shown name
	Project string `json:"project,omitempty"` // the project's folder; "" user-wide
	// Dir is the agent's folder: it being there is what says the agent is
	// installed (for a project target, the agent's user-wide folder)
	Dir       string       `json:"dir"`
	Skills    string       `json:"skills,omitempty"` // the folder it finds skills in; "" none
	MCP       *mcpfmt.File `json:"mcp,omitempty"`    // the file it reads servers from; nil none
	Installed bool         `json:"installed"`
	// SkillsVia names another target whose skills folder this agent reads
	// instead of one of its own (Goose reads ~/.agents/skills)
	SkillsVia string `json:"skillsVia,omitempty"`
}

// Shared is the target for ~/.agents/skills, which several agents read.
const Shared = "shared"

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// envDir is the variable's folder when set to an absolute path, else def.
func envDir(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" && filepath.IsAbs(v) {
		return v
	}
	return def
}

// xdgConfig is $XDG_CONFIG_HOME, else ~/.config (the agents built on
// Node's or Rust's XDG helpers use it on every system).
func xdgConfig(home string) string { return envDir("XDG_CONFIG_HOME", filepath.Join(home, ".config")) }

func mcp(path string, f mcpfmt.Format) *mcpfmt.File { return &mcpfmt.File{Path: path, Format: f} }

// UserTargets are the user-wide places of every agent the library knows,
// installed or not (Installed says), in the order the page lists them.
func UserTargets() []*Target { return userTargets(homeDir()) }

var homeDir = func() string { h, _ := os.UserHomeDir(); return h }

func userTargets(h string) []*Target {
	var out []*Target
	add := func(t *Target) {
		t.Agent = t.ID
		if t.Dir != "" {
			t.Installed = isDir(t.Dir)
		}
		out = append(out, t)
	}

	// Claude Code: CLAUDE_CONFIG_DIR moves its folder and its .claude.json
	// with it; the default ~/.claude's servers are in ~/.claude.json
	claude := envDir("CLAUDE_CONFIG_DIR", filepath.Join(h, ".claude"))
	claudeJSON := filepath.Join(claude, ".claude.json")
	if filepath.Clean(claude) == filepath.Join(h, ".claude") {
		claudeJSON = filepath.Join(h, ".claude.json")
	}
	add(&Target{ID: "claude", Label: "Claude Code", Dir: claude, Skills: filepath.Join(claude, "skills"), MCP: mcp(claudeJSON, mcpfmt.Claude)})

	codex := envDir("CODEX_HOME", filepath.Join(h, ".codex"))
	add(&Target{ID: "codex", Label: "Codex", Dir: codex, Skills: filepath.Join(codex, "skills"), MCP: mcp(filepath.Join(codex, "config.toml"), mcpfmt.Codex)})

	gemini := filepath.Join(envDir("GEMINI_CLI_HOME", h), ".gemini")
	add(&Target{ID: "gemini", Label: "Gemini CLI", Dir: gemini, Skills: filepath.Join(gemini, "skills"), MCP: mcp(filepath.Join(gemini, "settings.json"), mcpfmt.Gemini)})

	// Qwen Code is Gemini CLI's fork: the same settings shape
	qwen := envDir("QWEN_HOME", filepath.Join(h, ".qwen"))
	add(&Target{ID: "qwen", Label: "Qwen Code", Dir: qwen, Skills: filepath.Join(qwen, "skills"), MCP: mcp(filepath.Join(qwen, "settings.json"), mcpfmt.Gemini)})

	// OpenCode: opencode.jsonc wins when it is the one there
	oc := filepath.Join(xdgConfig(h), "opencode")
	ocFile := filepath.Join(oc, "opencode.json")
	if c := filepath.Join(oc, "opencode.jsonc"); exists(c) && !exists(ocFile) {
		ocFile = c
	}
	add(&Target{ID: "opencode", Label: "OpenCode", Dir: oc, Skills: filepath.Join(oc, "skills"), MCP: mcp(ocFile, mcpfmt.OpenCode)})

	cursor := filepath.Join(h, ".cursor")
	add(&Target{ID: "cursor", Label: "Cursor", Dir: cursor, Skills: filepath.Join(cursor, "skills"), MCP: mcp(filepath.Join(cursor, "mcp.json"), mcpfmt.Cursor)})

	copilot := envDir("COPILOT_HOME", filepath.Join(h, ".copilot"))
	add(&Target{ID: "copilot", Label: "Copilot CLI", Dir: copilot, Skills: filepath.Join(copilot, "skills"), MCP: mcp(filepath.Join(copilot, "mcp-config.json"), mcpfmt.Copilot)})

	// Kimi Code 2.x (the one code-speed reads): its own folder for skills
	// and mcp.json
	kimi := envDir("KIMI_CODE_HOME", filepath.Join(h, ".kimi-code"))
	add(&Target{ID: "kimi", Label: "Kimi Code", Dir: kimi, Skills: filepath.Join(kimi, "skills"), MCP: mcp(filepath.Join(kimi, "mcp.json"), mcpfmt.Kimi)})

	// Pi reads its own MCP servers from mcp.json since 0.99
	pi := envDir("PI_CODING_AGENT_DIR", filepath.Join(h, ".pi", "agent"))
	add(&Target{ID: "pi", Label: "Pi", Dir: pi, Skills: filepath.Join(pi, "skills"), MCP: mcp(filepath.Join(pi, "mcp.json"), mcpfmt.PiNative)})

	omp := filepath.Join(h, ".omp", "agent")
	add(&Target{ID: "omp", Label: "Oh My Pi", Dir: omp, Skills: filepath.Join(omp, "skills"), MCP: mcp(filepath.Join(omp, "mcp.json"), mcpfmt.Omp)})

	droid := filepath.Join(h, ".factory")
	add(&Target{ID: "droid", Label: "Factory Droid", Dir: droid, Skills: filepath.Join(droid, "skills"), MCP: mcp(filepath.Join(droid, "mcp.json"), mcpfmt.Claude)})

	grok := envDir("GROK_HOME", filepath.Join(h, ".grok"))
	add(&Target{ID: "grok", Label: "Grok Build", Dir: grok, Skills: filepath.Join(grok, "skills"), MCP: mcp(filepath.Join(grok, "config.toml"), mcpfmt.Grok)})

	qoder := filepath.Join(h, ".qoder")
	add(&Target{ID: "qoder", Label: "Qoder CLI", Dir: qoder, Skills: filepath.Join(qoder, "skills"), MCP: mcp(filepath.Join(qoder, "settings.json"), mcpfmt.Omp)})

	// Antigravity (the app and agy) reads ~/.gemini/config
	agy := filepath.Join(h, ".gemini", "config")
	add(&Target{ID: "antigravity", Label: "Antigravity", Dir: agy, Skills: filepath.Join(agy, "skills"), MCP: mcp(filepath.Join(agy, "mcp_config.json"), mcpfmt.Antigravity)})

	zcode := filepath.Join(h, ".zcode")
	add(&Target{ID: "zcode", Label: "ZCode", Dir: zcode, Skills: filepath.Join(zcode, "skills"), MCP: mcp(filepath.Join(zcode, "cli", "config.json"), mcpfmt.ZCode)})

	hermes := hermesHome(h)
	add(&Target{ID: "hermes", Label: "Hermes Agent", Dir: hermes, Skills: filepath.Join(hermes, "skills"), MCP: mcp(filepath.Join(hermes, "config.yaml"), mcpfmt.Hermes)})

	// Goose reads ~/.agents/skills (the shared target) and its servers are
	// config.yaml's extensions
	goose := gooseConfig(h)
	add(&Target{ID: "goose", Label: "Goose", Dir: goose, SkillsVia: Shared, MCP: mcp(filepath.Join(goose, "config.yaml"), mcpfmt.Goose)})

	// Cline: the CLI's (and 4.x extension's) data folder
	cline := clineData(h)
	clineMCP := envDir("CLINE_MCP_SETTINGS_PATH", "")
	if clineMCP == "" {
		clineMCP = filepath.Join(cline, "settings", "cline_mcp_settings.json")
	}
	add(&Target{ID: "cline", Label: "Cline", Dir: filepath.Dir(cline), Skills: filepath.Join(filepath.Dir(cline), "skills"), MCP: mcp(clineMCP, mcpfmt.Cline)})

	for _, w := range []struct{ id, label, dir string }{{"workbuddy", "WorkBuddy", ".workbuddy"}, {"workbuddy-ai", "WorkBuddy AI", ".workbuddy-ai"}} {
		d := filepath.Join(h, w.dir)
		add(&Target{ID: w.id, Label: w.label, Dir: d, Skills: filepath.Join(d, "skills"), MCP: mcp(filepath.Join(d, "mcp.json"), mcpfmt.Claude)})
	}

	// DeepSeek Harness: skills only, its servers are in profile patches
	dsh := envDir("DSH_HOME", filepath.Join(h, ".dsh"))
	add(&Target{ID: "dsh", Label: "DeepSeek Harness", Dir: dsh, Skills: filepath.Join(dsh, "skills")})

	// ~/.agents/skills: read by Codex, Gemini CLI, OpenCode, Goose, Cline,
	// Copilot, Kimi and more besides their own; there whenever its parent is
	shared := filepath.Join(h, ".agents")
	t := &Target{ID: Shared, Label: "~/.agents", Dir: shared, Skills: filepath.Join(shared, "skills")}
	t.Agent, t.Installed = Shared, true
	out = append(out, t)
	return out
}

func hermesHome(h string) string {
	def := filepath.Join(h, ".hermes")
	if runtime.GOOS == "windows" {
		def = filepath.Join(envDir("LOCALAPPDATA", filepath.Join(h, "AppData", "Local")), "hermes")
	}
	return envDir("HERMES_HOME", def)
}

func gooseConfig(h string) string {
	if root := envDir("GOOSE_PATH_ROOT", ""); root != "" {
		return filepath.Join(root, "config")
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(envDir("APPDATA", filepath.Join(h, "AppData", "Roaming")), "Block", "goose", "config")
	}
	return filepath.Join(xdgConfig(h), "goose")
}

func clineData(h string) string {
	if d := envDir("CLINE_DATA_DIR", ""); d != "" {
		return d
	}
	if d := envDir("CLINE_DIR", ""); d != "" {
		return filepath.Join(d, "data")
	}
	return filepath.Join(h, ".cline", "data")
}

// projectPlaces are where each agent reads a project's own skills and
// servers, relative to the project's folder (magpie's projectMCPFiles,
// and each agent's docs for skills). An agent missing here has none.
var projectPlaces = []struct {
	agent  string
	skills string
	mcp    string
	format mcpfmt.Format
}{
	{"claude", ".claude/skills", ".mcp.json", mcpfmt.Claude},
	{"codex", "", ".codex/config.toml", mcpfmt.Codex},
	{"gemini", ".gemini/skills", ".gemini/settings.json", mcpfmt.Gemini},
	{"qwen", ".qwen/skills", ".qwen/settings.json", mcpfmt.Gemini},
	{"opencode", ".opencode/skills", "opencode.json", mcpfmt.OpenCode},
	{"cursor", ".cursor/skills", ".cursor/mcp.json", mcpfmt.Cursor},
	{"copilot", ".github/skills", ".github/mcp.json", mcpfmt.Copilot},
	{"pi", ".pi/skills", ".pi/mcp.json", mcpfmt.PiNative},
	{"qoder", ".qoder/skills", ".qoder/settings.json", mcpfmt.Omp},
	{"droid", ".factory/skills", ".factory/mcp.json", mcpfmt.Claude},
	{"zcode", ".zcode/skills", ".zcode/config.json", mcpfmt.ZCode},
	{Shared, ".agents/skills", "", ""},
}

// ProjectTargetID is the id of an agent's places in a project.
func ProjectTargetID(root, agent string) string { return "proj:" + root + "#" + agent }

// ProjectTargets are each agent's places in the project at root. An agent
// counts as installed there when it is installed user-wide.
func ProjectTargets(root string, user []*Target) []*Target {
	byID := map[string]*Target{}
	for _, t := range user {
		byID[t.ID] = t
	}
	var out []*Target
	for _, p := range projectPlaces {
		u := byID[p.agent]
		if u == nil {
			continue
		}
		t := &Target{ID: ProjectTargetID(root, p.agent), Agent: p.agent, Label: u.Label, Project: root, Dir: u.Dir, Installed: u.Installed}
		if p.skills != "" {
			t.Skills = filepath.Join(root, filepath.FromSlash(p.skills))
		}
		if p.mcp != "" {
			t.MCP = mcp(filepath.Join(root, filepath.FromSlash(p.mcp)), p.format)
		}
		out = append(out, t)
	}
	return out
}
