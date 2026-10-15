package fbx_test

// Oracle tests: run owpreview/fbx.py export_fbx on the same inputs and
// compare the files. Python's zlib.compress is wrapped to record its output
// for every array; Go's writer then uses exactly those compressed payloads,
// so the files must match byte for byte, except where NumPy's BLAS/LAPACK
// matrix products differ from Go's loops in the last bit of a double (then
// the parsed trees must agree within 1e-12 and the layout must be
// identical). A second comparison uses Go's own zlib and checks the
// decompressed content.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/fbx"
	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
)

func ftModel(name string) []byte {
	model := ft.NewModel()
	model.Name = name
	return model.Bytes()
}

func ftEntity(name, model, child, attachment string) []byte {
	entity := ft.Entity{Name: name, Model: model, Minor: 1, Relative: "."}
	if child != "" {
		entity.Children = []ft.Child{{Reference: child, Attachment: attachment}}
	}
	return entity.Bytes()
}

const fbxPython = `
import base64, hashlib, sys, tempfile, time, types, zlib
from pathlib import Path
from owpreview import fbx as F, formats, materials

class _Neutral:
    def __init__(self, asset_path, root_path=None, warnings=None):
        self.root = Path(root_path).resolve() if root_path else materials._default_root(Path(asset_path).resolve())
        self.textures = {}
    def for_model(self, model, entity_look='', **kwargs):
        return {}

materials.MaterialResolver = _Neutral
real = zlib.compress
pairs = {}
def compress(raw, level):
    out = real(raw, level)
    pairs[hashlib.sha256(raw).hexdigest()] = base64.b64encode(out).decode()
    return out
F.zlib = types.SimpleNamespace(compress=compress)
out = []
for case in args[0]:
    asset = case['asset'] if case.get('asset') is not None else formats.load_asset(*case['load'])
    clip = case.get('clip')
    if isinstance(clip, str):
        clip = formats.load_clip(clip)
    with tempfile.TemporaryDirectory() as temp:
        path = Path(temp) / 'x.fbx'
        try:
            start = time.perf_counter()
            report = F.export_fbx(path, asset, clip, **case['kwargs'])
            elapsed = time.perf_counter() - start
            out.append({'ok': True, 'report': report, 'seconds': elapsed,
                        'data': base64.b64encode(path.read_bytes()).decode()})
        except Exception as error:
            out.append({'ok': False, 'error': str(error)})
result = {'cases': out, 'pairs': pairs}
`

type fbxCase struct {
	label string
	asset *formats.Asset
	load  []string // Python loads the asset itself (exact float32 values)
	clip  *formats.Clip
	path  string // clip path for Python's load_clip
	opts  fbx.Options
	// pythonLoads makes Python run load_asset itself (benchmark).
	pythonLoads bool
}

func (c fbxCase) request(t *testing.T) map[string]any {
	kwargs := map[string]any{"up_axis": c.opts.UpAxis, "include_mesh": c.opts.IncludeMesh, "root_motion": c.opts.RootMotion}
	if c.opts.RigID != "" {
		kwargs["rig_id"] = c.opts.RigID
	}
	if c.opts.ClipName != "" {
		kwargs["clip_name"] = c.opts.ClipName
	}
	if c.opts.Materials != nil {
		kwargs["materials"] = c.opts.Materials
	}
	request := map[string]any{"kwargs": kwargs}
	// Python receives the asset Go loaded (exactly, see exporttest.PyJSON):
	// formats' rest matrices may differ from Python's load_asset in the
	// last bit (Go's math.Sin/Cos are not glibc's), which is the formats
	// port's tolerance, not this writer's.
	if c.asset == nil || c.pythonLoads {
		request["load"] = c.load
	} else {
		request["asset"] = exporttest.PyJSON(t, c.asset)
	}
	switch {
	case c.path != "":
		request["clip"] = c.path
	case c.clip != nil:
		request["clip"] = exporttest.PyJSON(t, c.clip)
	}
	return request
}

type pythonFBX struct {
	Cases []struct {
		OK      bool            `json:"ok"`
		Error   string          `json:"error"`
		Report  json.RawMessage `json:"report"`
		Data    string          `json:"data"`
		Seconds float64         `json:"seconds"`
	} `json:"cases"`
	Pairs map[string]string `json:"pairs"`
}

