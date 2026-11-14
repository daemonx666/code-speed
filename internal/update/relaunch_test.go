package update

import (
	"strings"
	"testing"
	"time"
)

func TestRelaunchCmd(t *testing.T) {
	cmd := relaunchCmd("windows", 42, `C:\Program Files\it's\code-speed.exe`, nil, 0)
	script := cmd.Args[len(cmd.Args)-1]
	for _, want := range []string{"Wait-Process -Id 42 -ErrorAction", `Start-Process -FilePath 'C:\Program Files\it''s\code-speed.exe'`} {
		if !strings.Contains(script, want) {
			t.Errorf("windows script %q lacks %q", script, want)
		}
	}
	if strings.Contains(script, "-ArgumentList") || strings.Contains(script, "-Timeout") {
		t.Errorf("no arguments and no limit, but %q has them", script)
	}

	// a process still running after the wait is left to run, and nothing started
	cmd = relaunchCmd("windows", 42, `C:\x.exe`, []string{"a b"}, 15*time.Second)
	script = cmd.Args[len(cmd.Args)-1]
	for _, want := range []string{"-Timeout 15", "if (Get-Process -Id 42 -ErrorAction SilentlyContinue) { exit }", "-ArgumentList 'a b'"} {
		if !strings.Contains(script, want) {
			t.Errorf("windows script %q lacks %q", script, want)
		}
	}

	cmd = relaunchCmd("linux", 42, "/opt/code-speed", []string{"x"}, time.Second)
	if got := strings.Join(cmd.Args[3:], " "); got != "sh 42 5 /opt/code-speed x" {
		t.Errorf("unix args %q", got)
	}
}
