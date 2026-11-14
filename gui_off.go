//go:build nogui

package main

// The terminal-only build: no webview, no cgo, cross-compiles anywhere.
func desktop() func() error { return nil }
