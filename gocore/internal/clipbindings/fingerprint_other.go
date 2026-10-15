//go:build !windows

package clipbindings

import (
	"os"
	"syscall"

	"avolition/gocore/internal/formats"
)

// fingerprint is (st_dev, st_ino, st_size, st_mtime_ns, st_ctime_ns) of the
// file path names (following symlinks), like os.stat in Python.
func fingerprint(path string) (stamp, error) {
	info, err := os.Stat(path)
	if err != nil {
		return stamp{}, formats.PythonOSError(err)
	}
	result := stamp{size: info.Size(), mtime: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		result.dev, result.ino = uint64(st.Dev), uint64(st.Ino)
		result.ctime = statCtime(st)
	}
	return result, nil
}
