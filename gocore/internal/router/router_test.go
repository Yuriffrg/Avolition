package router

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePython answers each forwarded line with handle(line), in order, like
// app.py's serial loop. A nil reply makes it exit as if it crashed.
type fakePython struct {
	handle   func(line string) []byte
	started  int
	received []string
	done     chan struct{}
	startErr error
}

func (f *fakePython) Start() (io.WriteCloser, io.Reader, error) {
	f.started++
	if f.startErr != nil {
		return nil, nil, f.startErr
	}
	inRead, inWrite := io.Pipe()
	outRead, outWrite := io.Pipe()
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		defer outWrite.Close()
		scanner := bufio.NewScanner(inRead)
		scanner.Buffer(nil, 1<<20)
		for scanner.Scan() {
			f.received = append(f.received, scanner.Text())
			reply := f.handle(scanner.Text())
			if reply == nil {
				inRead.CloseWithError(errors.New("crashed"))
				return
			}
			_, _ = outWrite.Write(reply)
		}
	}()
	return inWrite, outRead, nil
}

func (f *fakePython) Wait() error { <-f.done; return nil }

func echo(line string) []byte {
	var request struct{ ID json.RawMessage }
	_ = json.Unmarshal([]byte(line), &request)
	return []byte(`{"id":` + string(request.ID) + `,"result":{"ok":true,"data":"python"}}` + "\n")
}

// syncBuffer lets the router's goroutines and the test share output safely.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type reply struct {
	ID     json.RawMessage
	Result struct {
		OK    bool
		Data  any
		Error string
	}
}

func serve(t *testing.T, python *fakePython, input string) (map[string]reply, error) {
	t.Helper()
	var out syncBuffer
	err := New(&out, python, Native()).Serve(strings.NewReader(input))
	replies := map[string]reply{}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var r reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unparseable reply line %q: %v", line, err)
		}
		replies[string(r.ID)] = r
	}
	return replies, err
}

func TestNativeAndForwardedRequests(t *testing.T) {
	python := &fakePython{handle: echo}
	input := strings.Join([]string{
		`{"id":1,"method":"ping","args":[]}`,
		`{"id":2,"method":"bootstrap"}`,
		`{"id":"three","method":"browse_root"}`,
		`{"id":4,"method":"scan_root","args":["C:\\Héro 日本語"]}`,
		`{"id":5,"method":"__class__"}`,
		`{"id":6,"method":"ping","args":["extra"]}`,
	}, "\n") + "\n"
	replies, err := serve(t, python, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 6 {
		t.Fatalf("got %d replies: %v", len(replies), replies)
	}
	if r := replies["1"]; !r.Result.OK || r.Result.Data != true {
		t.Errorf("ping: %+v", r)
	}
	for _, id := range []string{"2", "4"} {
		if r := replies[id]; !r.Result.OK || r.Result.Data != "python" {
			t.Errorf("id %s was not forwarded: %+v", id, r)
		}
	}
	if r := replies[`"three"`]; r.Result.OK || !strings.Contains(r.Result.Error, "desktop window") {
		t.Errorf("browse_root: %+v", r)
	}
	if r := replies["5"]; r.Result.OK || r.Result.Error != "Unknown API method" {
		t.Errorf("private method: %+v", r)
	}
	if r := replies["6"]; r.Result.OK {
		t.Errorf("extra argument accepted: %+v", r)
	}
	// Forwarded byte for byte, and only the methods Go does not handle.
	want := []string{`{"id":2,"method":"bootstrap"}`, `{"id":4,"method":"scan_root","args":["C:\\Héro 日本語"]}`}
	if strings.Join(python.received, "\n") != strings.Join(want, "\n") {
		t.Errorf("forwarded %q, want %q", python.received, want)
	}
}

func TestPythonStartsOnlyWhenNeeded(t *testing.T) {
	python := &fakePython{handle: echo}
	if _, err := serve(t, python, `{"id":1,"method":"ping"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if python.started != 0 {
		t.Fatalf("Python started %d times for a native-only session", python.started)
	}
}

func TestStartFailureIsReportedPerRequest(t *testing.T) {
	python := &fakePython{startErr: errors.New("the Python core was not found")}
	replies, err := serve(t, python, `{"id":1,"method":"bootstrap"}`+"\n"+`{"id":2,"method":"ping"}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if r := replies["1"]; r.Result.OK || r.Result.Error != "the Python core was not found" {
		t.Errorf("bootstrap: %+v", r)
	}
	if r := replies["2"]; !r.Result.OK {
		t.Errorf("ping after start failure: %+v", r)
	}
}

func TestLargeRepliesStayWholeWhileNativeRepliesInterleave(t *testing.T) {
	big := strings.Repeat("x", 3<<20) // Larger than the copy buffer.
	python := &fakePython{handle: func(line string) []byte {
		var request struct{ ID json.RawMessage }
		_ = json.Unmarshal([]byte(line), &request)
		return []byte(`{"id":` + string(request.ID) + `,"result":{"ok":true,"data":"` + big + `"}}` + "\n")
	}}
	var input strings.Builder
	for i := range 20 {
		input.WriteString(`{"id":` + itoa(100+i) + `,"method":"load_entity","args":[]}` + "\n")
		input.WriteString(`{"id":` + itoa(200+i) + `,"method":"ping"}` + "\n")
	}
	replies, err := serve(t, python, input.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(replies) != 40 {
		t.Fatalf("got %d whole replies, want 40", len(replies))
	}
	for i := range 20 {
		if r := replies[itoa(100+i)]; r.Result.Data != big {
			t.Fatalf("reply %d was damaged", 100+i)
		}
	}
}

func TestPythonCrashEndsTheSession(t *testing.T) {
	python := &fakePython{handle: func(string) []byte { return nil }}
	reader, writer := io.Pipe()
	defer writer.Close()
	result := make(chan error, 1)
	var out syncBuffer
	go func() { result <- New(&out, python, Native()).Serve(reader) }()
	_, _ = writer.Write([]byte(`{"id":1,"method":"bootstrap"}` + "\n"))
	// Input stays open: the router must notice the exit by itself, so the
	// desktop host reports a stopped core instead of waiting for timeouts.
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("router kept running after the Python core exited")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
