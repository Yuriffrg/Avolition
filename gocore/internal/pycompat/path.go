package pycompat

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// windowsPaths selects ntpath/PureWindowsPath semantics (the shipping target).
var windowsPaths = runtime.GOOS == "windows"

// pathParts is pathlib's parsed form: drive, root and the remaining parts.
type pathParts struct {
	drive, root string
	parts       []string
}

func sep() string {
	if windowsPaths {
		return `\`
	}
	return "/"
}

func parsePath(p string) pathParts {
	if windowsPaths {
		return parseWindows(p)
	}
	var result pathParts
	if strings.HasPrefix(p, "/") {
		result.root = "/"
		if strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") {
			result.root = "//"
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part != "" && part != "." {
			result.parts = append(result.parts, part)
		}
	}
	return result
}

func parseWindows(p string) pathParts {
	p = strings.ReplaceAll(p, "/", `\`)
	var result pathParts
	rest := p
	switch {
	case strings.HasPrefix(p, `\\`):
		// UNC: \\server\share (also \\?\UNC\server\share and \\?\C:).
		fields := strings.Split(p[2:], `\`)
		count := 2
		if len(fields) > 1 && (fields[0] == "?" || fields[0] == ".") && strings.EqualFold(fields[1], "UNC") {
			count = 4
		}
		if len(fields) > count {
			result.drive = `\\` + strings.Join(fields[:count], `\`)
			rest = strings.Join(fields[count:], `\`)
		} else {
			result.drive = `\\` + strings.Join(fields, `\`)
			rest = ""
		}
		result.root = `\`
	case len([]rune(p)) >= 2 && []rune(p)[1] == ':':
		runes := []rune(p)
		result.drive = string(runes[:2])
		rest = string(runes[2:])
		if strings.HasPrefix(rest, `\`) {
			result.root = `\`
		}
	case strings.HasPrefix(p, `\`):
		result.root = `\`
	}
	for _, part := range strings.Split(rest, `\`) {
		if part != "" && part != "." {
			result.parts = append(result.parts, part)
		}
	}
	return result
}

func (p pathParts) String() string {
	anchor := p.drive + p.root
	if anchor == "" && len(p.parts) == 0 {
		return "."
	}
	return anchor + strings.Join(p.parts, sep())
}

// PathStr is str(Path(p)).
func PathStr(p string) string { return parsePath(p).String() }

// IsAbs is Path(p).is_absolute().
func IsAbs(p string) bool {
	parsed := parsePath(p)
	if windowsPaths {
		return parsed.drive != "" && parsed.root != ""
	}
	return parsed.root != ""
}

// Join is Path(base).joinpath(*parts).
func Join(base string, parts ...string) string {
	result := parsePath(base)
	for _, part := range parts {
		next := parsePath(part)
		switch {
		case next.drive != "":
			result = next
		case next.root != "":
			result = pathParts{drive: result.drive, root: next.root, parts: next.parts}
		default:
			result.parts = append(append([]string(nil), result.parts...), next.parts...)
		}
	}
	return result.String()
}

// Parent is str(Path(p).parent).
func Parent(p string) string {
	parsed := parsePath(p)
	if len(parsed.parts) > 0 {
		parsed.parts = parsed.parts[:len(parsed.parts)-1]
	}
	return parsed.String()
}

// Name is Path(p).name.
func Name(p string) string {
	parsed := parsePath(p)
	if len(parsed.parts) == 0 {
		return ""
	}
	return parsed.parts[len(parsed.parts)-1]
}

// Parts is Path(p).parts.
func Parts(p string) []string {
	parsed := parsePath(p)
	var out []string
	if anchor := parsed.drive + parsed.root; anchor != "" {
		out = append(out, anchor)
	}
	return append(out, parsed.parts...)
}

// NormCase is os.path.normcase.
func NormCase(p string) string {
	if windowsPaths {
		return strings.ToLower(strings.ReplaceAll(p, "/", `\`))
	}
	return p
}

// IsRelativeTo is Path(p).is_relative_to(base) (lexical, like pathlib).
func IsRelativeTo(p, base string) bool {
	a, b := parsePath(p), parsePath(base)
	same := func(x, y string) bool {
		if windowsPaths {
			return strings.ToLower(x) == strings.ToLower(y)
		}
		return x == y
	}
	if !same(a.drive, b.drive) || a.root != b.root || len(b.parts) > len(a.parts) {
		return false
	}
	for i := range b.parts {
		if !same(a.parts[i], b.parts[i]) {
			return false
		}
	}
	return true
}

// RelativeTo is Path(p).relative_to(base) for a path known to be inside base,
// joined with forward slashes (PurePath.as_posix).
func RelativeTo(p, base string) (string, bool) {
	if !IsRelativeTo(p, base) {
		return "", false
	}
	a, b := parsePath(p), parsePath(base)
	return strings.Join(a.parts[len(b.parts):], "/"), true
}

// Exists, IsFile and IsDir follow Python 3.13: errors read as false.
func Exists(p string) bool { _, err := os.Stat(p); return err == nil }

func IsFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

func IsDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// ErrNoHome is Path.expanduser's RuntimeError.
var ErrNoHome = errors.New("Could not determine home directory.")

// ExpandUser is str(Path(p).expanduser()).
func ExpandUser(p string) (string, error) {
	parsed := parsePath(p)
	if parsed.drive != "" || parsed.root != "" || len(parsed.parts) == 0 || !strings.HasPrefix(parsed.parts[0], "~") {
		return parsed.String(), nil
	}
	var home string
	if windowsPaths {
		home = ntExpandUser(parsed.parts[0])
	} else {
		home = posixExpandUser(parsed.parts[0])
	}
	if strings.HasPrefix(home, "~") {
		return "", ErrNoHome
	}
	return Join(home, parsed.parts[1:]...), nil
}

func posixExpandUser(component string) string {
	var home string
	if component == "~" {
		if value, ok := os.LookupEnv("HOME"); ok {
			home = value
		} else {
			current, err := user.Current()
			if err != nil {
				return component
			}
			home = current.HomeDir
		}
	} else {
		account, err := user.Lookup(component[1:])
		if err != nil {
			return component
		}
		home = account.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if home == "" {
		return "/"
	}
	return home
}

func ntExpandUser(component string) string {
	var home string
	if value, ok := lookupEnv("USERPROFILE"); ok {
		home = value
	} else if path, ok := lookupEnv("HOMEPATH"); !ok {
		return component
	} else {
		drive, _ := lookupEnv("HOMEDRIVE")
		home = ntJoin(drive, path)
	}
	if component != "~" {
		target := component[1:]
		current, ok := lookupEnv("USERNAME")
		if !ok || target != current {
			if !ok || current != ntBase(home) {
				return component
			}
			home = ntJoin(ntDir(home), target)
		}
	}
	return home
}

func ntJoin(a, b string) string {
	if a == "" {
		return b
	}
	if strings.HasPrefix(b, `\`) || strings.HasPrefix(b, "/") || (len(b) > 1 && b[1] == ':') {
		if len(b) > 1 && b[1] == ':' {
			return b
		}
		return a + b
	}
	if strings.HasSuffix(a, `\`) || strings.HasSuffix(a, "/") || strings.HasSuffix(a, ":") {
		return a + b
	}
	return a + `\` + b
}

func ntSplit(p string) (string, string) {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		if len(p) > 1 && p[1] == ':' {
			return p[:2], p[2:]
		}
		return "", p
	}
	head := strings.TrimRight(p[:i+1], `\/`)
	if head == "" || (len(head) == 2 && head[1] == ':') {
		head = p[:i+1]
	}
	return head, p[i+1:]
}

func ntBase(p string) string { _, base := ntSplit(p); return base }
func ntDir(p string) string  { dir, _ := ntSplit(p); return dir }

// lookupEnv mirrors os.environ, which is case-insensitive on Windows.
func lookupEnv(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if !windowsPaths {
		return os.LookupEnv(name)
	}
	if value, ok := os.LookupEnv(name); ok {
		return value, true
	}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found && key != "" && strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

// ExpandVars is os.path.expandvars.
func ExpandVars(p string) string {
	if windowsPaths {
		return ntExpandVars(p)
	}
	return posixExpandVars(p)
}

var posixVar = regexp.MustCompile(`\$([A-Za-z0-9_]+|\{[^}]*\})`)

func posixExpandVars(path string) string {
	if !strings.Contains(path, "$") {
		return path
	}
	i := 0
	for {
		loc := posixVar.FindStringSubmatchIndex(path[i:])
		if loc == nil {
			break
		}
		start, end := i+loc[0], i+loc[1]
		name := path[i+loc[2] : i+loc[3]]
		if strings.HasPrefix(name, "{") && strings.HasSuffix(name, "}") {
			name = name[1 : len(name)-1]
		}
		value, ok := lookupEnv(name)
		if !ok {
			i = end
			continue
		}
		tail := path[end:]
		path = path[:start] + value
		i = len(path)
		path += tail
	}
	return path
}

func isVarChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
}

func indexRune(runes []rune, target rune) int {
	for i, r := range runes {
		if r == target {
			return i
		}
	}
	return -1
}

// ntExpandVars ports ntpath.expandvars: %VAR%, ${VAR}, $VAR, ” quoting.
func ntExpandVars(text string) string {
	if !strings.ContainsAny(text, "$%") {
		return text
	}
	path := []rune(text)
	var res strings.Builder
	index := 0
	for index < len(path) {
		c := path[index]
		switch c {
		case '\'':
			path = path[index+1:]
			if found := indexRune(path, c); found >= 0 {
				index = found
				res.WriteRune(c)
				res.WriteString(string(path[:index+1]))
			} else {
				res.WriteRune(c)
				res.WriteString(string(path))
				index = len(path) - 1
			}
		case '%':
			if index+1 < len(path) && path[index+1] == '%' {
				res.WriteRune(c)
				index++
			} else {
				path = path[index+1:]
				if found := indexRune(path, '%'); found < 0 {
					res.WriteString("%" + string(path))
					index = len(path) - 1
				} else {
					index = found
					name := string(path[:index])
					if value, ok := lookupEnv(name); ok {
						res.WriteString(value)
					} else {
						res.WriteString("%" + name + "%")
					}
				}
			}
		case '$':
			switch {
			case index+1 < len(path) && path[index+1] == '$':
				res.WriteRune(c)
				index++
			case index+1 < len(path) && path[index+1] == '{':
				path = path[index+2:]
				if found := indexRune(path, '}'); found < 0 {
					res.WriteString("${" + string(path))
					index = len(path) - 1
				} else {
					index = found
					name := string(path[:index])
					if value, ok := lookupEnv(name); ok {
						res.WriteString(value)
					} else {
						res.WriteString("${" + name + "}")
					}
				}
			default:
				var name []rune
				index++
				for index < len(path) && isVarChar(path[index]) {
					name = append(name, path[index])
					index++
				}
				if value, ok := lookupEnv(string(name)); ok {
					res.WriteString(value)
				} else {
					res.WriteString("$" + string(name))
				}
				if index < len(path) {
					index--
				}
			}
		default:
			res.WriteRune(c)
		}
		index++
	}
	return res.String()
}

// Resolve is str(Path(p).resolve()) (non-strict).
func Resolve(p string) string {
	if windowsPaths {
		return resolveWindows(p)
	}
	return posixRealpath(PathStr(p))
}

func resolveWindows(p string) string {
	absolute, err := filepath.Abs(PathStr(p))
	if err != nil {
		absolute = filepath.Clean(PathStr(p))
	}
	// ntpath.realpath resolves the longest existing prefix and appends the
	// rest unchanged.
	head, tail := absolute, ""
	for {
		if resolved, err := filepath.EvalSymlinks(head); err == nil {
			if tail != "" {
				resolved = filepath.Join(resolved, tail)
			}
			return PathStr(resolved)
		}
		parent := filepath.Dir(head)
		if parent == head {
			return PathStr(absolute)
		}
		name := filepath.Base(head)
		if tail == "" {
			tail = name
		} else {
			tail = name + `\` + tail
		}
		head = parent
	}
}

type realpathItem struct {
	name   string
	marker bool // a resolved symlink target; the next item is the link path
}

// posixRealpath ports Python 3.13 posixpath._realpath (strict=False).
func posixRealpath(filename string) string {
	var rest []realpathItem
	split := strings.Split(filename, "/")
	for i := len(split) - 1; i >= 0; i-- {
		rest = append(rest, realpathItem{name: split[i]})
	}
	partCount := len(split)
	path := "/"
	if !strings.HasPrefix(filename, "/") {
		cwd, err := os.Getwd()
		if err != nil {
			cwd = "/"
		}
		path = cwd
	}
	seen := map[string]*string{}
	pop := func() realpathItem {
		item := rest[len(rest)-1]
		rest = rest[:len(rest)-1]
		return item
	}
	for partCount > 0 {
		item := pop()
		if item.marker {
			link := pop()
			resolved := path
			seen[link.name] = &resolved
			continue
		}
		partCount--
		name := item.name
		if name == "" || name == "." {
			continue
		}
		if name == ".." {
			if i := strings.LastIndex(path, "/"); i > 0 {
				path = path[:i]
			} else {
				path = "/"
			}
			continue
		}
		newpath := path + "/" + name
		if path == "/" {
			newpath = "/" + name
		}
		info, err := os.Lstat(newpath)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			path = newpath
			continue
		}
		if cached, ok := seen[newpath]; ok {
			if cached != nil {
				path = *cached
				continue
			}
			path = newpath // symlink loop: keep the unresolved path
			continue
		}
		target, err := os.Readlink(newpath)
		if err != nil {
			path = newpath
			continue
		}
		seen[newpath] = nil
		if strings.HasPrefix(target, "/") {
			path = "/"
		}
		rest = append(rest, realpathItem{name: newpath}, realpathItem{marker: true})
		parts := strings.Split(target, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			rest = append(rest, realpathItem{name: parts[i]})
		}
		partCount += len(parts)
	}
	return path
}
