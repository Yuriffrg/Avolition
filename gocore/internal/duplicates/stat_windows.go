//go:build windows

package duplicates

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// fingerprint mirrors CPython's os.stat on Windows: st_dev is the volume
// serial number, st_ino the file index, st_mtime the last write time and
// st_ctime the creation time.
type fingerprint struct {
	dev, ino     uint64
	size         int64
	mtime, ctime int64
}

func stat(path string) (fingerprint, int64, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fingerprint{}, 0, err
	}
	const fileReadAttributes = 0x80
	handle, err := syscall.CreateFile(name, fileReadAttributes,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fingerprint{}, 0, pyOSError(&os.PathError{Op: "stat", Path: path, Err: err})
	}
	defer syscall.CloseHandle(handle)
	var data syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &data); err != nil {
		return fingerprint{}, 0, pyOSError(&os.PathError{Op: "stat", Path: path, Err: err})
	}
	size := int64(data.FileSizeHigh)<<32 | int64(data.FileSizeLow)
	return fingerprint{
		dev:   uint64(data.VolumeSerialNumber),
		ino:   uint64(data.FileIndexHigh)<<32 | uint64(data.FileIndexLow),
		size:  size,
		mtime: data.LastWriteTime.Nanoseconds(),
		ctime: data.CreationTime.Nanoseconds(),
	}, size, nil
}

// pathKey is os.path.normcase(os.path.abspath(path)).
func pathKey(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = filepath.Clean(path)
	}
	return strings.ToLower(strings.ReplaceAll(absolute, "/", `\`))
}
