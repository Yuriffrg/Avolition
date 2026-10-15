package mapexport

// Ports tests/test_map_export.py (native map scene contract with a mocked
// bridge) and MapExportNormalTests from tests/test_export_normals.py.

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/c4dexport"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/mapformats"
	"avolition/gocore/internal/mapformats/maptest"
	_ "avolition/gocore/internal/materials"
	"avolition/gocore/internal/objectnames"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

// mockBridge simulates only the external native save; staging and
// publication are real.
type mockBridge struct {
	connected, capability bool
	failure               string
	calls                 []map[string]any
	raw                   [][]byte
}

func newMock() *mockBridge { return &mockBridge{connected: true, capability: true} }

func (b *mockBridge) Status() *c4dbridge.Status {
	fields := ordered.New[json.RawMessage]()
	set := func(key string, value any) {
		data, _ := json.Marshal(value)
		fields.Set(key, data)
	}
	set("connected", b.connected)
	set("octane", true)
	set("mapInstances", b.capability)
	if b.capability {
		set("bridgeVersion", "0.7.4")
	} else {
		set("bridgeVersion", "0.7.2")
	}
	if b.connected {
		set("message", "Ready")
	} else {
		set("message", "Cinema 4D is disconnected.")
	}
	return c4dbridge.NewStatus(fields)
}

