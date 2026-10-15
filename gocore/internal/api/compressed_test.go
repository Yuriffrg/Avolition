package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/bcenc"
)

// materialTexture loads the material fixture and returns one texture id.
func materialTexture(t *testing.T, a *API) string {
	t.Helper()
	material := filepath.Join(repo(t), "tests", "material-assets", "Entities", "MaterialRobot", "MaterialRobot.owentity")
	asset := okMap(t, call(t, a, "load_entity", material))
	for id := range m(asset["textures"]) {
		return id
	}
	t.Fatal("no texture in the material fixture")
	return ""
}

// checkCompressed verifies a compressed load_texture reply's mip chain.
func checkCompressed(t *testing.T, data map[string]any, format string) {
	t.Helper()
	if data["format"] != format || data["png"] != nil || data["dataUri"] != nil {
		t.Fatalf("reply %v", data)
	}
	width, height := int(data["width"].(float64)), int(data["height"].(float64))
	mips := data["mips"].([]any)
	if len(mips) == 0 {
		t.Fatal("no mips")
	}
	for i, item := range mips {
		mip := item.(map[string]any)
		w, h := int(mip["width"].(float64)), int(mip["height"].(float64))
		if w != max(1, width>>i) || h != max(1, height>>i) {
			t.Fatalf("mip %d is %d×%d of %d×%d", i, w, h, width, height)
		}
		blob := mip["data"].(blobValue)
		if blob.kind != "u8" || len(blob.raw) != bcenc.Size(format, w, h) {
			t.Fatalf("mip %d: %s blob of %d bytes", i, blob.kind, len(blob.raw))
		}
	}
	if last := mips[len(mips)-1].(map[string]any); last["width"].(float64) != 1 || last["height"].(float64) != 1 {
		t.Fatalf("chain ends at %v", last)
	}
}

// "textureFormats" only changes binary load_texture replies; JSON replies
// stay byte-identical.
func TestStdioCompressedTextures(t *testing.T) {
	a, _ := stdioAPI(t)
	texture := materialTexture(t, a)
	lines := []map[string]any{
		{"id": 1, "method": "load_texture", "args": []any{texture, 64}},
		{"id": 2, "method": "load_texture", "args": []any{texture, 64}, "textureFormats": []any{"bc7", "bc3", "bc1"}},
		{"id": 3, "method": "load_texture", "args": []any{texture, 64}, "binary": true, "textureFormats": []any{"bc1", "bc7"}},
		{"id": 4, "method": "load_texture", "args": []any{texture, 64}, "binary": true, "textureFormats": []any{"bc1"}},
		{"id": 5, "method": "load_texture", "args": []any{texture, 64}, "binary": true, "textureFormats": []any{"astc", 7}},
		{"id": 6, "method": "load_texture", "args": []any{texture, 64}, "binary": true, "textureFormats": "bc7"},
		{"id": 7, "method": "load_texture", "args": []any{texture, 64}, "binary": true},
		{"id": 8, "method": "load_texture", "args": []any{"missing", 64}, "binary": true, "textureFormats": []any{"bc7"}},
		{"id": 9, "method": "ping", "args": []any{}, "binary": true, "textureFormats": []any{"bc7"}},
	}
	var input strings.Builder
	for _, line := range lines {
		encoded, _ := json.Marshal(line)
		input.Write(encoded)
		input.WriteByte('\n')
	}
	var output bytes.Buffer
	if err := a.ServeStdio(strings.NewReader(input.String()), &output); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(bytes.NewReader(output.Bytes()))
	replies := map[float64]map[string]any{}
	raw := map[float64][]byte{}
	for offset := 0; offset < output.Len(); {
		before := output.Bytes()[offset:]
		reply, n, _ := readFrame(t, reader, offset)
		raw[reply["id"].(float64)] = before[:n]
		replies[reply["id"].(float64)] = reply
		offset += n
	}
	data := func(id float64) map[string]any {
		result := replies[id]["result"].(map[string]any)
		if result["ok"] != true {
			t.Fatalf("reply %v: %v", id, result)
		}
		return result["data"].(map[string]any)
	}
	if !bytes.Equal(raw[1][len(`{"id":1`):], raw[2][len(`{"id":2`):]) {
		t.Fatal("textureFormats changed a JSON reply")
	}
	checkCompressed(t, data(3), "bc7")
	checkCompressed(t, data(4), "bc1")
	for _, id := range []float64{5, 6, 7} {
		if _, ok := data(id)["png"].(blobValue); !ok {
			t.Fatalf("reply %v is not a PNG frame: %v", id, data(id))
		}
	}
	if result := replies[8]["result"].(map[string]any); result["error"] != "Texture is not registered for the loaded entity. Reload the entity." {
		t.Fatalf("missing: %v", result)
	}
	if replies[9]["result"].(map[string]any)["ok"] != true {
		t.Fatal("ping")
	}
}

func TestServerCompressedTextures(t *testing.T) {
	isolate(t)
	a := newAPI(t, Options{DataDir: tempDir(t)})
	server, err := NewServer(a, t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer server.listener.Close()
	if !strings.Contains(server.bridge(), "setTextureFormats") || !strings.Contains(server.bridge(), "X-Avolition-Texture-Formats") {
		t.Fatal("the bridge cannot pass texture formats")
	}
	texture := materialTexture(t, a)
	post := func(binary bool, formats string) *httptest.ResponseRecorder {
		body, _ := json.Marshal([]any{texture, 64})
		r := httptest.NewRequest(http.MethodPost, "/api/load_texture", bytes.NewReader(body))
		r.Header.Set("X-Preview-Token", server.token)
		r.Header.Set("Content-Length", itoa(len(body)))
		if binary {
			r.Header.Set("X-Avolition-Binary", "1")
		}
		if formats != "" {
			r.Header.Set("X-Avolition-Texture-Formats", formats)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	if plain, withFormats := post(false, ""), post(false, "bc7"); !bytes.Equal(plain.Body.Bytes(), withFormats.Body.Bytes()) {
		t.Fatal("the formats header changed a JSON reply")
	}
	framed := post(true, "bc3,bc7")
	reply, _, isFrame := readFrame(t, bufio.NewReader(bytes.NewReader(framed.Body.Bytes())), 0)
	if !isFrame {
		t.Fatal("not a frame")
	}
	checkCompressed(t, reply["result"].(map[string]any)["data"].(map[string]any), "bc7")
}
