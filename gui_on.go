//go:build !nogui

package main

import "github.com/daemonx666/code-speed/internal/gui"

// The desktop build: links the system webview (cgo on macOS and Linux).
func desktop() func() error { return func() error { return gui.Run(version) } }
