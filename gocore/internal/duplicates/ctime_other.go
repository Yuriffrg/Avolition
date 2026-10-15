//go:build !linux && !windows

package duplicates

import "syscall"

// Other Unix systems name the field differently; size, mtime and inode
// still detect replacement.
func changeTime(*syscall.Stat_t) int64 { return 0 }
