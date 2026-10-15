package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unicode"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
	"avolition/gocore/internal/pypath"
)

// valueError is a Python ValueError raised by app.py itself.
type valueError struct{ message string }

func (e *valueError) Error() string { return e.message }

func newValueError(message string) error { return &valueError{message} }

// runtimeError is a Python RuntimeError.
type runtimeError struct{ message string }

func (e *runtimeError) Error() string { return e.message }

// typeError is a Python TypeError.
type typeError struct{ message string }

func (e *typeError) Error() string { return e.message }

// pathOf is Path(value)'s argument check: only str is accepted.
func pathOf(value any) (string, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	return "", &typeError{fmt.Sprintf("argument should be a str or an os.PathLike object where __fspath__ returns a str, not '%s'", pycompat.TypeName(value))}
}

// isAbsArg is `isinstance(value, str) and Path(value).is_absolute()`.
func isAbsArg(value any) bool {
	text, ok := value.(string)
	return ok && pycompat.IsAbs(text)
}

// pyInt reports an int value (Python bool is an int too).
func pyInt(value any) (int64, bool) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		if !pycompat.IsInt(v) {
			return 0, false
		}
		n, err := v.Int64()
		if err != nil {
			// Out of range: compare by sign only.
			if strings.HasPrefix(string(v), "-") {
				return -1 << 62, true
			}
			return 1 << 62, true
		}
		return n, true
	}
	return 0, false
}

func hasKey(dict *ordered.Map[any], name string) bool {
	_, ok := dict.Get(name)
	return ok
}

func cloneDict(dict *ordered.Map[any]) *ordered.Map[any] {
	return pycompat.Clone(dict).(*ordered.Map[any])
}

func dictOf(value any) (*ordered.Map[any], bool) {
	dict, ok := value.(*ordered.Map[any])
	return dict, ok && dict != nil
}

func get(dict *ordered.Map[any], name string) any {
	if dict == nil {
		return nil
	}
	value, _ := dict.Get(name)
	return value
}

// getOr is dict.get(name, fallback).
func getOr(dict *ordered.Map[any], name string, fallback any) any {
	if dict == nil {
		return fallback
	}
	if value, ok := dict.Get(name); ok {
		return value
	}
	return fallback
}

// keysSubset reports set(dict) <= allowed.
func keysSubset(dict *ordered.Map[any], allowed ...string) bool {
	for _, k := range dict.Keys() {
		found := false
		for _, a := range allowed {
			if k == a {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func pypathWithSuffix(path, suffix string) string { return pypath.WithSuffix(path, suffix) }

// pyOSError converts a Go file-system error to Python's OSError text.
func pyOSError(err error) error {
	if err == nil {
		return nil
	}
	var link *os.LinkError
	if errors.As(err, &link) {
		var errno syscall.Errno
		if errors.As(link.Err, &errno) {
			text := strings.TrimSpace(errno.Error())
			label := "Errno"
			if runtime.GOOS == "windows" {
				label = "WinError"
				text = strings.TrimSuffix(text, ".")
			}
			if text != "" {
				runes := []rune(text)
				runes[0] = unicode.ToUpper(runes[0])
				text = string(runes)
			}
			return &formats.OSError{Message: fmt.Sprintf("[%s %d] %s: %s -> %s", label, int(errno), text,
				formats.PythonRepr(link.Old), formats.PythonRepr(link.New)), Err: err}
		}
	}
	return formats.PythonOSError(err)
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// errorText is reply()'s `str(error) or type(error).__name__`.
func errorText(err error) string {
	if message := err.Error(); message != "" {
		return message
	}
	switch err.(type) {
	case *valueError, *pycompat.ValueError, *formats.FormatError:
		return "ValueError"
	case *typeError, *pycompat.TypeError:
		return "TypeError"
	case *runtimeError:
		return "RuntimeError"
	case *formats.OSError:
		return "OSError"
	}
	return "Exception"
}

// pyLower is str.lower().
func pyLower(s string) string {
	if !strings.ContainsRune(s, 'İ') {
		return strings.ToLower(s)
	}
	return strings.ReplaceAll(strings.ToLower(strings.ReplaceAll(s, "İ", "i̇")), "İ", "i̇")
}

// runes helpers for Python's code-point indexing.
func runeLen(s string) int { return len([]rune(s)) }

// utf16Len is len(s.encode('utf-16-le')) // 2.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// strOrEmpty is str(value or '').
func strOrEmpty(value any) string {
	if !pycompat.Truthy(value) {
		return ""
	}
	return pycompat.Str(value)
}

// relativeTo is str(Path(p).relative_to(base)) for p inside base.
func relativeTo(p, base string) string {
	parts := pycompat.Parts(p)
	baseParts := pycompat.Parts(base)
	if len(baseParts) > len(parts) {
		return pycompat.PathStr(p)
	}
	rest := parts[len(baseParts):]
	if len(rest) == 0 {
		return "."
	}
	sep := "/"
	if runtime.GOOS == "windows" {
		sep = `\`
	}
	return strings.Join(rest, sep)
}

// fbxOptionsDefault is DEFAULT_FBX_OPTIONS (export_bundle.fbx_options()).
func fbxOptionsDefault() *ordered.Map[any] {
	value := ordered.New[any]()
	value.Set("enabled", false)
	value.Set("upAxis", "Y")
	value.Set("includeMesh", true)
	value.Set("bakeTextures", true)
	value.Set("rootMotion", true)
	value.Set("c4dEnabled", true)
	value.Set("clipEnabled", false)
	return value
}

// fbxOptions is export_bundle.fbx_options(value).
func fbxOptions(value any) (*ordered.Map[any], error) {
	result := fbxOptionsDefault()
	if value == nil {
		return result, nil
	}
	dict, ok := dictOf(value)
	if !ok {
		return nil, newValueError("Invalid FBX export options.")
	}
	for _, name := range []string{"enabled", "includeMesh", "bakeTextures", "rootMotion", "c4dEnabled", "clipEnabled"} {
		if item, found := dict.Get(name); found {
			flag, isBool := item.(bool)
			if !isBool {
				return nil, newValueError("Invalid FBX option: " + name)
			}
			result.Set(name, flag)
		}
	}
	if axis, found := dict.Get("upAxis"); found {
		if axis != "Y" && axis != "Z" {
			return nil, newValueError("Choose Y or Z as the FBX up axis.")
		}
		result.Set("upAxis", axis)
	}
	return result, nil
}

func flag(dict *ordered.Map[any], name string) bool {
	value, _ := dict.Get(name).(bool)
	return value
}
