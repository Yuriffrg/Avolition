//go:build linux

package duplicates

import "syscall"

func changeTime(sys *syscall.Stat_t) int64 { return sys.Ctim.Nano() }
