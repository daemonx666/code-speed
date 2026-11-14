//go:build !windows

package source

// WSLHomes is only ever non-empty on Windows.
func WSLHomes() []string { return nil }
