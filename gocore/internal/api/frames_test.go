package api

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// blobValue is a decoded blob: its type and values.
type blobValue struct {
	kind   string
	values []float64
	raw    []byte
}

func decodeBlob(t *testing.T, kind string, raw []byte) blobValue {
	t.Helper()
	size := map[string]int{"f32": 4, "f64": 8, "u32": 4, "u16": 2, "u8": 1}[kind]
	if size == 0 || len(raw)%size != 0 {
		t.Fatalf("blob %s of %d bytes", kind, len(raw))
	}
	values := make([]float64, len(raw)/size)
	for i := range values {
		chunk := raw[i*size:]
		switch kind {
		case "f32":
			values[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(chunk)))
		case "f64":
			values[i] = math.Float64frombits(binary.LittleEndian.Uint64(chunk))
		case "u32":
			values[i] = float64(binary.LittleEndian.Uint32(chunk))
		case "u16":
			values[i] = float64(binary.LittleEndian.Uint16(chunk))
		case "u8":
			values[i] = float64(chunk[0])
		}
	}
	return blobValue{kind, values, raw}
}

// readFrame reads one reply (a JSON line or a binary frame) and returns it
// with placeholders replaced by blobValues.
func readFrame(t *testing.T, in *bufio.Reader, start int) (map[string]any, int, bool) {
	t.Helper()
	line, err := in.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	read := len(line)
	var reply map[string]any
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatalf("reply %q: %v", line, err)
	}
	specs, isFrame := reply["blobs"].([]any)
	if !isFrame {
		return reply, read, false
	}
	if read%8 != 0 {
		t.Fatalf("frame line at %d has %d bytes, not a multiple of 8", start, read)
	}
	delete(reply, "blobs")
	blobs := make([]blobValue, len(specs))
	for i, spec := range specs {
		pair := spec.([]any)
		length := int(pair[1].(float64))
		raw := make([]byte, length+padding(length))
		if _, err := io.ReadFull(in, raw); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw[length:], make([]byte, padding(length))) {
			t.Fatal("blob padding is not zero")
		}
		read += len(raw)
		blobs[i] = decodeBlob(t, pair[0].(string), raw[:length])
	}
	used := make([]bool, len(blobs))
	var attach func(value any) any
	attach = func(value any) any {
		switch v := value.(type) {
		case []any:
			for i := range v {
				v[i] = attach(v[i])
			}
		case map[string]any:
			if index, ok := v["$blob"]; ok {
				if len(v) != 1 {
					t.Fatalf("placeholder %v", v)
				}
				used[int(index.(float64))] = true
				return blobs[int(index.(float64))]
			}
			for key := range v {
				v[key] = attach(v[key])
			}
		}
		return value
	}
	reply["result"] = attach(reply["result"])
	for i, u := range used {
		if !u {
			t.Fatalf("blob %d is not referenced", i)
		}
	}
	return reply, read, true
}

// sameData compares a binary reply with the JSON reply of the same request:
// float32 blobs equal the JSON numbers rounded to float32, everything else is
// identical; a "png" blob is the JSON "dataUri"'s bytes.
func sameData(t *testing.T, path string, binary, plain any) {
	t.Helper()
	switch b := binary.(type) {
	case blobValue:
		list, ok := plain.([]any)
		if !ok || len(list) != len(b.values) {
			t.Fatalf("%s: blob of %d values vs %T", path, len(b.values), plain)
		}
		for i, item := range list {
			want := item.(float64)
			if b.kind == "f32" {
				want = float64(float32(want))
			}
			if b.values[i] != want {
				t.Fatalf("%s[%d]: %v vs %v", path, i, b.values[i], want)
			}
		}
	case map[string]any:
		p, ok := plain.(map[string]any)
		if !ok {
			t.Fatalf("%s: %T vs %T", path, binary, plain)
		}
		if png, isPNG := b["png"].(blobValue); isPNG {
			if png.kind != "u8" || p["dataUri"] != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(png.raw) {
				t.Fatalf("%s: png blob differs from dataUri", path)
			}
			b = map[string]any{"width": b["width"], "height": b["height"], "warning": b["warning"]}
			p = map[string]any{"width": p["width"], "height": p["height"], "warning": p["warning"]}
		}
		if len(b) != len(p) {
			t.Fatalf("%s: keys %d vs %d", path, len(b), len(p))
		}
		for key, value := range b {
			sameData(t, path+"."+key, value, p[key])
		}
	case []any:
		p, ok := plain.([]any)
		if !ok || len(p) != len(b) {
			t.Fatalf("%s: list vs %T", path, plain)
		}
		for i := range b {
			sameData(t, path+"["+string(rune('0'+i%10))+"]", b[i], p[i])
		}
	default:
		if !jsonEqual(t, binary, plain) {
			t.Fatalf("%s: %v vs %v", path, binary, plain)
		}
	}
}

