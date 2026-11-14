package claudebridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/daemonx666/code-speed/internal/usage"
)

// file is the quota file: one value per window.
type file struct {
	Version    int               `json:"version"`
	ReceivedMs int64             `json:"receivedMs"`
	Windows    map[string]window `json:"windows"`
}

type window struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"` // Unix seconds
}

func readFile() (file, bool) {
	b, err := os.ReadFile(QuotaPath())
	if err != nil {
		return file{}, false
	}
	var f file
	if json.Unmarshal(b, &f) != nil || f.Version != 1 {
		return file{}, false
	}
	return f, true
}

// merge keeps one value per window: a later reset (by more than 60 s) is a
// new window; within 60 s it is the same window and the higher use wins.
// It reports whether anything changed.
func merge(f *file, in gjson.Result) bool {
	changed := false
	for _, name := range []string{"five_hour", "seven_day"} {
		w := in.Get(name)
		used, reset := w.Get("used_percentage"), w.Get("resets_at")
		if !w.IsObject() || used.Type != gjson.Number || reset.Type != gjson.Number {
			continue
		}
		nw := window{UsedPercentage: used.Num, ResetsAt: int64(reset.Num)}
		old, ok := f.Windows[name]
		switch {
		case !ok || nw.ResetsAt > old.ResetsAt+60:
		case nw.ResetsAt >= old.ResetsAt-60 && nw.UsedPercentage > old.UsedPercentage:
		default:
			continue
		}
		f.Windows[name] = nw
		changed = true
	}
	return changed
}

// RunHelper is `code-speed statusline`: Claude Code's status-line command.
// It keeps the limits, then runs the status line there was before (its
// output and exit code are ours), or prints the limits itself. It prints
// nothing else, logs nothing of its input, and never fails Claude Code.
func RunHelper(stdin io.Reader, stdout io.Writer) int {
	in, _ := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if rl := gjson.GetBytes(in, "rate_limits"); rl.IsObject() {
		f, ok := readFile()
		if !ok {
			f = file{Version: 1}
		}
		if f.Windows == nil {
			f.Windows = map[string]window{}
		}
		if merge(&f, rl) {
			f.ReceivedMs = time.Now().UnixMilli()
			if b, err := json.Marshal(f); err == nil {
				_ = writeFile(QuotaPath(), b, 0o600)
			}
		}
	}
	if prev := previous(); prev.Get("type").String() == "command" && prev.Get("command").String() != "" {
		return runPrevious(prev.Get("command").String(), in, stdout)
	}
	if line := summary(time.Now()); line != "" {
		fmt.Fprintln(stdout, line)
	}
	return 0
}

// summary is the stored windows that haven't reset: "5h 6% · 7d 86%".
func summary(now time.Time) string {
	f, ok := readFile()
	if !ok {
		return ""
	}
	var parts []string
	for _, w := range []struct{ key, label string }{{"five_hour", "5h"}, {"seven_day", "7d"}} {
		if v, ok := f.Windows[w.key]; ok && v.ResetsAt > now.Unix() {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", w.label, v.UsedPercentage))
		}
	}
	return strings.Join(parts, " · ")
}

// runPrevious runs the user's own status line with the same input,
// environment and folder, for at most 10 s.
func runPrevious(command string, in []byte, stdout io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, windowsShell(), "-c", command)
		if strings.HasSuffix(strings.ToLower(windowsShell()), "cmd.exe") {
			cmd = exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", command)
		}
	} else {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return 0
	}
	return 0
}

// windowsShell is the bash Claude Code uses on Windows, else cmd.exe.
func windowsShell() string {
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
		return p
	}
	if git, err := exec.LookPath("git.exe"); err == nil {
		bash := filepath.Join(filepath.Dir(git), "..", "bin", "bash.exe")
		if _, err := os.Stat(bash); err == nil {
			return bash
		}
	}
	return "cmd.exe"
}

// Read returns the limits the helper kept, as a quota, and the file's
// modification time so a reader can skip an unchanged file.
func Read() (usage.Quota, time.Time, bool) {
	fi, err := os.Stat(QuotaPath())
	if err != nil {
		return usage.Quota{}, time.Time{}, false
	}
	f, ok := readFile()
	if !ok {
		return usage.Quota{}, fi.ModTime(), false
	}
	q := usage.Quota{Source: usage.ClaudeCode, ObservedMs: f.ReceivedMs}
	for _, w := range []struct {
		key, kind string
		minutes   int64
	}{{"five_hour", "fiveHour", 300}, {"seven_day", "sevenDay", 10080}} {
		if v, ok := f.Windows[w.key]; ok {
			q.Windows = append(q.Windows, usage.QuotaWindow{Kind: w.kind, UsedPercent: v.UsedPercentage,
				WindowMinutes: w.minutes, ResetsAtMs: v.ResetsAt * 1000})
		}
	}
	return q, fi.ModTime(), len(q.Windows) > 0
}
