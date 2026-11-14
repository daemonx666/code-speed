//go:build !windows

package hermes

import (
	"os"
	"syscall"
)

// fileID is a file's identity: device and inode.
type fileID struct{ dev, ino uint64 }

func identity(path string, fi os.FileInfo) (fileID, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{uint64(st.Dev), uint64(st.Ino)}, true //nolint:unconvert
}
