package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/protocol"
	"avolition/gocore/internal/pycompat"
)

// Server is make_server's developer-only test host: it serves web/ with a
// same-origin, token-protected bridge on the loopback interface.
type Server struct {
	api      *API
	webRoot  string
	token    string
	listener net.Listener
	http     *http.Server
	allowed  map[string]bool
	done     chan struct{}
	once     sync.Once
}

// NewServer is make_server(api, port): it listens on 127.0.0.1:port (0 picks
// a free port).
func NewServer(a *API, webRoot string, port int) (*Server, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	s := &Server{api: a, webRoot: webRoot, token: base64.RawURLEncoding.EncodeToString(raw[:]),
		listener: listener, allowed: map[string]bool{}, done: make(chan struct{})}
	for _, name := range protocol.Methods {
		s.allowed[name] = true
	}
	s.http = &http.Server{Handler: s}
	a.SetServerShutdown(s.Shutdown)
	return s, nil
}

// Port is server.server_port.
func (s *Server) Port() int { return s.listener.Addr().(*net.TCPAddr).Port }

// Serve is serve_forever(): it returns after Shutdown.
func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-s.done
		return nil
	}
	return err
}

// Shutdown stops the server (close_app).
func (s *Server) Shutdown() {
	s.once.Do(func() {
		_ = s.http.Shutdown(context.Background())
		close(s.done)
	})
}

