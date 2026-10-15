package exporttextures

// Oracle tests: bake the same materials with owpreview.export_textures in
// Python and with BakeMaterials into separate folders, then compare the
// returned descriptors (paths normalized), the file names, and the decoded
// pixels of every PNG (both loaded with Pillow).

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/materials"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pyjson"
	"avolition/gocore/internal/pypath"
)

var _ = materials.New // registers the material resolver with formats

func repoRoot() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..", "..")
}

// pyMaterial encodes a descriptor so Python reads Python floats (pyjson
// writes float64 as repr, "1.0"), keeping the identity digests identical.
func pyMaterial(m *formats.Material) any {
	if m == nil {
		return nil
	}
	out := map[string]any{"name": m.Name, "sourcePath": m.SourcePath, "shaderId": m.ShaderID,
		"baseColor": m.BaseColor[:], "roughness": m.Roughness, "metalness": m.Metalness, "emissive": m.Emissive[:],
		"opacity": m.Opacity, "alphaMode": m.AlphaMode, "alphaCutoff": m.AlphaCutoff, "doubleSided": m.DoubleSided}
	maps := map[string]any{}
	if m.Maps != nil {
		m.Maps.Range(func(role string, value *formats.MaterialMap) bool {
			maps[role] = map[string]any{"textureId": value.TextureID, "uvIndex": value.UVIndex,
				"colorSpace": value.ColorSpace, "repeat": value.Repeat[:]}
			return true
		})
	}
	out["maps"] = maps
	if m.EmissiveIntensity != nil {
		out["emissiveIntensity"] = *m.EmissiveIntensity
	}
	if m.EmissiveMode != "" {
		out["emissiveMode"] = m.EmissiveMode
	}
	if m.EmissiveUsesAO != nil {
		out["emissiveUsesAO"] = *m.EmissiveUsesAO
	}
	return out
}

