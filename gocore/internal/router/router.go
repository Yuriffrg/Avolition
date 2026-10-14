// Package router answers ported methods in Go and forwards the rest to the
// Python core, so the desktop host sees one backend with the same protocol.
package router

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"avolition/gocore/internal/protocol"
)

// Handler implements one ported method. Args are the raw JSON arguments.
type Handler func(args []json.RawMessage) (any, error)

// Backend is the Python core, started on first use.
type Backend interface {
	Start() (stdin io.WriteCloser, stdout io.Reader, err error)
	Wait() error
}

// forwardQueue bounds requests waiting for the Python core. The desktop host
// allows 128 pending calls, so this never blocks a well-behaved host, and
// native methods keep answering while Python works through a long call.
const forwardQueue = 256

type Router struct {
	native  map[string]Handler
	backend Backend
	out     *lineWriter

	startOnce sync.Once
	startErr  error
	forward   chan []byte
	pumped    chan error
	exited    chan error // Receives the pump result if Python ends during input.
}

func New(out io.Writer, backend Backend, native map[string]Handler) *Router {
	return &Router{native: native, backend: backend, out: &lineWriter{w: out}, exited: make(chan error, 1)}
}

// Serve processes requests until input ends, then lets the Python core finish
// its queued work and exit. A Python core that stops by itself ends Serve with
// an error, so the desktop host reports it exactly as it reported a failed
// core before.
func (r *Router) Serve(in io.Reader) error {
	// Lines are handled on the reading goroutine itself: a hand-off per line
	// costs a scheduler wake-up on every call. This goroutine only waits for
	// the end of input or an exit of the Python core, whichever comes first.
	readErr := make(chan error, 1)
	go func() {
		reader := bufio.NewReaderSize(in, 64*1024)
		for {
			line, err := readLine(reader)
			if len(line) > 0 || err == nil {
				r.handle(line)
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()
	select {
	case err := <-readErr:
		if !errors.Is(err, io.EOF) {
			return err
		}
		return r.close()
	case err := <-r.exited:
		if waitErr := r.backend.Wait(); waitErr != nil {
			return waitErr
		}
		if err != nil {
			return err
		}
		return errors.New("the Python core stopped unexpectedly")
	}
}

// readLine returns one line without its terminator. Oversized lines are read
// whole; Parse rejects them with app.py's message.
func readLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadBytes('\n')
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
	}
	return line, err
}

func (r *Router) handle(line []byte) {
	request, failure := protocol.Parse(line)
	if failure != nil {
		r.out.write(protocol.Error(failure.ID, failure.Message))
		return
	}
	if handler, ok := r.native[request.Method]; ok {
		r.out.write(respond(request, handler))
		return
	}
	if err := r.ensureStarted(); err != nil {
		r.out.write(protocol.Error(request.ID, err.Error()))
		return
	}
	// The validated original line is forwarded byte for byte.
	r.forward <- append(line, '\n')
}

func respond(request protocol.Request, handler Handler) (line []byte) {
	defer func() {
		if recovered := recover(); recovered != nil {
			line = protocol.Error(request.ID, fmt.Sprint(recovered))
		}
	}()
	data, err := handler(request.Args)
	if err != nil {
		return protocol.Error(request.ID, err.Error())
	}
	line, err = protocol.Success(request.ID, data)
	if err != nil {
		return protocol.Error(request.ID, err.Error())
	}
	return line
}

func (r *Router) ensureStarted() error {
	r.startOnce.Do(func() {
		stdin, stdout, err := r.backend.Start()
		if err != nil {
			r.startErr = err
			return
		}
		r.forward = make(chan []byte, forwardQueue)
		r.pumped = make(chan error, 1)
		go func() {
			for line := range r.forward {
				if _, err := stdin.Write(line); err != nil {
					break // The pump reports the exit; keep draining below.
				}
			}
			for range r.forward {
			}
			_ = stdin.Close()
		}()
		go func() {
			err := r.out.copyLines(stdout)
			r.pumped <- err
			r.exited <- err
		}()
	})
	return r.startErr
}

func (r *Router) close() error {
	if r.forward == nil {
		return nil
	}
	close(r.forward)
	pumpErr := <-r.pumped
	if err := r.backend.Wait(); err != nil {
		return err
	}
	return pumpErr
}

// lineWriter keeps each reply line whole on the shared output.
type lineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineWriter) write(line []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(line)
	if flusher, ok := l.w.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
}

// copyLines streams Python's replies in fixed-size chunks, holding the lock
// for the duration of one line. A 200 MB mesh reply is never buffered whole.
func (l *lineWriter) copyLines(src io.Reader) error {
	reader := bufio.NewReaderSize(src, 256*1024)
	locked := false
	defer func() {
		if locked {
			l.mu.Unlock()
		}
	}()
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			if !locked {
				l.mu.Lock()
				locked = true
			}
			if _, werr := l.w.Write(chunk); werr != nil {
				return werr
			}
			if chunk[len(chunk)-1] == '\n' {
				if flusher, ok := l.w.(interface{ Flush() error }); ok {
					_ = flusher.Flush()
				}
				l.mu.Unlock()
				locked = false
			}
		}
		switch {
		case err == nil, errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if locked { // Terminate a partial final line so output stays parseable.
				_, _ = l.w.Write([]byte{'\n'})
			}
			return nil
		default:
			return err
		}
	}
}
