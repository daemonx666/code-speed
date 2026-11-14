package cli

import (
	"os"
	"syscall"
)

// The desktop build is linked as a GUI program (-H windowsgui) so that
// double-clicking it opens no console window; such a program starts with no
// standard streams, and `code-speed today` typed in a terminal printed
// nothing. When it is run with a command and its output goes nowhere, it
// borrows the console of the terminal it was started from. Output that is
// redirected (Claude Code's status line reads it through a pipe) already
// has somewhere to go and is left alone. The terminal-only build is a
// console program and needs none of this. Ported from magpie
// (github.com/yetone/magpie, MIT, internal/cli/console_windows.go).
//
// A terminal doesn't wait for a GUI program, so its prompt comes back at
// once and the output lands after it; for `watch`, the terminal-only build
// is the better fit.
func init() {
	if len(os.Args) < 2 {
		return
	}
	if t, err := syscall.GetFileType(syscall.Stdout); err == nil && t != 0 {
		return // FILE_TYPE_UNKNOWN is 0: nothing attached
	}
	attach := syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := attach.Call(attachParentProcess); r == 0 {
		return
	}
	if out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
		os.Stdout, os.Stderr = out, out
	}
	if in, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
		os.Stdin = in
	}
}
