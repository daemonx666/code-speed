// Package claudebridge reads Claude Code's subscription limits through its
// status line. After each response the Claude Code CLI runs the user's
// status-line command with JSON on standard input; for Pro, Max and Ultra
// subscribers that JSON carries rate_limits (five_hour and seven_day).
// code-speed's own binary, run as `code-speed statusline`, is that command:
// it keeps the limits in a small file and then runs the status line the
// user had before, so nothing they see changes.
//
// Turning the bridge on edits one member of Claude Code's settings file,
// statusLine, keeping the others as they were; turning it off puts the
// previous value back. The rules follow Agent Speed's specification
// (spec/QUOTAS.md §5, MIT, github.com/zoefix/agent-speed). Claude Desktop
// never runs the status line, so only the CLI's account is seen.
package claudebridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/pretty"
	"github.com/tidwall/sjson"

	"github.com/daemonx666/code-speed/internal/appdir"
)

// Status of the bridge, for Settings.
const (
	StatusOff        = "off"        // not installed
	StatusWaiting    = "waiting"    // installed; no limits seen yet (Claude Code hasn't answered since)
	StatusOK         = "ok"         // installed, limits seen
	StatusReplaced   = "replaced"   // the status line is someone else's now
	StatusUnreadable = "unreadable" // Claude Code's settings file isn't a JSON object
)

// marker is how our command is recognised in a status line.
const marker = "statusline"

// SettingsPath is Claude Code's user settings file.
func SettingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		if i := strings.IndexByte(d, ','); i >= 0 {
			d = d[:i]
		}
		return filepath.Join(strings.TrimSpace(d), "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func quotaDir() string { return filepath.Join(appdir.Dir(), "quota") }

// QuotaPath is the file the helper writes the limits to.
func QuotaPath() string { return filepath.Join(quotaDir(), "claude-cli.json") }

func previousPath() string { return filepath.Join(quotaDir(), "claude-statusline-previous.json") }
func backupPath() string   { return filepath.Join(quotaDir(), "claude-settings.backup.json") }

// Command is the status-line command that runs exe as the helper.
func Command(exe string) string {
	if runtime.GOOS == "windows" {
		return `"` + filepath.ToSlash(exe) + `" ` + marker
	}
	return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "' " + marker
}

// isOurs reports whether a statusLine value is our command.
func isOurs(v gjson.Result) bool {
	if !v.IsObject() || v.Get("type").String() != "command" {
		return false
	}
	c := v.Get("command").String()
	return strings.Contains(c, "code-speed") && strings.HasSuffix(strings.TrimSpace(c), " "+marker)
}

func readSettings() ([]byte, error) {
	b, err := os.ReadFile(SettingsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return []byte("{}"), nil
	}
	if err != nil {
		return nil, err
	}
	if !gjson.ValidBytes(b) || !gjson.ParseBytes(b).IsObject() {
		return nil, errNotObject
	}
	return b, nil
}

var errNotObject = errors.New("Claude Code's settings file is not a JSON object")

// writeFile writes through a temporary file, keeping the old file's mode.
func writeFile(path string, b []byte, mode os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".code-speed.tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// format writes JSON the way Claude Code does: two-space indentation, the
// members in their order, a final newline.
func format(b []byte) []byte {
	out := pretty.PrettyOptions(b, &pretty.Options{Width: 0, Indent: "  ", SortKeys: false})
	return append(bytes.TrimRight(out, "\n"), '\n')
}

// Install points Claude Code's status line at exe. The status line there
// before is kept, to be run by the helper and put back by Uninstall.
// Installing again (a moved app) only rewrites the command.
func Install(exe string) error {
	b, err := readSettings()
	if err != nil {
		return err
	}
	cur := gjson.GetBytes(b, "statusLine")
	if !isOurs(cur) {
		prev := []byte(`{"version":1}`)
		if cur.Exists() {
			prev, _ = sjson.SetRawBytes(prev, "statusLine", []byte(cur.Raw))
		}
		if err := writeFile(previousPath(), prev, 0o600); err != nil {
			return err
		}
		if err := writeFile(backupPath(), b, 0o600); err != nil {
			return err
		}
	}
	line := map[string]any{"type": "command", "command": Command(exe)}
	if p := cur.Get("padding"); p.Type == gjson.Number {
		line["padding"] = p.Num
	}
	raw, _ := json.Marshal(line)
	if cur.Exists() && isOurs(cur) && cur.Get("command").String() == line["command"] {
		return nil // already in place
	}
	nb, err := sjson.SetRawBytes(b, "statusLine", raw)
	if err != nil {
		return err
	}
	return writeFile(SettingsPath(), format(nb), 0o644)
}

// Uninstall puts the previous status line back, if ours is still there,
// and removes the bridge's files.
func Uninstall() error {
	defer func() {
		os.Remove(previousPath())
		os.Remove(backupPath())
		os.Remove(QuotaPath())
	}()
	b, err := readSettings()
	if err != nil {
		return err
	}
	if !isOurs(gjson.GetBytes(b, "statusLine")) {
		return nil // the user replaced it: leave their file alone
	}
	var nb []byte
	if prev := previous(); prev.Exists() {
		nb, err = sjson.SetRawBytes(b, "statusLine", []byte(prev.Raw))
	} else {
		nb, err = sjson.DeleteBytes(b, "statusLine")
	}
	if err != nil {
		return err
	}
	return writeFile(SettingsPath(), format(nb), 0o644)
}

// previous is the status line there was before ours.
func previous() gjson.Result {
	b, err := os.ReadFile(previousPath())
	if err != nil {
		return gjson.Result{}
	}
	return gjson.GetBytes(b, "statusLine")
}

// Status reports the bridge's state.
func Status() string {
	b, err := readSettings()
	if err != nil {
		if errors.Is(err, errNotObject) {
			return StatusUnreadable
		}
		return StatusOff
	}
	if !isOurs(gjson.GetBytes(b, "statusLine")) {
		if _, err := os.Stat(previousPath()); err == nil {
			return StatusReplaced
		}
		return StatusOff
	}
	if _, err := os.Stat(QuotaPath()); err == nil {
		return StatusOK
	}
	return StatusWaiting
}
