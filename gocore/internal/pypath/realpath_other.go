//go:build !windows

package pypath

import (
	"os"
	"strings"
)

// realpath ports posixpath.realpath(path, strict=False) for an absolute,
// normalized path: symlinks are resolved component by component, ".." is
// applied to the resolved prefix, and unreadable or missing components are
// appended unresolved. A symlink loop leaves the remainder unresolved.
func realpath(path string) string {
	rest := strings.Split(strings.TrimPrefix(path, "/"), "/")
	result := "/"
	seen := map[string]*string{}
	return resolveParts(result, rest, seen)
}

func joinPosix(a, b string) string {
	if a == "/" {
		return "/" + b
	}
	return a + "/" + b
}

func parentPosix(p string) string {
	if p == "/" {
		return "/"
	}
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func resolveParts(result string, rest []string, seen map[string]*string) string {
	for i := 0; i < len(rest); i++ {
		name := rest[i]
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			result = parentPosix(result)
			continue
		}
		next := joinPosix(result, name)
		info, err := os.Lstat(next)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			result = next
			continue
		}
		if resolved, known := seen[next]; known {
			if resolved != nil {
				result = *resolved
				continue
			}
			// Symlink loop: return the remainder unresolved, as Python does.
			for _, part := range rest[i+1:] {
				if part != "" && part != "." {
					next = joinPosix(next, part)
				}
			}
			return next
		}
		seen[next] = nil
		target, err := os.Readlink(next)
		if err != nil {
			result = next
			continue
		}
		base := result
		if strings.HasPrefix(target, "/") {
			base = "/"
		}
		resolved := resolveParts(base, strings.Split(strings.TrimPrefix(target, "/"), "/"), seen)
		seen[next] = &resolved
		result = resolved
	}
	return result
}
