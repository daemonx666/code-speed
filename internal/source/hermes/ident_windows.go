//go:build windows

package hermes

import (
	"os"
	"syscall"
)

// fileID is a file's identity: volume serial number and file index.
type fileID struct{ vol, hi, lo uint32 }

func identity(path string, fi os.FileInfo) (fileID, bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fileID{}, false
	}
	// read, write and delete sharing: Hermes renames the live log
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return fileID{}, false
	}
	defer syscall.CloseHandle(h)
	var info syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(h, &info) != nil {
		return fileID{}, false
	}
	return fileID{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}, true
}
