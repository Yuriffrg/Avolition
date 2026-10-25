package mapexport

import (
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/formats"
)

type oldTimingBridge struct{ *mockBridge }

func (b oldTimingBridge) Status() *c4dbridge.Status {
	status := b.mockBridge.Status()
	status.Set("mapTiming24", false)
	status.Set("bridgeVersion", "0.7.5")
	return status
}

func TestMapTimingPreflightBeforeGeometryAndBaking(t *testing.T) {
	output := filepath.Join(t.TempDir(), "output")
	bridge := oldTimingBridge{newMock()}
	// Deliberately incomplete geometry: preflight must reject the old companion
	// before payload validation, expensive baking or output staging.
	asset := &formats.Asset{Name: "Map"}
	progress := false
	_, err := ExportMap(asset, output, nil, Options{BakeTextures: true, Bridge: bridge, Progress: func(string, *int, *int) { progress = true }})
	if err == nil || err.Error() != c4dbridge.FeatureBridgeMessage {
		t.Fatalf("Wrong preflight error: %v", err)
	}
	if progress || len(bridge.calls) > 0 {
		t.Fatal("Export work began despite missing timing capability")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("Output staging created")
	}
}
