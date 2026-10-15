package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/mapexport"
)

var mockC4DBytes = []byte("MOCK C4D SCENE: API transaction fixture, not a native Cinema 4D file.")

type mockStatus struct {
	connected, octane bool
	message           string
	extra             map[string]any
}

func (s *mockStatus) Connected() bool { return s.connected }
func (s *mockStatus) Octane() bool    { return s.octane }
func (s *mockStatus) Message() string { return s.message }
func (s *mockStatus) MarshalJSON() ([]byte, error) {
	out := map[string]any{"connected": s.connected, "octane": s.octane, "message": s.message}
	for k, v := range s.extra {
		out[k] = v
	}
	return json.Marshal(out)
}

type bridgeCall struct {
	Payload     map[string]any
	Destination string
	Textures    map[string][]byte
	StagedFBX   []byte
}

// mockBridge records the bridge contract, substituting only Cinema's native
// writer (MockBridgeClient in the Python tests).
type mockBridge struct {
	mu          sync.Mutex
	root        string
	status      *mockStatus
	fail        bool
	calls       []bridgeCall
	statusCalls int
	writer      func(payload map[string]any, destination string) // optional custom writer
}

func newMockBridge() *mockBridge {
	return &mockBridge{status: &mockStatus{connected: true, octane: true, message: "Mock test bridge connected."}}
}

func (b *mockBridge) Root() string { return b.root }
func (b *mockBridge) Status() BridgeStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.statusCalls++
	return b.status
}

func (b *mockBridge) Export(payload any, destination, textureFolder string) ([]string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	call := bridgeCall{Payload: decoded, Destination: destination, Textures: map[string][]byte{}}
	matches, _ := filepath.Glob(filepath.Join(textureFolder, "*.png"))
	for _, path := range matches {
		data, _ := os.ReadFile(path)
		call.Textures[filepath.Base(path)] = data
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(destination), "animation.fbx")); err == nil {
		call.StagedFBX = data
	}
	b.mu.Lock()
	b.calls = append(b.calls, call)
	b.mu.Unlock()
	if b.writer != nil {
		b.writer(decoded, destination)
	} else if err := os.WriteFile(destination, mockC4DBytes, 0o644); err != nil {
		return nil, err
	}
	if b.fail {
		return nil, errors.New("Mock native writer failed after writing a partial scene.")
	}
	return []string{"Mock native writer: native C4D compatibility is not tested."}, nil
}

func (b *mockBridge) last(t testing.TB) bridgeCall {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		t.Fatal("no bridge calls")
	}
	return b.calls[len(b.calls)-1]
}

// withBridge returns deps whose bridge is the mock.
func withBridge(bridge *mockBridge) *Deps {
	deps := Default()
	deps.NewBridge = func(root string) Bridge { bridge.root = root; return bridge }
	return &deps
}

func requireBake(t testing.TB, a *API) {
	t.Helper()
	if a.deps.BakeMaterials == nil {
		t.Skip("waiting for exporttextures (bake_materials)")
	}
}

// MapBridge adapts the mock to map_export's bridge contract.
func (b *mockBridge) MapBridge() mapexport.Bridge { return mockMapBridge{b} }

type mockMapBridge struct{ b *mockBridge }

func (m mockMapBridge) Status() *c4dbridge.Status {
	encoded, _ := json.Marshal(m.b.Status())
	status := c4dbridge.NewStatus(nil)
	if err := status.UnmarshalJSON(encoded); err != nil {
		panic(err)
	}
	return status
}

func (m mockMapBridge) Export(payload any, destination, textureFolder string, _ c4dbridge.ExportOptions) (json.RawMessage, error) {
	warnings, err := m.b.Export(payload, destination, textureFolder)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"warnings": warnings})
}