func (b *mockBridge) Export(payload any, destination, textureFolder string, options c4dbridge.ExportOptions) (json.RawMessage, error) {
	data, err := c4dbridge.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	b.calls = append(b.calls, decoded)
	b.raw = append(b.raw, data)
	if !options.MapInstances {
		return nil, errors.New("map payload not flagged")
	}
	if b.failure != "" {
		return nil, &c4dbridge.RuntimeError{Message: b.failure}
	}
	if err := os.WriteFile(destination, []byte("MOCK C4D SCENE; NOT A NATIVE FILE"), 0o644); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"warnings":[]}`), nil
}

func loadFixture(t *testing.T) (string, *formats.Asset) {
	t.Helper()
	root := pypath.Resolve(t.TempDir())
	asset, err := mapformats.LoadMap(maptest.WriteFixture(t, filepath.Join(root, "source")), "")
	if err != nil {
		t.Fatal(err)
	}
	return root, asset
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func payloadJSON(t *testing.T, payload *StaticPayload) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(encode(t, payload), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestStaticPayloadIsCompactHasNoAnimationAndPreservesSource(t *testing.T) {
	_, asset := loadFixture(t)
	before := encode(t, asset)
	payload, err := MakeStaticPayload(asset, nil, "Map name")
	if err != nil {
		t.Fatal(err)
	}
	decoded := payloadJSON(t, payload)
	if got := string(encode(t, decoded["animation"])); got != `{"fps":30,"lastFrame":0,"tracks":[]}` {
		t.Fatal(got)
	}
	if decoded["mapInstances"] != true || decoded["name"] != "Map name" {
		t.Fatal(decoded["mapInstances"], decoded["name"])
	}
	if payload.Asset.MeshPrototypes.Len() != 3 {
		t.Fatal(payload.Asset.MeshPrototypes.Len())
	}
	for _, model := range decoded["asset"].(map[string]any)["models"].([]any) {
		if _, ok := model.(map[string]any)["meshes"]; ok {
			t.Fatal("expanded model")
		}
	}
	if !bytes.Equal(encode(t, asset), before) {
		t.Fatal("asset changed")
	}
	expanded, err := mapformats.ExpandMapAsset(payload.Asset)
	if err != nil {
		t.Fatal(err)
	}
	compact := encode(t, payload)
	full := *payload
	full.Asset = expanded
	if len(compact) >= len(encode(t, &full)) {
		t.Fatal("compact payload is not smaller")
	}
}

func TestCompactNativeLookupAppliesOnlyIndividualMeshAlias(t *testing.T) {
	_, asset := loadFixture(t)
	names := ordered.New[string]()
	names.Set("0", "First instance only")
	asset.Models[0].MeshNames = names
	payload, err := MakeStaticPayload(asset, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	models, err := c4dexport.AssetModels(payload.Asset)
	if err != nil {
		t.Fatal(err)
	}
	keys := models.Keys()
	first, _ := models.Get(keys[0])
	second, _ := models.Get(keys[1])
	if first.Meshes[0].Name != "First instance only" || second.Meshes[0].Name != "Triangle" {
		t.Fatal(first.Meshes[0].Name, second.Meshes[0].Name)
	}
	if &first.Meshes[0].Positions[0] != &second.Meshes[0].Positions[0] {
		t.Fatal("geometry not shared")
	}
	asset.Models[0].MeshPrototype = "missing"
	if _, err := MakeStaticPayload(asset, nil, ""); err == nil || !strings.Contains(err.Error(), "missing mesh prototype") {
		t.Fatal(err)
	}
}

func TestNonuniformScaleAndAttachmentsReachNativeTransform(t *testing.T) {
	_, asset := loadFixture(t)
	payload, err := MakeStaticPayload(asset, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]formats.Node{}
	for _, node := range payload.Asset.ExportHierarchy {
		nodes[node.ID] = node
	}
	placed := nodes["map-2-model-0:model"].Matrix
	// c4d_scene._matrix: off = (m12, m13, -m14) * 100, v1.y = m1, v2.x = m4, v3.z = m10.
	if placed[12]*100 != -100 || placed[13]*100 != 200 || -placed[14]*100 != -300 {
		t.Fatal(placed)
	}
	for _, c := range [][2]float64{{placed[1], -2}, {placed[4], -3}, {placed[10], 4}} {
		if math.Abs(c[0]-c[1]) > 1e-6 {
			t.Fatal(placed)
		}
	}
	child := nodes["map-3-model-1:entity"]
	if child.ParentID == nil || *child.ParentID != "map-3-model-0:bone:1" {
		t.Fatal(child.ParentID)
	}
	if child.Matrix != asset.Models[len(asset.Models)-1].AttachmentMatrix {
		t.Fatal(child.Matrix)
	}
}

func TestExportStagePublishesOneC4DAndPreservesExistingFile(t *testing.T) {
	root, asset := loadFixture(t)
	output := filepath.Join(root, "sortie café")
	os.Mkdir(output, 0o755)
	original := filepath.Join(output, "Máp.c4d")
	os.WriteFile(original, []byte("EXISTING USER SCENE"), 0o644)
	bridge := newMock()
	name := "Máp"
	opts := DefaultOptions()
	opts.BakeTextures, opts.Bridge = false, bridge
	result, err := ExportMap(asset, output, &name, opts)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(original); string(data) != "EXISTING USER SCENE" {
		t.Fatal("overwritten")
	}
	if filepath.Base(*result.C4DPath) != "Máp (2).c4d" || len(result.Files) != 1 {
		t.Fatal(*result.C4DPath, result.Files)
	}
	entries, _ := os.ReadDir(output)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".owanimclip") || strings.HasPrefix(e.Name(), ".whitehound-export-") {
			t.Fatal(e.Name())
		}
	}
	if got := string(encode(t, bridge.calls[0]["animation"].(map[string]any)["tracks"])); got != "[]" {
		t.Fatal(got)
	}
}

func TestOldBridgeAndDisconnectionFailBeforeOutputCreation(t *testing.T) {
	root, asset := loadFixture(t)
	disconnected := newMock()
	disconnected.connected = false
	old := newMock()
	old.capability = false
	for _, c := range []struct {
		bridge  *mockBridge
		pattern string
	}{{old, `bridge 0\.7\.2.*Avolition C4D Bridge 0\.7\.5`}, {disconnected, "disconnected"}} {
		output := filepath.Join(root, "never-created")
		name := "Map"
		opts := DefaultOptions()
		opts.Bridge = c.bridge
		_, err := ExportMap(asset, output, &name, opts)
		var runtime *c4dbridge.RuntimeError
		if !errors.As(err, &runtime) || !regexp.MustCompile(c.pattern).MatchString(err.Error()) {
			t.Fatalf("%v", err)
		}
		if _, err := os.Stat(output); err == nil {
			t.Fatal("output created")
		}
		if len(c.bridge.calls) != 0 {
			t.Fatal("exported")
		}
	}
}

func TestFailedNativeExportPublishesNothingAndKeepsExistingFiles(t *testing.T) {
	root, asset := loadFixture(t)
	output := filepath.Join(root, "output")
	os.Mkdir(output, 0o755)
	os.WriteFile(filepath.Join(output, "existing.c4d"), []byte("keep"), 0o644)
	bridge := newMock()
	bridge.failure = "Native save failed"
	name := "Map"
	opts := DefaultOptions()
	opts.Bridge = bridge
	if _, err := ExportMap(asset, output, &name, opts); err == nil || err.Error() != "Native save failed" {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(output)
	if len(entries) != 1 || entries[0].Name() != "existing.c4d" {
		t.Fatal(entries)
	}
	if data, _ := os.ReadFile(filepath.Join(output, "existing.c4d")); string(data) != "keep" {
		t.Fatal("changed")
	}
}

func TestTextureBakingDeduplicatesReusedAppearances(t *testing.T) {
	root, asset := loadFixture(t)
	baked, err := bakeMapMaterials(asset, filepath.Join(root, "tex"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if baked.Materials.Len() != 5 {
		t.Fatal(baked.Materials.Keys())
	}
	representatives := map[*ordered.Map[*formats.Material]]bool{}
	for _, model := range asset.Models {
		representatives[model.Materials] = true
	}
	if len(representatives) != 4 { // Five objects; one look used twice.
		t.Fatal(len(representatives))
	}
	bridge := newMock()
	name := "Map"
	opts := DefaultOptions()
	opts.Bridge = bridge
	if _, err := ExportMap(asset, filepath.Join(root, "output"), &name, opts); err != nil {
		t.Fatal(err)
	}
	if len(bridge.calls[0]["materials"].(map[string]any)) != 5 {
		t.Fatal(bridge.calls[0]["materials"])
	}
}

func TestNamesAndSourceDataAreKeptSeparate(t *testing.T) {
	root, asset := loadFixture(t)
	renamed, err := objectnames.ApplyObjectNames(asset, map[string]any{"model:map-0-model-0": "Gate",
		"mesh:map-0-model-0:0": "Gate panel"})
	if err != nil {
		t.Fatal(err)
	}
	bridge := newMock()
	name := "Named map"
	opts := DefaultOptions()
	opts.BakeTextures, opts.Bridge = false, bridge
	if _, err := ExportMap(renamed, filepath.Join(root, "output"), &name, opts); err != nil {
		t.Fatal(err)
	}
	model := bridge.calls[0]["asset"].(map[string]any)["models"].([]any)[0].(map[string]any)
	if model["name"] != "Gate" || model["meshNames"].(map[string]any)["0"] != "Gate panel" {
		t.Fatal(model["name"], model["meshNames"])
	}
	if asset.Models[0].Name != "Prop" || asset.Models[0].MeshNames != nil {
		t.Fatal("source changed")
	}
}

func TestInvalidFileNamesZeroScaleAndShearCannotPublish(t *testing.T) {
	root, asset := loadFixture(t)
	for _, name := range []string{"", "..", "../map", `C:\map`, "NUL", "trail.", strings.Repeat("a", 181), "com¹.txt", "  ", "x.C4D."} {
		opts := DefaultOptions()
		opts.Bridge = newMock()
		if _, err := ExportMap(asset, filepath.Join(root, "out"), &name, opts); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
	for _, value := range []float64{0, .4} {
		copied := *asset
		copied.ExportHierarchy = append([]formats.Node(nil), asset.ExportHierarchy...)
		if value == 0 {
			copied.ExportHierarchy[0].Matrix[0] = 0
		} else {
			copied.ExportHierarchy[0].Matrix[1] = value
		}
		if _, err := MakeStaticPayload(&copied, nil, ""); err == nil {
			t.Errorf("matrix value %v accepted", value)
		}
	}
}

func TestSceneName(t *testing.T) {
	for in, want := range map[string]string{"Map": "Map", "  Map.c4d ": "Map", "Map.C4D": "Map", "a.b": "a.b",
		"CONSOLE": "CONSOLE", "con.x": "", "LPT¹": "", strings.Repeat("日", 180): strings.Repeat("日", 180),
		strings.Repeat("😀", 91): ""} {
		got, err := SceneName(in)
		if want == "" {
			if err == nil {
				t.Errorf("%q accepted", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, err := SceneName(strings.Repeat("😀", 91)); err == nil || err.Error() != "Keep the map scene filename within 180 characters." {
		t.Fatal(err)
	}
}

func TestOutputFolderMustBeAbsoluteDirectory(t *testing.T) {
	root, asset := loadFixture(t)
	file := filepath.Join(root, "file")
	os.WriteFile(file, nil, 0o644)
	for _, folder := range []string{"relative", file} {
		opts := DefaultOptions()
		opts.Bridge = newMock()
		if _, err := ExportMap(asset, folder, nil, opts); err == nil || err.Error() != "Choose a full path to a map output folder." {
			t.Fatal(folder, err)
		}
	}
	notMap := *asset
	notMap.Map = nil
	if _, err := MakeStaticPayload(&notMap, nil, ""); err == nil || err.Error() != "Load a map before exporting a static map scene." {
		t.Fatal(err)
	}
}

// MapExportNormalTests (tests/test_export_normals.py).

func normalFixture(t *testing.T) (string, *formats.Asset, string, *formats.Mesh) {
	root, asset := loadFixture(t)
	key := asset.Models[0].MeshPrototype
	prototype, _ := asset.MeshPrototypes.Get(key)
	mesh := prototype.Meshes[0]
	mesh.Normals[0], mesh.Normals[1], mesh.Normals[2] = 0, 0, 0
	asset.Warnings = append(asset.Warnings, "Keep original map warning")
	return root, asset, key, mesh
}

func TestCompactMapRepairsOncePerPrototypeAndKeepsWarning(t *testing.T) {
	_, asset, key, mesh := normalFixture(t)
	before := encode(t, asset)
	payload, err := MakeStaticPayload(asset, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encode(t, asset), before) {
		t.Fatal("asset changed")
	}
	if strings.Join(payload.Asset.MeshPrototypes.Keys(), ",") != strings.Join(asset.MeshPrototypes.Keys(), ",") {
		t.Fatal("prototype keys")
	}
	repeated := 0
	for _, model := range asset.Models {
		if model.MeshPrototype == key {
			repeated++
		}
	}
	if repeated < 2 {
		t.Fatal("fixture must exercise reused compact map geometry")
	}
	if strings.Join(payload.Warnings, "|") != strings.Join(payload.Asset.Warnings, "|") {
		t.Fatal("warnings")
	}
	var repairs []string
	keep := false
	for _, w := range payload.Warnings {
		keep = keep || w == "Keep original map warning"
		if strings.Contains(strings.ToLower(w), "normal") {
			repairs = append(repairs, w)
		}
	}
	if !keep || len(repairs) != 1 || !regexp.MustCompile(`(^|\D)1(\D|$)`).MatchString(repairs[0]) {
		t.Fatal(payload.Warnings)
	}
	prototype, _ := payload.Asset.MeshPrototypes.Get(key)
	repaired := prototype.Meshes[0]
	for i := 0; i < len(repaired.Normals); i += 3 {
		n := repaired.Normals[i : i+3]
		length := math.Sqrt(float64(n[0])*float64(n[0]) + float64(n[1])*float64(n[1]) + float64(n[2])*float64(n[2]))
		if math.Abs(length-1) > 1e-6 {
			t.Fatal(length)
		}
	}
	if &repaired.Indices[0] != &mesh.Indices[0] && string(encode(t, repaired.Indices)) != string(encode(t, mesh.Indices)) {
		t.Fatal("indices changed")
	}
	if len(payload.Animation.Tracks) != 0 {
		t.Fatal("tracks")
	}
}

func TestMapExportReturnsRepairWarningsWhenBridgeHasNoWarnings(t *testing.T) {
	root, asset, _, _ := normalFixture(t)
	before := encode(t, asset)
	bridge := newMock()
	name := "Repaired map"
	opts := DefaultOptions()
	opts.BakeTextures, opts.Bridge = false, bridge
	result, err := ExportMap(asset, filepath.Join(root, "output"), &name, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encode(t, asset), before) || len(bridge.calls) != 1 {
		t.Fatal("asset changed or calls", len(bridge.calls))
	}
	if string(encode(t, result.Warnings)) != string(encode(t, bridge.calls[0]["warnings"])) {
		t.Fatal(result.Warnings, bridge.calls[0]["warnings"])
	}
	found := false
	for _, w := range result.Warnings {
		found = found || strings.Contains(strings.ToLower(w), "normal")
	}
	if !found {
		t.Fatal(result.Warnings)
	}
	if !pypath.IsFile(*result.C4DPath) {
		t.Fatal("no file")
	}
}

func TestProgressMessages(t *testing.T) {
	root, asset := loadFixture(t)
	var messages []string
	name := "Map"
	opts := DefaultOptions()
	opts.Bridge = newMock()
	opts.Progress = func(message string, completed, total *int) {
		if completed != nil {
			message += " " + string(rune('0'+*completed)) + "/" + string(rune('0'+*total))
		}
		messages = append(messages, message)
	}
	if _, err := ExportMap(asset, filepath.Join(root, "output"), &name, opts); err != nil {
		t.Fatal(err)
	}
	// Two looks; the entity models have no material slots.
	want := "Preparing map geometry|Baking map materials 0/2|Baking map materials 1/2|Baking map materials 2/2|" +
		"Publishing the C4D file and textures to the output folder"
	if got := strings.Join(messages, "|"); got != want {
		t.Fatal(got)
	}
}
