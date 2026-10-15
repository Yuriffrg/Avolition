package textures

// Python formats OSError as "[Errno 2] No such file or directory: 'path'"
// (or "[WinError 2] ..." for Win32 API failures on Windows). The UI shows
// str(exception), so errors are rendered the same way. This file is shared
// verbatim by the ported packages that report file errors.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

type pyError struct {
	message string
	err     error
}

func (e *pyError) Error() string { return e.message }
func (e *pyError) Unwrap() error { return e.err }

// pyOSError renders file-system errors like Python's OSError.__str__.
func pyOSError(err error) error {
	if err == nil {
		return nil
	}
	var pathError *fs.PathError
	if errors.As(err, &pathError) {
		prefix := errnoPrefix(pathError.Op, pathError.Err)
		if prefix == "" {
			return err
		}
		return &pyError{prefix + ": " + pyRepr(pathError.Path), err}
	}
	var linkError *os.LinkError
	if errors.As(err, &linkError) {
		prefix := errnoPrefix(linkError.Op, linkError.Err)
		if prefix == "" {
			return err
		}
		return &pyError{prefix + ": " + pyRepr(linkError.Old) + " -> " + pyRepr(linkError.New), err}
	}
	return err
}

func capitalize(message string) string {
	r, size := utf8.DecodeRuneInString(message)
	if size == 0 {
		return message
	}
	return string(unicode.ToUpper(r)) + message[size:]
}

// pyRepr mirrors Python's repr(str).
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.IndexByte(s, '\'') >= 0 && strings.IndexByte(s, '"') < 0 {
		quote = '"'
	}
	var builder strings.Builder
	builder.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == rune(quote) || r == '\\':
			builder.WriteByte('\\')
			builder.WriteRune(r)
		case r == '\n':
			builder.WriteString(`\n`)
		case r == '\r':
			builder.WriteString(`\r`)
		case r == '\t':
			builder.WriteString(`\t`)
		case r == utf8.RuneError || !unicode.IsPrint(r):
			switch {
			case r < 0x100:
				fmt.Fprintf(&builder, `\x%02x`, r)
			case r < 0x10000:
				fmt.Fprintf(&builder, `\u%04x`, r)
			default:
				fmt.Fprintf(&builder, `\U%08x`, r)
			}
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte(quote)
	return builder.String()
}