func runPython(t *testing.T, cases []fbxCase) (*pythonFBX, fbx.Compressor) {
	var requests []any
	for _, c := range cases {
		requests = append(requests, loadCase(t, c).request(t))
	}
	raw := oracle.Exec(t, fbxPython, requests).MustValue(t)
	var result pythonFBX
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	missing := 0
	compress := func(data []byte) []byte {
		sum := sha256.Sum256(data)
		if encoded, ok := result.Pairs[hex.EncodeToString(sum[:])]; ok {
			out, _ := base64.StdEncoding.DecodeString(encoded)
			return out
		}
		// An array whose content differs from Python's (see the test).
		mu.Lock()
		missing++
		mu.Unlock()
		return fbx.ZlibBestSpeed(data)
	}
	t.Cleanup(func() { t.Logf("%d arrays were not among Python's (different content)", missing) })
	return &result, compress
}

func demoCase(label string, opts fbx.Options) fbxCase {
	root := filepath.Join(exporttest.TestsDir(), "demo-assets")
	entity := filepath.Join(root, "Entities/PreviewRobot/PreviewRobot.owentity")
	return fbxCase{label: label, load: []string{entity, root}, path: filepath.Join(root, "Animations/001_Wave.owanimclip"), opts: opts}
}

func loadCase(t *testing.T, c fbxCase) fbxCase {
	if c.load != nil {
		asset, err := formats.LoadAsset(c.load[0], c.load[1])
		if err != nil {
			t.Fatal(err)
		}
		c.asset = asset
	}
	if c.path != "" {
		clip, err := formats.LoadClip(c.path)
		if err != nil {
			t.Fatal(err)
		}
		c.clip = clip
	}
	return c
}

func with(opts fbx.Options, change func(*fbx.Options)) fbx.Options {
	change(&opts)
	return opts
}

func materialsFixture() *fbx.Materials {
	maps := ordered.New[*formats.MaterialMap]()
	maps.Set("baseColor", &formats.MaterialMap{TextureID: "a", RelativePath: "tex\\color.png", Repeat: [2]float64{2, 0}})
	maps.Set("normal", &formats.MaterialMap{TextureID: "b", UVIndex: 1, RelativePath: "tex/normal.png", Repeat: [2]float64{1, 1}})
	maps.Set("ao", &formats.MaterialMap{TextureID: "c", RelativePath: "tex/ao.png", Repeat: [2]float64{1, 1}})
	maps.Set("emissive", &formats.MaterialMap{TextureID: "d", RelativePath: "tex/glow.png", Repeat: [2]float64{1, 1}})
	intensity := 3.5
	descriptor := &formats.Material{Name: "Robot paint", BaseColor: [3]float64{.2, .3, .4}, Roughness: .3, Metalness: .7,
		Emissive: [3]float64{1, .5, .25}, Opacity: 1.4, AlphaMode: "opaque", Maps: maps, EmissiveIntensity: &intensity,
		UnbakedMaps: []string{"emissive"}}
	descriptors := ordered.New[*formats.Material]()
	descriptors.Set("4000", descriptor)
	materials := ordered.New[*ordered.Map[*formats.Material]]()
	return func() *fbx.Materials { materials.Set("model-0", descriptors); return materials }()
}

