//go:build !darwin && !linux && !windows

package autostart

import "errors"

var errUnsupported = errors.New("starting at login is not supported on this system")

func enable(string) error { return errUnsupported }
func disable() error      { return nil }
func enabled() bool       { return false }

func dropOld() {}

// HadOld is only ever true on macOS, the one system whose login item
// carried the old name.
func HadOld() bool { return false }
