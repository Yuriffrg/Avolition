//go:build windows

package clipbindings

import (
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"

	"avolition/gocore/internal/formats"
)

// fingerprint mirrors os.stat on Windows: the volume serial number and file
// index identify the file (so a replacement with the same size and times is
// still detected), plus size, last write, creation and change time.
func fingerprint(path string) (stamp, error) {
	fail := func(err error) (stamp, error) {
		return stamp{}, formats.PythonOSError(&fs.PathError{Op: "stat", Path: path, Err: err})
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fail(err)
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return fail(err)
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fail(err)
	}
	result := stamp{
		dev:   uint64(info.VolumeSerialNumber),
		ino:   uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		size:  int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow),
		mtime: info.LastWriteTime.Nanoseconds(),
		ctime: info.CreationTime.Nanoseconds(),
	}
	var basic fileBasicInfo
	if windows.GetFileInformationByHandleEx(handle, windows.FileBasicInfo,
		(*byte)(unsafe.Pointer(&basic)), uint32(unsafe.Sizeof(basic))) == nil {
		result.change = basic.ChangeTime
	}
	return result, nil
}

// fileBasicInfo is FILE_BASIC_INFO.
type fileBasicInfo struct {
	CreationTime, LastAccessTime, LastWriteTime, ChangeTime int64
	FileAttributes                                          uint32
	_                                                       uint32
}
