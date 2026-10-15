package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/protocol"
	"avolition/gocore/internal/pycompat"
)

// DecodeArgs decodes raw JSON arguments into Python-shaped values.
func DecodeArgs(raw []json.RawMessage) ([]any, error) {
	args := make([]any, len(raw))
	for i, item := range raw {
		value, err := pycompat.LoadsText(string(item))
		if err != nil {
			return nil, err
		}
		args[i] = value
	}
	return args, nil
}

// AppendData appends the JSON encoding of reply data.
func AppendData(dst []byte, data any) ([]byte, error) {
	if appender, ok := data.(Appender); ok {
		return appender.AppendJSON(dst)
	}
	return appendValue(dst, data)
}

// appendValue appends json.Marshal(data) without HTML escaping.
func appendValue(dst []byte, data any) ([]byte, error) {
	buffer := bytes.NewBuffer(dst)
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(data); err != nil {
		return nil, err
	}
	out := buffer.Bytes()
	return out[:len(out)-1], nil // drop Encode's newline
}

func appendString(dst []byte, s string) []byte {
	// json.Marshal would escape <, > and &; Python does not.
	out := bytes.NewBuffer(dst)
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	result := out.Bytes()
	return result[:len(result)-1]
}

// AppendResult appends the reply wrapper's result object:
// {"ok":true,"data":...} or {"ok":false,"error":"..."}.
func AppendResult(dst []byte, data any, err error) []byte {
	if err == nil {
		start := len(dst)
		dst = append(dst, `{"ok":true,"data":`...)
		encoded, encodeErr := AppendData(dst, data)
		if encodeErr == nil {
			return append(encoded, '}')
		}
		dst, err = dst[:start], encodeErr
	}
	dst = append(dst, `{"ok":false,"error":`...)
	dst = appendString(dst, errorText(err))
	return append(dst, '}')
}

// AppendReply appends one stdio reply line: {"id":id,"result":...}\n.
func AppendReply(dst []byte, id json.RawMessage, data any, err error) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	dst = append(dst, `{"id":`...)
	dst = append(dst, id...)
	dst = append(dst, `,"result":`...)
	dst = AppendResult(dst, data, err)
	return append(dst, "}\n"...)
}

// Handle runs one request line (without its terminator) and appends the
// reply line, like one iteration of run_stdio, or the whole binary frame when
// the request asked for one.
func (a *API) Handle(dst []byte, line []byte) []byte {
	dst, blobs := a.handle(dst, line)
	if blobs != nil {
		for _, blob := range blobs.Items {
			dst = append(dst, blob.Data...)
			dst = append(dst, zeros[:padding(len(blob.Data))]...)
		}
	}
	return dst
}

// handle appends the reply line, or a binary frame's JSON line and returns
// the blobs that follow it.
func (a *API) handle(dst []byte, line []byte) ([]byte, *formats.Blobs) {
	request, failure := protocol.Parse(line)
	if failure != nil {
		return AppendReply(dst, failure.ID, nil, errors.New(failure.Message)), nil
	}
	args, err := DecodeArgs(request.Args)
	if err != nil {
		return AppendReply(dst, request.ID, nil, err), nil
	}
	data, err := a.Call(request.Method, args)
	if !request.Binary {
		return AppendReply(dst, request.ID, data, err), nil
	}
	start := len(dst)
	dst = append(dst, `{"id":`...)
	dst = append(dst, request.ID...)
	dst = append(dst, `,"result":`...)
	dst, blobs := appendResultFrame(dst, data, err)
	if blobs == nil {
		return append(dst, "}\n"...), nil
	}
	return appendFrameTail(dst, start, blobs), blobs
}

// ServeStdio is run_stdio: one JSON request per line in, one reply per line
// out, strictly in order. Long-running work runs in background jobs polled
// by status methods, so serial handling never blocks on them.
func (a *API) ServeStdio(in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 64*1024)
	writer := bufio.NewWriterSize(out, 256*1024)
	var buffer []byte
	for {
		line, readErr := reader.ReadBytes('\n')
		if n := len(line); n > 0 && line[n-1] == '\n' {
			line = line[:n-1]
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
		}
		if len(line) > 0 || readErr == nil {
			var blobs *formats.Blobs
			buffer, blobs = a.handle(buffer[:0], line)
			if _, err := writer.Write(buffer); err != nil {
				return err
			}
			if blobs != nil {
				if err := writeBlobs(writer, blobs); err != nil {
					return err
				}
			}
			if err := writer.Flush(); err != nil {
				return err
			}
			if cap(buffer) > 16<<20 {
				buffer = nil // do not pin a huge mesh reply's buffer
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