func (s *Server) bridge() string {
	methods, _ := pycompat.Dumps(func() []any {
		out := make([]any, len(protocol.Methods))
		for i, m := range protocol.Methods {
			out[i] = m
		}
		return out
	}(), -1)
	token, _ := pycompat.Dumps(s.token, -1)
	// The bridge asks for binary frames (frames.go) and turns their blobs into
	// typed arrays viewing the response buffer, like the desktop host does.
	return `<script>window.pywebview={api:{}};(()=>{` +
		`const types={f32:Float32Array,f64:Float64Array,u32:Uint32Array,u16:Uint16Array,u8:Uint8Array};` +
		`const attach=(value,arrays)=>{if(Array.isArray(value)){for(let i=0;i<value.length;i++)if(value[i]&&typeof value[i]==="object")value[i]=attach(value[i],arrays);return value;}` +
		`if(Object.hasOwn(value,"$blob"))return arrays[value.$blob];for(const key in value)if(value[key]&&typeof value[key]==="object")value[key]=attach(value[key],arrays);return value;};` +
		`const frame=buffer=>{const bytes=new Uint8Array(buffer),end=bytes.indexOf(10),head=JSON.parse(new TextDecoder().decode(bytes.subarray(0,end)));let offset=end+1;` +
		`const arrays=head.blobs.map(([type,length])=>{const Type=types[type],array=new Type(buffer,offset,length/Type.BYTES_PER_ELEMENT);offset+=length+(8-length%8)%8;return array;});` +
		`return head.result&&typeof head.result==="object"?attach(head.result,arrays):head.result;};` +
		`for(const method of ` + string(methods) + `){window.pywebview.api[method]=(...args)=>fetch("/api/"+method,{method:"POST",headers:{"Content-Type":"application/json","X-Preview-Token":` + string(token) + `,"X-Avolition-Binary":"1"},body:JSON.stringify(args)})` +
		`.then(r=>r.headers.get("Content-Type")===` + strconv.Quote(FrameContentType) + `?r.arrayBuffer().then(frame):r.json());}` +
		`})();</script>`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		path := r.URL.Path
		if path == "/" || path == "/index.html" {
			content, err := os.ReadFile(filepath.Join(s.webRoot, "index.html"))
			if err != nil {
				http.Error(w, "File not found", http.StatusNotFound)
				return
			}
			payload := []byte(strings.ReplaceAll(string(content), "<head>", "<head>"+s.bridge()))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write(payload)
			}
			return
		}
		http.FileServer(http.Dir(s.webRoot)).ServeHTTP(w, r)
	case http.MethodPost:
		s.post(w, r)
	default:
		http.Error(w, "Unsupported method", http.StatusNotImplemented)
	}
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	path := r.URL.RequestURI()
	method := strings.TrimPrefix(path, "/api/")
	if !strings.HasPrefix(path, "/api/") || !s.allowed[method] || r.Header.Get("X-Preview-Token") != s.token {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	var line []byte
	data, err := func() (any, error) {
		text := r.Header.Get("Content-Length")
		if text == "" {
			text = "0"
		}
		length, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			return nil, fmt.Errorf("invalid literal for int() with base 10: %s", pycompat.Repr(text))
		}
		if length < 0 || length > 100000 {
			return nil, newValueError("Request too large")
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r.Body, body); err != nil {
			return nil, err
		}
		text, err = pycompat.DecodeUTF8Strict(body)
		if err != nil {
			return nil, err
		}
		decoded, err := pycompat.LoadsText(text)
		if err != nil {
			return nil, err
		}
		args, ok := decoded.([]any)
		if !ok {
			return nil, newValueError("Expected arguments")
		}
		return s.api.Call(method, args)
	}()
	if r.Header.Get("X-Avolition-Binary") == "1" {
		// The bridge script accepts binary frames (frames.go).
		const prefix = `{"result":`
		var blobs *formats.Blobs
		line, blobs = appendResultFrame(append(line, prefix...), data, err)
		if blobs != nil {
			line = appendFrameTail(line, 0, blobs)
			w.Header().Set("Content-Type", FrameContentType)
			w.Header().Set("Content-Length", strconv.Itoa(len(line)+frameSize(blobs)))
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write(line); err == nil {
				_ = writeBlobs(w, blobs)
			}
			return
		}
		line = line[len(prefix):]
	} else {
		line = AppendResult(line, data, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(line)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(line)
}

// FindWebRoot locates web/index.html: AVOLITION_WEB_ROOT, then web/ beside
// the executable or in up to four parent folders (a source checkout).
func FindWebRoot() (string, error) {
	if root := os.Getenv("AVOLITION_WEB_ROOT"); root != "" {
		return root, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	folder := filepath.Dir(executable)
	for range 5 {
		if info, err := os.Stat(filepath.Join(folder, "web", "index.html")); err == nil && !info.IsDir() {
			return filepath.Join(folder, "web"), nil
		}
		parent := filepath.Dir(folder)
		if parent == folder {
			break
		}
		folder = parent
	}
	return "", errors.New("the web/ folder was not found beside the Go core")
}

// SelfTest is --self-test REPORT: the bootstrap reply and, with a root, the
// scan reply, written as json.dumps(checks, indent=2).
func SelfTest(a *API, root, report string) error {
	checks := map[string]json.RawMessage{}
	ok := true
	data, err := a.Call("bootstrap", nil)
	checks["bootstrap"] = AppendResult(nil, data, err)
	order := []string{"ok", "bootstrap"}
	if root != "" {
		data, err := a.Call("scan_root", []any{root})
		checks["scan"] = AppendResult(nil, data, err)
		ok = err == nil
		order = append(order, "scan")
	}
	checks["ok"], _ = json.Marshal(ok)
	var out strings.Builder
	out.WriteString("{")
	for i, name := range order {
		if i > 0 {
			out.WriteString(",")
		}
		out.WriteString("\n  " + strconv.Quote(name) + ": ")
		value, err := pycompat.LoadsText(string(checks[name]))
		if err != nil {
			return err
		}
		encoded, err := dumpsASCIIIndented(value, 1)
		if err != nil {
			return err
		}
		out.WriteString(encoded)
	}
	out.WriteString("\n}")
	return writeText(report, out.String(), false)
}

// dumpsASCIIIndented is json.dumps(value, indent=2) (ensure_ascii=True) for a
// value nested level levels deep.
func dumpsASCIIIndented(value any, level int) (string, error) {
	encoded, err := pycompat.Dumps(value, 2)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range string(encoded) {
		switch {
		case r == '\n':
			b.WriteString("\n" + strings.Repeat("  ", level))
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		case r > 0x7F:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), nil
}
