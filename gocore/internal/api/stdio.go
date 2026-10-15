package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"sync"

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
	return a.handleRequest(dst, request)
}

func (a *API) handleRequest(dst []byte, request protocol.Request) ([]byte, *formats.Blobs) {
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

// concurrent lists methods that only read shared state, so consecutive calls
// may run in parallel. The renderer requests several textures at once and the
// host matches replies by id, so they may finish in any order. Every other
// request first waits for these to finish, so it observes exactly the state
// that run_stdio's strictly serial order would give it.
var concurrent = map[string]bool{"load_texture": true}

// ServeStdio is run_stdio: one JSON request per line in, one reply per line
// (or binary frame) out. Requests run in arrival order, except that a run of
// consecutive load_texture calls is decoded in parallel. Long-running work
// runs in background jobs polled by status methods.
func (a *API) ServeStdio(in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 64*1024)
	writer := &replyWriter{w: bufio.NewWriterSize(out, 256*1024)}
	var inflight sync.WaitGroup
	slots := make(chan struct{}, max(2, runtime.NumCPU()))
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
			request, failure := protocol.Parse(line)
			if failure == nil && concurrent[request.Method] {
				slots <- struct{}{}
				inflight.Add(1)
				go func() {
					defer func() { <-slots; inflight.Done() }()
					reply, blobs := a.handleRequest(nil, request)
					writer.send(reply, blobs)
				}()
			} else {
				inflight.Wait()
				var blobs *formats.Blobs
				if failure != nil {
					buffer = AppendReply(buffer[:0], failure.ID, nil, errors.New(failure.Message))
				} else {
					buffer, blobs = a.handleRequest(buffer[:0], request)
				}
				writer.send(buffer, blobs)
				if cap(buffer) > 16<<20 {
					buffer = nil // do not pin a huge mesh reply's buffer
				}
			}
			if err := writer.failed(); err != nil {
				inflight.Wait()
				return err
			}
		}
		if readErr != nil {
			inflight.Wait()
			if err := writer.failed(); err != nil {
				return err
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// replyWriter writes whole replies (a line plus any blobs) one at a time and
// remembers the first write error.
type replyWriter struct {
	mu  sync.Mutex
	w   *bufio.Writer
	err error
}

func (r *replyWriter) send(reply []byte, blobs *formats.Blobs) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	if _, r.err = r.w.Write(reply); r.err != nil {
		return
	}
	if blobs != nil {
		if r.err = writeBlobs(r.w, blobs); r.err != nil {
			return
		}
	}
	r.err = r.w.Flush()
}

func (r *replyWriter) failed() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}
