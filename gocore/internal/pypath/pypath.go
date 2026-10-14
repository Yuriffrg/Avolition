// Package pypath reproduces the parts of Python's pathlib that the backend
// relies on, so ported code builds, names and resolves paths exactly like the
// Python reference:
//
//   - Join keeps ".." components (pathlib never collapses them lexically; the
//     operating system resolves them against symlinks when the path is used).
//     filepath.Join would Clean them away, which can select a different file.
//   - Name, Stem, Suffix and Parent follow PurePath's rules ("a." has no
//     suffix, ".a" is a stem, "" and "." have an empty name).
//   - Resolve is Path.expanduser().resolve(): absolute, symlinks resolved,
//     non-existent tails kept as written (non-strict os.path.realpath).
//
// Strings are in the host's native form, like str(Path) in Python.
package pypath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const sep = string(filepath.Separator)

func isSep(c byte) bool {
	return c == '/' || (runtime.GOOS == "windows" && c == '\\')
}

// split returns the anchor (volume plus root separator, if any) and the
// normalized components of p: empty and "." components are dropped.
func split(p string) (anchor string, parts []string) {
	volume := filepath.VolumeName(p)
	rest := p[len(volume):]
	anchor = volume
	if rest != "" && isSep(rest[0]) {
		anchor += sep
	}
	start := 0
	for i := 0; i <= len(rest); i++ {
		if i == len(rest) || isSep(rest[i]) {
			if part := rest[start:i]; part != "" && part != "." {
				parts = append(parts, part)
			}
			start = i + 1
		}
	}
	return anchor, parts
}

func build(anchor string, parts []string) string {
	if anchor == "" && len(parts) == 0 {
		return "."
	}
	return anchor + strings.Join(parts, sep)
}

// Clean normalizes p like str(PurePath(p)): repeated separators and "."
// components are removed, ".." components are kept.
func Clean(p string) string {
	anchor, parts := split(p)
	return build(anchor, parts)
}

// IsAbs reports whether p is absolute in pathlib's sense.
func IsAbs(p string) bool { return filepath.IsAbs(p) }

// Join is PurePath(base) / ref for each ref: an absolute ref replaces the
// result, and on Windows a rooted ref without a drive keeps base's drive.
func Join(base string, refs ...string) string {
	result := base
	for _, ref := range refs {
		switch {
		case ref == "":
		case filepath.IsAbs(ref):
			result = ref
		case runtime.GOOS == "windows" && isSep(ref[0]):
			result = filepath.VolumeName(result) + ref
		case runtime.GOOS == "windows" && filepath.VolumeName(ref) != "":
			result = ref
		default:
			if result == "" || isSep(result[len(result)-1]) {
				result += ref
			} else {
				result += sep + ref
			}
		}
	}
	return Clean(result)
}

// Name is PurePath(p).name: the final component, or "" for an anchor or ".".
func Name(p string) string {
	_, parts := split(p)
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func suffixIndex(name string) int {
	i := strings.LastIndexByte(name, '.')
	if 0 < i && i < len(name)-1 {
		return i
	}
	return -1
}

// Suffix is PurePath(p).suffix, e.g. ".owmdl"; "" when there is none.
func Suffix(p string) string {
	name := Name(p)
	if i := suffixIndex(name); i >= 0 {
		return name[i:]
	}
	return ""
}

// Stem is PurePath(p).stem: the name without its final suffix.
func Stem(p string) string {
	name := Name(p)
	if i := suffixIndex(name); i >= 0 {
		return name[:i]
	}
	return name
}

// WithSuffix is PurePath(p).with_suffix(suffix) for a non-empty name.
func WithSuffix(p, suffix string) string {
	anchor, parts := split(p)
	if len(parts) == 0 {
		return build(anchor, parts)
	}
	last := parts[len(parts)-1]
	if i := suffixIndex(last); i >= 0 {
		last = last[:i]
	}
	parts[len(parts)-1] = last + suffix
	return build(anchor, parts)
}

// Parent is PurePath(p).parent (lexical; ".." is a normal component).
func Parent(p string) string {
	anchor, parts := split(p)
	if len(parts) == 0 {
		return build(anchor, parts)
	}
	return build(anchor, parts[:len(parts)-1])
}

// Parents is list(PurePath(p).parents): nearest first, ending at the anchor
// (or "." for a relative path).
func Parents(p string) []string {
	anchor, parts := split(p)
	result := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		result = append(result, build(anchor, parts[:i]))
	}
	return result
}

// ExpandUser is Path.expanduser() for "~" and "~/..." (the forms used here).
func ExpandUser(p string) string {
	if p == "~" || (len(p) > 1 && p[0] == '~' && isSep(p[1])) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home + p[1:]
		}
	}
	return p
}

// Absolute is Path.absolute(): p joined to the working directory if relative.
func Absolute(p string) string {
	if filepath.IsAbs(p) {
		return Clean(p)
	}
	wd, err := os.Getwd()
	if err != nil {
		return Clean(p)
	}
	return Join(wd, p)
}

// Resolve is Path(p).expanduser().resolve().
func Resolve(p string) string {
	return realpath(Absolute(ExpandUser(p)))
}

// IsFile is Path.is_file(): a regular file after following symlinks; any
// error (missing, permission, invalid name) reports false.
func IsFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// IsDir is Path.is_dir().
func IsDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// IsRelativeTo is PurePath(p).is_relative_to(base) (lexical, component-wise).
func IsRelativeTo(p, base string) bool {
	pa, pp := split(p)
	ba, bp := split(base)
	if !sameComponent(pa, ba) || len(bp) > len(pp) {
		return false
	}
	for i := range bp {
		if !sameComponent(bp[i], pp[i]) {
			return false
		}
	}
	return true
}

func sameComponent(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(strings.ReplaceAll(a, "/", `\`), strings.ReplaceAll(b, "/", `\`))
	}
	return a == b
}