// pyAsset is the part of an asset bake_materials reads, as Python JSON.
func pyAsset(t testing.TB, asset *formats.Asset) json.RawMessage {
	models := []any{}
	for _, model := range asset.Models {
		// Built by hand to keep the material keys in order (Go maps sort).
		materials := []byte{'{'}
		if model.Materials != nil {
			model.Materials.Range(func(key string, m *formats.Material) bool {
				if len(materials) > 1 {
					materials = append(materials, ',')
				}
				materials = pyjson.AppendString(materials, key, false)
				materials = append(materials, ':')
				value, err := pyjson.Dumps(pyMaterial(m), pyjson.Options{Indent: -1})
				if err != nil {
					t.Fatal(err)
				}
				materials = append(materials, value...)
				return true
			})
		}
		materials = append(materials, '}')
		models = append(models, map[string]any{"id": model.ID, "name": model.Name, "materials": json.RawMessage(materials)})
	}
	textures := map[string]any{}
	if asset.Textures != nil {
		asset.Textures.Range(func(id string, spec *formats.Texture) bool {
			textures[id] = spec
			return true
		})
	}
	data, err := pyjson.Dumps(map[string]any{"path": asset.Path, "textureRoot": asset.TextureRoot,
		"textures": textures, "warnings": asset.Warnings, "models": models}, pyjson.Options{Indent: -1})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

const bakePython = `
import time
from owpreview.export_textures import bake_materials
from owpreview.formats import load_asset
asset, target, prefix = args
if isinstance(asset, str):
    asset = load_asset(asset)
calls = []
start = time.perf_counter()
baked = bake_materials(asset, target, prefix, progress=lambda message, completed, total: calls.append([message, completed, total]))
result = {'result': baked, 'progress': calls, 'seconds': time.perf_counter() - start}
`

const pixelsPython = `
import numpy as np
from PIL import Image
bad = []
for a, b in args[0]:
    with Image.open(a) as x, Image.open(b) as y:
        x.load(); y.load()
        if x.size != y.size:
            bad.append([a, b, 'size', list(x.size), list(y.size)])
            continue
        p = np.asarray(x.convert('RGBA'))
        q = np.asarray(y.convert('RGBA'))
        if x.mode != y.mode or not np.array_equal(p, q):
            bad.append([a, b, x.mode, y.mode, int(np.count_nonzero(np.any(p != q, axis=-1)))])
result = bad
`

var bytesIO = regexp.MustCompile(`<_io\.BytesIO object at 0x[0-9a-f]+>`)

type comparison struct {
	pythonSeconds, goSeconds float64
	files                    int
}

// compareBake bakes asset (or, for Python, pythonAsset when it is a path to
// load) in both implementations and compares everything.
func compareBake(t *testing.T, asset *formats.Asset, pythonAsset any, prefix string) comparison {
	t.Helper()
	base := pypath.Resolve(t.TempDir())
	pyDir, goDir := filepath.Join(base, "python", "tex"), filepath.Join(base, "go", "tex")
	reply := oracle.Exec(t, bakePython, pythonAsset, pyDir, prefix).MustValue(t)
	var python struct {
		Result   json.RawMessage `json:"result"`
		Progress [][]any         `json:"progress"`
		Seconds  float64         `json:"seconds"`
	}
	if err := json.Unmarshal(reply, &python); err != nil {
		t.Fatal(err)
	}
	var progress [][]any
	start := time.Now()
	result, err := BakeMaterials(asset, goDir, prefix, func(message string, completed, total int) {
		progress = append(progress, []any{message, float64(completed), float64(total)})
	})
	goSeconds := time.Since(start).Seconds()
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(string(python.Result), pyDir, goDir)
	want = bytesIO.ReplaceAllString(want, "<_io.BytesIO>")
	gotText := bytesIO.ReplaceAllString(encoded.String(), "<_io.BytesIO>")
	diffs, err := jsoncmp.Diff([]byte(want), []byte(gotText), jsoncmp.Options{MaxDiffs: 30})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		t.Error(d)
	}
	if fmt.Sprint(progress) != fmt.Sprint(python.Progress) {
		t.Errorf("progress differs:\n go %v\n py %v", progress, python.Progress)
	}
	// Same file names on disk, and identical pixels.
	pyFiles, _ := os.ReadDir(pyDir)
	goFiles, _ := os.ReadDir(goDir)
	var pyNames, goNames []string
	for _, e := range pyFiles {
		pyNames = append(pyNames, e.Name())
	}
	for _, e := range goFiles {
		goNames = append(goNames, e.Name())
	}
	if fmt.Sprint(pyNames) != fmt.Sprint(goNames) {
		t.Fatalf("file names differ:\n go %v\n py %v", goNames, pyNames)
	}
	var pairs [][2]string
	for _, name := range pyNames {
		pairs = append(pairs, [2]string{filepath.Join(pyDir, name), filepath.Join(goDir, name)})
	}
	if len(pairs) > 0 {
		var bad []any
		if err := json.Unmarshal(oracle.Exec(t, pixelsPython, pairs).MustValue(t), &bad); err != nil {
			t.Fatal(err)
		}
		for _, b := range bad {
			t.Errorf("pixels differ: %v", b)
		}
	}
	return comparison{python.Seconds, goSeconds, len(pairs)}
}

// noise writes a deterministic RGBA PNG with smooth gradients plus noise.
func noise(t *testing.T, dir, name string, width, height int, seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 7))
	pixels := make([][4]byte, width*height)
	for y := range height {
		for x := range width {
			var p [4]byte
			for c := range 4 {
				v := float64((x*(c+1)*37+y*(5-c)*11)%256) + r.NormFloat64()*40
				p[c] = byte(math.Max(0, math.Min(255, v)))
			}
			if r.IntN(17) == 0 {
				p[3] = 255
			}
			if r.IntN(23) == 0 {
				p[3] = 0
			}
			pixels[y*width+x] = p
		}
	}
	return ft.Write(t, dir, name, ft.PNGBytes(width, height, func(x, y int) [4]byte { return pixels[y*width+x] }))
}

