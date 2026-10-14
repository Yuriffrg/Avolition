//go:build windows

package pypath

import "path/filepath"

// realpath approximates ntpath.realpath(strict=False): resolve the longest
// existing prefix through filepath.EvalSymlinks and keep the missing tail.
func realpath(path string) string {
	cleaned := filepath.Clean(path)
	tail := ""
	current := cleaned
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			if tail == "" {
				return resolved
			}
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cleaned
		}
		if tail == "" {
			tail = filepath.Base(current)
		} else {
			tail = filepath.Join(filepath.Base(current), tail)
		}
		current = parent
	}
}
