//go:build !windows

package duplicates

import (
	"errors"
	"strconv"
	"syscall"
)

var errIsDir error = syscall.EISDIR

// errnoPrefix renders "[Errno N] <strerror>"; Go's messages are C's strerror
// texts in lower case.
func errnoPrefix(_ string, err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return ""
	}
	return "[Errno " + strconv.Itoa(int(errno)) + "] " + capitalize(errno.Error())
}
