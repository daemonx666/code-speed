package claudebridge

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func setup(t *testing.T, settings string) string {
	t.Helper()
	t.Setenv("CODE_SPEED_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	if settings != "" {
		if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

const userSettings = `{
  "model": "opus",
  "permissions": {
    "allow": [
      "Bash(go test:*)"
    ],
    "deny": []
  },
  "env": {
    "A": "1"
  }
}
`

const exe = "/Applications/code-speed.app/Contents/MacOS/code-speed"

func TestInstallAndUninstallWithoutAPreviousStatusLine(t *testing.T) {
	path := setup(t, userSettings)
	if Status() != StatusOff {
		t.Fatalf("status before: %s", Status())
	}
	if err := Install(exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	// the other members keep their order and indentation; ours is appended
	if !strings.HasPrefix(s, "{\n  \"model\": \"opus\",\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(go test:*)\"\n    ],") {
		t.Fatalf("settings rewritten differently:\n%s", s)
	}
	if !strings.Contains(s, `"statusLine": {`) || !strings.Contains(s, strings.TrimSuffix(strings.TrimPrefix(strconv.Quote(Command(exe)), `"`), `"`)) {
		t.Fatalf("no status line:\n%s", s)
	}
	if Status() != StatusWaiting {
		t.Fatalf("status after install: %s", Status())
	}
	if err := Install(exe); err != nil { // again: no change, previous kept
		t.Fatal(err)
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != userSettings {
		t.Fatalf("uninstall did not restore the file:\n%s", after)
	}
	if Status() != StatusOff {
		t.Fatalf("status after uninstall: %s", Status())
	}
}

func TestPreviousStatusLineIsKeptRunAndRestored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs /bin/sh")
	}
	orig := "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"cat > /dev/null; echo mine\",\n    \"padding\": 2\n  }\n}\n"
	path := setup(t, orig)
	if err := Install(exe); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"padding": 2`) {
		t.Fatalf("padding lost:\n%s", b)
	}
	var out bytes.Buffer
	in := `{"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":` + itoa(time.Now().Add(time.Hour).Unix()) + `}}}`
	if code := RunHelper(strings.NewReader(in), &out); code != 0 || out.String() != "mine\n" {
		t.Fatalf("previous status line: %q (exit %d)", out.String(), code)
	}
	if q, _, ok := Read(); !ok || q.Windows[0].Kind != "fiveHour" || q.Windows[0].UsedPercent != 23.5 {
		t.Fatalf("quota: %+v", q)
	}
	if Status() != StatusOK {
		t.Fatalf("status: %s", Status())
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); string(after) != orig {
		t.Fatalf("not restored:\n%s", after)
	}
	if _, _, ok := Read(); ok {
		t.Fatal("quota file left behind")
	}
}

func TestAStatusLineTheUserReplacedIsLeftAlone(t *testing.T) {
	path := setup(t, userSettings)
	if err := Install(exe); err != nil {
		t.Fatal(err)
	}
	theirs := `{"statusLine": {"type": "command", "command": "my-own"}}`
	os.WriteFile(path, []byte(theirs), 0o600)
	if Status() != StatusReplaced {
		t.Fatalf("status: %s", Status())
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != theirs {
		t.Fatalf("their file changed:\n%s", b)
	}
}

func TestUnreadableSettingsAreNotTouched(t *testing.T) {
	path := setup(t, "[1, 2]")
	if err := Install(exe); err == nil {
		t.Fatal("installed into a settings file that is not an object")
	}
	if Status() != StatusUnreadable {
		t.Fatalf("status: %s", Status())
	}
	if b, _ := os.ReadFile(path); string(b) != "[1, 2]" {
		t.Fatalf("file changed: %s", b)
	}
}

func TestMissingSettingsFileIsCreatedAndRemovedAgain(t *testing.T) {
	path := setup(t, "")
	if err := Install(exe); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "statusLine") {
		t.Fatalf("left: %s", b)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestMergeAndSummary(t *testing.T) {
	setup(t, "")
	now := time.Now()
	reset5 := now.Add(2 * time.Hour).Unix()
	reset7 := now.Add(72 * time.Hour).Unix()
	feed := func(js string) string {
		var out bytes.Buffer
		RunHelper(strings.NewReader(js), &out)
		return out.String()
	}
	both := `{"rate_limits":{"five_hour":{"used_percentage":6,"resets_at":` + itoa(reset5) + `},"seven_day":{"used_percentage":86,"resets_at":` + itoa(reset7) + `}}}`
	if got := feed(both); got != "5h 6% · 7d 86%\n" {
		t.Fatalf("summary %q", got)
	}
	// same window, a session that saw less: the higher value stays
	feed(`{"rate_limits":{"five_hour":{"used_percentage":3,"resets_at":` + itoa(reset5+30) + `}}}`)
	// a window missing from the input is kept
	if got := feed(`{"rate_limits":{}}`); got != "5h 6% · 7d 86%\n" {
		t.Fatalf("after a lower value: %q", got)
	}
	// a new window (later reset) replaces the old, lower or not
	feed(`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":` + itoa(reset5+18000) + `}}}`)
	if got := feed(`{}`); got != "5h 1% · 7d 86%\n" {
		t.Fatalf("after a new window: %q", got)
	}
	// input that isn't JSON still answers, never fails Claude Code
	if code := RunHelper(strings.NewReader("garbage"), &bytes.Buffer{}); code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestCommandQuotesThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	if got := Command("/Users/o'neil/code-speed"); got != `'/Users/o'\''neil/code-speed' statusline` {
		t.Fatalf("command %s", got)
	}
}
