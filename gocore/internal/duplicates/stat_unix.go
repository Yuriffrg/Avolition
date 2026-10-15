//go:build !windows

package duplicates

import (
	"os"
	"path/filepath"
	"syscall"
)

// fingerprint is Python's (st_dev, st_ino, st_size, st_mtime_ns,
// st_ctime_ns): ctime/inode also catch replacements which preserve size and
// mtime.
type fingerprint struct {
	dev, ino     uint64
	size         int64
	mtime, ctime int64
}

func stat(path string) (fingerprint, int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fingerprint{}, 0, pyOSError(err)
	}
	result := fingerprint{size: info.Size(), mtime: info.ModTime().UnixNano()}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		result.dev, result.ino = uint64(sys.Dev), uint64(sys.Ino)
		result.ctime = changeTime(sys)
	}
	return result, info.Size(), nil
}

// pathKey is os.path.normcase(os.path.abspath(path)).
func pathKey(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return absolute
}