func TestStdioBinaryFramesMatchJSONReplies(t *testing.T) {
	a, _ := stdioAPI(t)
	demo := demoAssets(t)
	entity := filepath.Join(demo, "Entities", "PreviewRobot", "PreviewRobot.owentity")
	clip := filepath.Join(demo, "Animations", "001_Wave.owanimclip")
	material := filepath.Join(repo(t), "tests", "material-assets", "Entities", "MaterialRobot", "MaterialRobot.owentity")
	asset := okMap(t, call(t, a, "load_entity", material))
	var texture string
	for id := range m(asset["textures"]) {
		texture = id
		break
	}
	if texture == "" {
		t.Fatal("no texture in the material fixture")
	}
	binaryRequest := func(id any, method string, args ...any) map[string]any {
		r := request(id, method, args...)
		r["binary"] = true
		return r
	}
	var input strings.Builder
	pairs := [][2]map[string]any{
		{request(5, "load_texture", texture), binaryRequest(6, "load_texture", texture)},
		{request(1, "load_entity", entity), binaryRequest(2, "load_entity", entity)},
		{request(3, "load_animation", clip), binaryRequest(4, "load_animation", clip)},
		{request(7, "ping"), binaryRequest(8, "ping")},
		{request(9, "load_entity", "missing.owentity"), binaryRequest(10, "load_entity", "missing.owentity")},
	}
	for _, pair := range pairs {
		for _, r := range pair {
			line, _ := json.Marshal(r)
			input.Write(line)
			input.WriteByte('\n')
		}
	}
	var output bytes.Buffer
	if err := a.ServeStdio(strings.NewReader(input.String()), &output); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(bytes.NewReader(output.Bytes()))
	offset := 0
	for i, pair := range pairs {
		plain, n, isFrame := readFrame(t, reader, offset)
		offset += n
		if isFrame {
			t.Fatalf("%s: JSON request got a frame", pair[0]["method"])
		}
		framed, n, isFrame := readFrame(t, reader, offset)
		offset += n
		if wantFrame := i < 3; isFrame != wantFrame {
			t.Fatalf("%s: frame %v, want %v", pair[1]["method"], isFrame, wantFrame)
		}
		if !jsonEqual(t, framed["id"], pair[1]["id"]) {
			t.Fatalf("id %v", framed["id"])
		}
		sameData(t, pair[0]["method"].(string), framed["result"], plain["result"])
	}
	if offset != output.Len() {
		t.Fatalf("read %d of %d bytes", offset, output.Len())
	}
	// Handle returns the same bytes as ServeStdio writes.
	line, _ := json.Marshal(binaryRequest(2, "load_entity", entity))
	frame := a.Handle(nil, line)
	if again := a.Handle(nil, line); !bytes.Equal(frame, again) || !bytes.Contains(output.Bytes(), frame) {
		t.Fatal("Handle and ServeStdio frames differ")
	}
}

func TestServerBinaryFrames(t *testing.T) {
	isolate(t)
	a := newAPI(t, Options{DataDir: tempDir(t)})
	server, err := NewServer(a, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer server.listener.Close()
	if !strings.Contains(server.bridge(), `"X-Avolition-Binary":"1"`) {
		t.Fatal("the bridge does not ask for binary frames")
	}
	clip := filepath.Join(demoAssets(t), "Animations", "002_Turn.owanimclip")
	post := func(binary bool) *httptest.ResponseRecorder {
		body, _ := json.Marshal([]any{clip})
		r := httptest.NewRequest(http.MethodPost, "/api/load_animation", bytes.NewReader(body))
		r.Header.Set("X-Preview-Token", server.token)
		r.Header.Set("Content-Length", itoa(len(body)))
		if binary {
			r.Header.Set("X-Avolition-Binary", "1")
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	plainResponse := post(false)
	if plainResponse.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type %q", plainResponse.Header().Get("Content-Type"))
	}
	var plain map[string]any
	if err := json.Unmarshal(plainResponse.Body.Bytes(), &plain); err != nil {
		t.Fatal(err)
	}
	framed := post(true)
	if framed.Header().Get("Content-Type") != FrameContentType || framed.Header().Get("Content-Length") != itoa(framed.Body.Len()) {
		t.Fatalf("frame headers %v: %s", framed.Header(), framed.Body.Bytes())
	}
	reply, n, isFrame := readFrame(t, bufio.NewReader(bytes.NewReader(framed.Body.Bytes())), 0)
	if !isFrame || n != framed.Body.Len() {
		t.Fatalf("frame %v, %d of %d bytes", isFrame, n, framed.Body.Len())
	}
	sameData(t, "load_animation", reply["result"], plain)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