var sizes = [][2]int{{64, 64}, {37, 23}, {128, 96}, {16, 16}, {64, 128}, {64, 32}, {23, 37}, {1, 1}, {96, 64}}

func TestOracleBakeShaderMaterials(t *testing.T) {
	dir := pypath.Resolve(t.TempDir())
	for i, size := range sizes {
		noise(t, dir, fmt.Sprintf("T/t%d.png", i), size[0], size[1], uint64(i))
	}
	all := []uint32{2903569922, 3093211343, 67369114, 1716930793, 1239794147, 3411049489, 378934698, 229655782, 562391268,
		2762290483, 548341454, 2018946191, 763550166, 1239777102, 3111105361, 3166598269, 1523270506, 1493127177,
		1482859648, 1140682086, 3335614873, 3761386704, 3989656707, 1117188170}
	inputs := [][]ft.StaticInput{
		nil,
		{{Key: 3250491852, Value: ft.Pack("f", 3)}, {Key: 1883253226, Value: ft.Pack("ff", 2, 3)}},
		{{Key: 3344068240, Value: ft.Pack("I", 2)}, {Key: 2446772623, Value: ft.Pack("f", .5)}, {Key: 4081294361}},
	}
	shaders := []uint32{0, 11, 34, 36, 37, 38, 43, 44, 50, 51, 54, 56, 167, 217, 221}
	var paths []string
	n := 0
	for s, shader := range shaders {
		for i, input := range inputs {
			var bindings [][]uint32
			if i == 0 && s%5 == 0 {
				for _, key := range all {
					bindings = append(bindings, []uint32{key})
				}
			}
			bindings = append(bindings, all, []uint32{all[(n*7)%len(all)], all[(n*3+5)%len(all)], all[(n*5+2)%len(all)], 4101268840, 758934576},
				[]uint32{2903569922, 1239794147, 3335614873, 1493127177}, []uint32{67369114, 1493127177, 3166598269, 3761386704})
			for _, keys := range bindings {
				var refs []ft.TextureRef
				for j, key := range keys {
					refs = append(refs, ft.TextureRef{Reference: fmt.Sprintf("T/t%d.png", (j*3+n)%len(sizes)), Binding: key})
				}
				paths = append(paths, ft.Write(t, dir, fmt.Sprintf("M/%04d.owmat", n), ft.MaterialBytes(refs, input, shader)))
				n++
			}
		}
	}
	var warnings []string
	resolver := materials.New(filepath.Join(dir, "model.owmdl"), dir, &warnings)
	asset := &formats.Asset{Path: filepath.Join(dir, "model.owmdl"), TextureRoot: dir, Warnings: []string{"Loaded"}}
	for i, path := range paths {
		descriptor, err := resolver.Descriptor(path)
		if err != nil {
			t.Fatal(err)
		}
		// Several materials per model; some models repeat earlier ones.
		if i%6 == 0 {
			asset.Models = append(asset.Models, &formats.Model{ID: fmt.Sprintf("model-%d", i/6), Name: fmt.Sprintf("Model %d", i/6),
				Materials: ordered.New[*formats.Material]()})
		}
		model := asset.Models[len(asset.Models)-1]
		model.Materials.Set(fmt.Sprint(1000+i), descriptor)
		if i%11 == 0 && i > 0 {
			// The same descriptor again under another key and model.
			first, _ := asset.Models[0].Materials.Get(asset.Models[0].Materials.Keys()[0])
			model.Materials.Set(fmt.Sprint(5000+i), first)
		}
	}
	asset.Textures = resolver.Textures()
	asset.Warnings = append(asset.Warnings, warnings...)
	c := compareBake(t, asset, pyAsset(t, asset), "Map 01/x")
	t.Logf("%d materials, %d files; Python %.2fs, Go %.2fs", len(paths), c.files, c.pythonSeconds, c.goSeconds)
}
