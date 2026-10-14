package formats

import (
	"errors"
	"io/fs"
	"runtime"
	"strings"
	"syscall"
	"unicode"
)

// FormatError is an unsupported, incomplete or inconsistent exported asset
// (Python's FormatError, a ValueError). Error() is exactly str(exception).
type FormatError struct{ Message string }

func (e *FormatError) Error() string { return e.Message }

func formatError(message string) error { return &FormatError{Message: message} }

// OSError mirrors Python's OSError family (FileNotFoundError, PermissionError,
// ...). Error() follows Python's text, e.g. "File not found: /x" or
// "[Errno 13] Permission denied: '/x'"; Unwrap gives the Go error, so
// errors.Is(err, fs.ErrNotExist) works.
type OSError struct {
	Message string
	Err     error
}

func (e *OSError) Error() string { return e.Message }
func (e *OSError) Unwrap() error { return e.Err }

// IsOSOrFormatError reports whether err is one Python code catches with
// `except (OSError, FormatError)`.
func IsOSOrFormatError(err error) bool {
	var formatErr *FormatError
	var osErr *OSError
	var pathErr *fs.PathError
	return errors.As(err, &formatErr) || errors.As(err, &osErr) || errors.As(err, &pathErr)
}

func notFound(path string) error {
	return &OSError{Message: "File not found: " + path, Err: fs.ErrNotExist}
}

// PythonOSError converts a Go file-system error into the message Python would
// show for the same failure ("[Errno 2] No such file or directory: '/x'" on
// POSIX, "[WinError 2] The system cannot find the file specified: 'C:\\x'"
// on Windows). Errors that are already *OSError or *FormatError are returned
// unchanged.
func PythonOSError(err error) error {
	if err == nil {
		return nil
	}
	var osErr *OSError
	var formatErr *FormatError
	if errors.As(err, &osErr) || errors.As(err, &formatErr) {
		return err
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		return &OSError{Message: err.Error(), Err: err}
	}
	var errno syscall.Errno
	if !errors.As(pathErr.Err, &errno) {
		return &OSError{Message: err.Error(), Err: err}
	}
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
	return &OSError{
		Message: "[" + label + " " + itoa(int64(errno)) + "] " + text + ": " + PythonRepr(pathErr.Path),
		Err:     err,
	}
}
