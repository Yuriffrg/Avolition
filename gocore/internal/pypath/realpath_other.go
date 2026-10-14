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
func realpath(path string) string { return realpathCached(path, nil) }

func realpathCached(path string, cache *ResolveCache) string {
	rest := strings.Split(strings.TrimPrefix(path, "/"), "/")
	result := "/"
	seen := map[string]*string{}
	return resolveParts(result, rest, seen, cache)
}

// linkInfo is the cached outcome of lstat (and readlink) for one path.
type linkInfo struct {
	symlink bool
	target  string
	err     error
}

func (c *ResolveCache) lstat(path string) linkInfo {
	if c != nil {
		if info, ok := c.links[path]; ok {
			return info
		}
	}
	var info linkInfo
	stat, err := os.Lstat(path)
	if err == nil && stat.Mode()&os.ModeSymlink != 0 {
		info.symlink = true
		info.target, info.err = os.Readlink(path)
	}
	if c != nil {
		if c.links == nil {
			c.links = map[string]linkInfo{}
		}
		c.links[path] = info
	}
	return info
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

func resolveParts(result string, rest []string, seen map[string]*string, cache *ResolveCache) string {
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
		info := cache.lstat(next)
		if !info.symlink {
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
		target := info.target
		if info.err != nil {
			result = next
			continue
		}
		base := result
		if strings.HasPrefix(target, "/") {
			base = "/"
		}
		resolved := resolveParts(base, strings.Split(strings.TrimPrefix(target, "/"), "/"), seen, cache)
		seen[next] = &resolved
		result = resolved
	}
	return result
}