func fbxCases(t *testing.T) []fbxCase {
	defaults := fbx.DefaultOptions()
	cases := []fbxCase{
		demoCase("demo", defaults),
		demoCase("demo Z", with(defaults, func(o *fbx.Options) { o.UpAxis = "Z" })),
		demoCase("demo skeleton only", with(defaults, func(o *fbx.Options) { o.IncludeMesh = false })),
		demoCase("demo in place", with(defaults, func(o *fbx.Options) { o.RootMotion = false; o.ClipName = "Wave in place" })),
		demoCase("demo materials", with(defaults, func(o *fbx.Options) { o.Materials = materialsFixture() })),
	}
	root := filepath.Join(exporttest.TestsDir(), "demo-assets")
	for _, name := range []string{"002_Turn", "003_Root_Move"} {
		c := demoCase(name, defaults)
		c.path = filepath.Join(root, "Animations", name+".owanimclip")
		cases = append(cases, c)
	}
	material := filepath.Join(exporttest.TestsDir(), "material-assets")
	cases = append(cases, fbxCase{label: "material robot", load: []string{filepath.Join(material, "Entities/MaterialRobot/MaterialRobot.owentity"), material},
		path: filepath.Join(material, "Animations/002_Turn.owanimclip"), opts: defaults})

	dir := t.TempDir()
	entity := exporttest.AttachedEntity(t, dir)
	clip := ft.Write(t, dir, "clip.owanimclip", ft.NewClip().Bytes())
	for _, rig := range []string{"auto", "model-0", "model-1", "model-2"} {
		cases = append(cases, fbxCase{label: "attached " + rig, load: []string{entity, dir}, path: clip,
			opts: with(defaults, func(o *fbx.Options) { o.RigID = rig })})
	}
	cases = append(cases, fbxCase{label: "attached rest", load: []string{entity, dir}, opts: defaults})

	asset, oracleClip := playbackAsset(t)
	cases = append(cases, fbxCase{label: "oracle Z locked", asset: asset, clip: oracleClip,
		opts: fbx.Options{UpAxis: "Z", IncludeMesh: false, RootMotion: false}})
	cases = append(cases, fbxCase{label: "oracle 29.97", asset: asset, clip: exporttest.Clip("Odd rate", 29.97, 20, oracleClip.Bones), opts: defaults})
	fixture := exporttest.LoadPlayback(t)
	duplicates := &formats.Asset{Name: "Duplicates", Warnings: []string{}, Models: []*formats.Model{
		{ID: "child", Name: "Other", Path: "Other", Bones: fixture.Bones[:1], Meshes: []*formats.Mesh{}, Sockets: []formats.Socket{}},
		{ID: "main", Name: "Selected", Path: "Selected", Bones: fixture.Bones, Meshes: []*formats.Mesh{}, Sockets: []formats.Socket{}}}}
	cases = append(cases, fbxCase{label: "duplicates", asset: duplicates, clip: exporttest.Clip("Oracle", 30, 1, fixture.Tracks),
		opts: with(defaults, func(o *fbx.Options) { o.RigID = "main" })})
	selected, selectedClip := exporttest.SelectedFixture(t)
	cases = append(cases, fbxCase{label: "selected fixture", asset: selected, clip: selectedClip, opts: defaults})

	// Errors.
	cases = append(cases,
		fbxCase{label: "axis", asset: asset, clip: oracleClip, opts: with(defaults, func(o *fbx.Options) { o.UpAxis = "X" })},
		fbxCase{label: "no models", asset: &formats.Asset{Models: []*formats.Model{}, Warnings: []string{}}, clip: oracleClip, opts: defaults},
		fbxCase{label: "timing", asset: asset, clip: exporttest.Clip("Bad", 0, 2, nil), opts: defaults},
		fbxCase{label: "negative", asset: asset, clip: exporttest.Clip("Bad", 30, -1, nil), opts: defaults},
		fbxCase{label: "missing rig", asset: asset, clip: oracleClip, opts: with(defaults, func(o *fbx.Options) { o.RigID = "missing" })},
		fbxCase{label: "no match", asset: asset, clip: exporttest.Clip("Other", 30, 2, []formats.ClipBone{{Name: "other"}}), opts: defaults},
		fbxCase{label: "too long", asset: asset, clip: exporttest.Clip("Long", 30, 250_000, nil), opts: defaults},
	)
	return cases
}

