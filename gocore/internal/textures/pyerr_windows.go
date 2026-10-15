//go:build windows

package textures

import (
	"errors"
	"strconv"
	"strings"
	"syscall"
)

// Opening a directory with the C runtime fails with EACCES on Windows.
var errIsDir error = syscall.ERROR_ACCESS_DENIED

// errnoPrefix follows CPython on Windows: open() goes through the C runtime
// and reports "[Errno N]" (mapped from the Win32 error), while stat, rename
// and directory calls report "[WinError N] <FormatMessage text>".
func errnoPrefix(op string, err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	if op == "open" {
		switch errno {
		case syscall.ERROR_FILE_NOT_FOUND, syscall.ERROR_PATH_NOT_FOUND:
			return "[Errno 2] No such file or directory"
		case syscall.ERROR_ACCESS_DENIED, 32 /* ERROR_SHARING_VIOLATION */, 33 /* ERROR_LOCK_VIOLATION */ :
			return "[Errno 13] Permission denied"
		case syscall.ERROR_FILE_EXISTS, syscall.ERROR_ALREADY_EXISTS:
			return "[Errno 17] File exists"
		}
	}
	message := strings.TrimRight(errno.Error(), " .\r\n\t")
	return "[WinError " + strconv.Itoa(int(errno)) + "] " + message
}
