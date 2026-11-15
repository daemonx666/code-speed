// Ported from magpie (internal/edit/bom_test.go), MIT License, (c) 2026 yetone.

package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// utf8BOM is what PowerShell's `Set-Content -Encoding UTF8` (5.1), Notepad
// and Claude Code channel switchers on Windows put in front of a file.
const utf8BOM = "\xef\xbb\xbf"

// claudeSettingsCRLF is a Windows ~/.claude/settings.json as such a tool
// leaves it: a BOM, CRLF line ends, two-space indent, the user's env (#1534).
const claudeSettingsCRLF = utf8BOM + "{\r\n" +
	"  \"env\": {\r\n" +
	"    \"ANTHROPIC_BASE_URL\": \"https://api.deepseek.com/anthropic\",\r\n" +
	"    \"ANTHROPIC_AUTH_TOKEN\": \"sk-user-own\",\r\n" +
	"    \"API_TIMEOUT_MS\": \"600000\"\r\n" +
	"  },\r\n" +
	"  \"permissions\": {\r\n" +
	"    \"allow\": [\r\n" +
	"      \"Bash(git status)\"\r\n" +
	"    ]\r\n" +
	"  },\r\n" +
	"  \"model\": \"deepseek-chat\",\r\n" +
	"  \"alwaysThinkingEnabled\": true\r\n" +
	"}\r\n"

// keptAsWritten fails unless the file at p starts with exactly one BOM,
// has only CRLF line ends, and holds every one of want.
func keptAsWritten(t *testing.T, p string, want ...string) string {
	t.Helper()
	got := read(t, p)
	if !strings.HasPrefix(got, utf8BOM) || strings.HasPrefix(got[len(utf8BOM):], utf8BOM) {
		t.Fatalf("the file's BOM was not kept once: %q", got)
	}
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Fatalf("the file's CRLF line ends were not kept: %q", got)
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Fatalf("%q missing from %q", w, got)
		}
	}
	return got
}

// The JSON layer reads through the BOM and writes the file back with it,
// the user's own keys and line ends as they were. On the old code every
// write here failed with "invalid JSON/JSONC" and every read found nothing.
func TestJSONSettingsWithBOM(t *testing.T) {
	p := tmpFile(t, "settings.json", claudeSettingsCRLF)
	if v, ok := GetJSON(p, "model"); !ok || v != "deepseek-chat" {
		t.Fatalf("GetJSON through a BOM: %q %v", v, ok)
	}
	if err := SetJSON(p, KV{Path: "model", Value: "magpie/deepseek/deepseek-flash"}, KV{Path: "env.MAGPIE", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	keptAsWritten(t, p, `"model": "magpie/deepseek/deepseek-flash"`, `"MAGPIE": "1"`, `"ANTHROPIC_AUTH_TOKEN": "sk-user-own"`, `"Bash(git status)"`)
	if err := DelJSON(p, "env.MAGPIE"); err != nil {
		t.Fatal(err)
	}
	got := keptAsWritten(t, p, `"ANTHROPIC_BASE_URL"`)
	if strings.Contains(got, "MAGPIE") {
		t.Fatalf("DelJSON through a BOM left the key: %q", got)
	}
	if err := SetJSON(p, KV{Path: "model", Value: "deepseek-chat"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != claudeSettingsCRLF {
		t.Fatalf("set back, the file is not the user's own bytes:\n%q\nwant\n%q", got, claudeSettingsCRLF)
	}
}

// A file without a BOM gets none, and a file emptied stays empty.
func TestWriteAtomicAddsNoBOM(t *testing.T) {
	p := tmpFile(t, "settings.json", "{\"a\": 1}\n")
	if err := SetJSON(p, KV{Path: "a", Value: 2}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "{\"a\": 2}\n" {
		t.Fatalf("got %q", got)
	}
	q := tmpFile(t, "x.json", utf8BOM+"{}\n")
	if err := WriteAtomic(q, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, q); got != "" {
		t.Fatalf("an emptied file got %q", got)
	}
	n := filepath.Join(t.TempDir(), "new.json")
	if err := WriteAtomic(n, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(n); string(b) != "{}" {
		t.Fatalf("a new file got %q", b)
	}
}