func TestExportFBXMatchesPythonByteForByte(t *testing.T) {
	cases := fbxCases(t)
	python, compress := runPython(t, cases)
	identical := 0
	for i, c := range cases {
		c = loadCase(t, c)
		want := python.Cases[i]
		opts := c.opts
		opts.Compress = compress
		data, report, err := fbx.Encode(c.asset, c.clip, opts)
		if !want.OK {
			if err == nil || err.Error() != want.Error {
				t.Errorf("%s: got %v, want error %q", c.label, err, want.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.label, err)
			continue
		}
		expected, _ := base64.StdEncoding.DecodeString(want.Data)
		if string(expected) == string(data) {
			jsoncmp.Equal(t, want.Report, report, jsoncmp.Options{})
			identical++
			continue
		}
		// Not identical: every value within 1e-12, and only skin cluster
		// Transform matrices (inv(boneWorld) @ meshWorld; NumPy inverts with
		// LAPACK) may differ in their last bits. Their compressed size then
		// differs too, which shifts the rest of the file.
		jsoncmp.Equal(t, want.Report, report, jsoncmp.Options{Ignore: []string{"$.bytes"}})
		a, _ := readBinaryFBX(t, expected)
		b, _ := readBinaryFBX(t, data)
		var diffs []string
		diffRecords("", a, b, 1e-12, &diffs)
		if len(diffs) > 0 {
			t.Errorf("%s differs:\n%s", c.label, diffs)
		}
		var exact []string
		diffRecords("", a, b, 0, &exact)
		for _, d := range exact {
			if !strings.Contains(d, "/Transform[") {
				t.Errorf("%s: %s", c.label, d)
			}
		}
		t.Logf("%s: %d LAPACK-inverse Transform arrays differ in the last bits", c.label, len(exact))
	}
	t.Logf("%d of %d successful exports byte-identical to Python", identical, len(cases)-7)
}

func TestExportFBXWithGoZlibMatchesPythonContent(t *testing.T) {
	cases := fbxCases(t)[:8]
	python, _ := runPython(t, cases)
	for i, c := range cases {
		c = loadCase(t, c)
		dir := t.TempDir()
		report, err := fbx.ExportFBX(filepath.Join(dir, "x.fbx"), c.asset, c.clip, c.opts)
		if err != nil {
			t.Fatal(c.label, err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "x.fbx"))
		expected, _ := base64.StdEncoding.DecodeString(python.Cases[i].Data)
		a, _ := readBinaryFBX(t, expected)
		b, _ := readBinaryFBX(t, data)
		var diffs []string
		diffRecords("", a, b, 1e-12, &diffs)
		if len(diffs) > 0 {
			t.Errorf("%s differs:\n%s", c.label, diffs)
		}
		var want map[string]any
		_ = json.Unmarshal(python.Cases[i].Report, &want)
		if report.Bytes != int64(len(data)) {
			t.Fatal(report.Bytes)
		}
		t.Logf("%s: Go %d bytes, Python %d bytes", c.label, len(data), len(expected))
	}
}

// TestExportFBXBenchmarkVsPython times both writers on a large rig and clip
// (AVOLITION_BENCH=1).
func TestExportFBXBenchmarkVsPython(t *testing.T) {
	if os.Getenv("AVOLITION_BENCH") == "" {
		t.Skip("set AVOLITION_BENCH=1 to compare export_fbx timings with Python")
	}
	dir := t.TempDir()
	model := ft.Write(t, dir, "Models/Big.00C/Big.owmdl", ft.LargeModel(200_000, 120))
	clip := ft.Write(t, dir, "Animations/Big.owanimclip", ft.LargeClip(120, 300))
	c := loadCase(t, fbxCase{label: "large", load: []string{model, dir}, path: clip, opts: fbx.DefaultOptions(), pythonLoads: true})
	python, _ := runPython(t, []fbxCase{c})
	start := time.Now()
	data, _, err := fbx.Encode(c.asset, c.clip, c.opts)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("export_fbx 200k vertices, 120 bones x 300 frames: Go %v (%d MB), Python %.3fs (%.1fx)",
		elapsed, len(data)>>20, python.Cases[0].Seconds, python.Cases[0].Seconds/elapsed.Seconds())
	if math.IsNaN(python.Cases[0].Seconds) {
		t.Fatal(fmt.Sprint(python.Cases[0]))
	}
}

func BenchmarkExportFBX(b *testing.B) {
	dir := b.TempDir()
	model := ft.Write(b, dir, "Models/Big.00C/Big.owmdl", ft.LargeModel(200_000, 120))
	clipPath := ft.Write(b, dir, "Animations/Big.owanimclip", ft.LargeClip(120, 300))
	asset, err := formats.LoadAsset(model, dir)
	if err != nil {
		b.Fatal(err)
	}
	clip, err := formats.LoadClip(clipPath)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := fbx.Encode(asset, clip, fbx.DefaultOptions()); err != nil {
			b.Fatal(err)
		}
	}
}
