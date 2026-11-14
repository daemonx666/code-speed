//go:build !darwin && !nogui

package gui

import "unsafe"

// makeClear is only needed on macOS; elsewhere BackgroundTypeTransparent
// does the job.
func makeClear(unsafe.Pointer) {}
