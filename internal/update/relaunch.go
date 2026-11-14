package update

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Relaunch starts this program again, with the arguments it was started
// with and env (NAME=value) added to its environment, once this process has
// quit: the copy running now holds the single instance lock, and a second
// copy started beside it would hand over to it and quit. With wait above
// zero, a process still running after wait is taken to be going on, and
// nothing is started; zero waits as long as it runs. The caller quits (or
// is about to exit) after it.
func Relaunch(wait time.Duration, env ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := relaunchCmd(runtime.GOOS, os.Getpid(), exe, os.Args[1:], wait)
	cmd.Env = append(os.Environ(), env...)
	return detached(cmd).Start()
}

func relaunchCmd(goos string, pid int, exe string, args []string, wait time.Duration) *exec.Cmd {
	if goos == "windows" {
		start := "Start-Process -FilePath " + psQuote(exe)
		if len(args) > 0 {
			q := make([]string, len(args))
			for i, a := range args {
				q[i] = psQuote(a)
			}
			start += " -ArgumentList " + strings.Join(q, ",")
		}
		script := fmt.Sprintf("Wait-Process -Id %d -ErrorAction SilentlyContinue; %s", pid, start)
		if wait > 0 {
			script = fmt.Sprintf("Wait-Process -Id %d -Timeout %d -ErrorAction SilentlyContinue; if (Get-Process -Id %d -ErrorAction SilentlyContinue) { exit }; %s",
				pid, max(1, int(wait/time.Second)), pid, start)
		}
		return powershell(script)
	}
	// polls every 0.2s; tries is how many polls before giving up (0: never)
	tries := int(wait / (200 * time.Millisecond))
	return exec.Command("/bin/sh", append([]string{"-c",
		`pid=$1; tries=$2; shift 2; n=0
while kill -0 "$pid" 2>/dev/null; do
	sleep 0.2; n=$((n+1))
	[ "$tries" -gt 0 ] && [ "$n" -ge "$tries" ] && exit 0
done
exec "$@" >/dev/null 2>&1`,
		"sh", strconv.Itoa(pid), strconv.Itoa(tries), exe}, args...)...)
}
