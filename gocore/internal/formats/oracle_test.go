package formats_test

// Oracle tests: run owpreview.formats on the same inputs and compare JSON
// results and error messages. Python's MaterialResolver is replaced by the
// same neutral stand-in Go uses until the materials package registers itself,
// so these tests compare exactly what formats.py itself computes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/pypath"
)

const neutralPython = `
from pathlib import Path
from owpreview import formats, materials

class _Neutral:
    def __init__(self, asset_path, root_path=None, warnings=None):
        self.root = Path(root_path).resolve() if root_path else materials._default_root(Path(asset_path).resolve())
        self.textures = {}

    def for_model(self, model, entity_look='', **kwargs):
        return {}

materials.MaterialResolver = _Neutral
out = []
for name, call_args in args[0]:
    try:
        out.append({'ok': True, 'result': getattr(formats, name)(*call_args)})
    except Exception as error:
        out.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
result = out
`

type pyCall struct {
	label string
	fn    string
	args  []any
}

func callGo(fn string, args []any) (any, error) {
	str := func(i int) string {
		if i >= len(args) || args[i] == nil {
			return ""
		}
		return args[i].(string)
	}
	switch fn {
	case "load_asset":
		return formats.LoadAsset(str(0), str(1))
	case "load_clip":
		return formats.LoadClip(str(0))
	case "read_clip_header":
		return formats.ReadClipHeader(str(0))
	case "read_clip_binding_metadata":
		return formats.ReadClipBindingMetadata(str(0))
	case "build_export_hierarchy":
		var asset formats.Asset
		raw, _ := json.Marshal(args[0])
		if err := json.Unmarshal(raw, &asset); err != nil {
			return nil, err
		}
		return formats.BuildExportHierarchy(&asset)
	}
	panic("unknown function " + fn)
}

// checkFastJSON verifies AppendAssetJSON/AppendClipJSON against json.Marshal
// with exact number comparison.
func checkFastJSON(t *testing.T, label string, value any, encoded []byte) {
	t.Helper()
	var fast []byte
	var err error
	switch v := value.(type) {
	case *formats.Asset:
		fast, err = formats.AppendAssetJSON(nil, v)
	case *formats.Clip:
		fast, err = formats.AppendClipJSON(nil, v)
	default:
		return
	}
	if err != nil || !json.Valid(fast) {
		t.Fatalf("%s: fast JSON invalid: %v\n%.300s", label, err, fast)
	}
	diffs, err := jsoncmp.Diff(encoded, fast, jsoncmp.Options{})
	if err != nil || len(diffs) > 0 {
		t.Errorf("%s: fast JSON differs from json.Marshal: %v %v", label, err, diffs)
	}
}

func pythonType(err error) string {
	var formatErr *formats.FormatError
	if errors.As(err, &formatErr) {
		return "FormatError"
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "FileNotFoundError"
	}
	return "OSError"
}

// compareWithPython runs every call in one Python process and in Go.
func compareWithPython(t *testing.T, calls []pyCall) {
	t.Helper()
	request := make([][]any, len(calls))
	for i, c := range calls {
		request[i] = []any{c.fn, c.args}
	}
	raw := oracle.Exec(t, neutralPython, request).MustValue(t)
	var results []oracle.Result
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != len(calls) {
		t.Fatalf("oracle returned %d results for %d calls", len(results), len(calls))
	}
	failures := map[string]int{}
	defer func() {
		t.Logf("%d calls compared; Python raised %v", len(calls), failures)
	}()
	for i, c := range calls {
		want := results[i]
		if !want.OK {
			failures[want.Type]++
			if os.Getenv("FORMATS_ORACLE_VERBOSE") != "" {
				t.Logf("%s %s: %s", c.fn, c.label, want.Error)
			}
		}
		got, err := callGo(c.fn, c.args)
		label := c.fn + " " + c.label
		switch {
		case !want.OK && err == nil:
			t.Errorf("%s: Python raised %s: %q, Go succeeded", label, want.Type, want.Error)
		case want.OK && err != nil:
			t.Errorf("%s: Go failed with %q, Python succeeded", label, err)
		case !want.OK:
			if err.Error() != want.Error || pythonType(err) != want.Type {
				t.Errorf("%s: error mismatch\n  python: %s: %q\n  go:     %s: %q", label, want.Type, want.Error, pythonType(err), err)
			}
		default:
			encoded, marshalErr := json.Marshal(got)
			if marshalErr != nil {
				t.Errorf("%s: marshal: %v", label, marshalErr)
				continue
			}
			checkFastJSON(t, label, got, encoded)
			diffs, diffErr := jsoncmp.Diff(want.Value, encoded, jsoncmp.Default)
			if diffErr != nil {
				t.Fatal(diffErr)
			}
			if len(diffs) > 0 {
				t.Errorf("%s: differs from Python:\n  %s", label, strings.Join(diffs, "\n  "))
			}
		}
	}
}

func repoRoot(t testing.TB) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

// assetFiles lists the fixture files under tests/<folder> by extension.
func assetFiles(t testing.TB, folder string) (assets, clips []string) {
	root := filepath.Join(repoRoot(t), "tests", folder)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".owmdl", ".owentity":
			assets = append(assets, path)
		case ".owanimclip":
			clips = append(clips, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return assets, clips
}

func clipCalls(path, label string) []pyCall {
	var calls []pyCall
	for _, fn := range []string{"load_clip", "read_clip_header", "read_clip_binding_metadata"} {
		calls = append(calls, pyCall{label, fn, []any{path}})
	}
	return calls
}

func TestOracleFixtureAssets(t *testing.T) {
	var calls []pyCall
	for _, folder := range []string{"demo-assets", "material-assets"} {
		root := filepath.Join(repoRoot(t), "tests", folder)
		assets, clips := assetFiles(t, folder)
		if len(assets) == 0 || len(clips) == 0 {
			t.Fatalf("no fixtures in %s", root)
		}
		for _, path := range assets {
			calls = append(calls,
				pyCall{path, "load_asset", []any{path, nil}},
				pyCall{path + " (root)", "load_asset", []any{path, root}})
		}
		for _, path := range clips {
			calls = append(calls, clipCalls(path, path)...)
		}
	}
	compareWithPython(t, calls)
}

func TestOracleBuildExportHierarchy(t *testing.T) {
	bone := func(name string, parent int) map[string]any {
		return map[string]any{"name": name, "parent": parent, "pos": []float64{0, 1, 2}, "rot": []float64{0, 0, math.Sqrt(.5), math.Sqrt(.5)}}
	}
	metadataFree := map[string]any{"name": "Scene", "models": []any{
		map[string]any{"id": "a", "name": "A", "path": "/x/Body.owmdl",
			"bones": []any{bone("bone_0001", -1), bone("bone_0002", 0)}, "meshes": []any{map[string]any{"name": "m0"}}},
		map[string]any{"id": "c", "name": "C", "parentId": "b", "isEntity": false, "attachmentBone": "missing"},
		map[string]any{"id": "b", "name": "B", "parentId": "a", "attachmentBone": "bone_0002", "entityName": "Named",
			"hasModel": false},
		map[string]any{"id": "d", "name": "D", "parentId": "a", "isEntity": false, "modelGuid": "G",
			"bones": []any{bone("bone_0011", -1)}},
	}}
	broken := map[string]any{"name": "Scene", "models": []any{
		map[string]any{"id": "a", "name": "A", "parentId": "z"},
	}}
	compareWithPython(t, []pyCall{
		{"metadata-free", "build_export_hierarchy", []any{metadataFree}},
		{"missing parent", "build_export_hierarchy", []any{broken}},
		{"empty", "build_export_hierarchy", []any{map[string]any{"models": []any{}}}},
	})
}

// synthetic builds a fixture tree and the calls exercising it.
type synthetic struct {
	t     *testing.T
	root  string
	calls []pyCall
}

func (s *synthetic) file(name string, data []byte) string {
	return ft.Write(s.t, s.root, name, data)
}

func (s *synthetic) asset(name string, data []byte, root bool) {
	path := s.file(name, data)
	args := []any{path, nil}
	if root {
		args[1] = s.root
	}
	s.calls = append(s.calls, pyCall{name, "load_asset", args})
}

func (s *synthetic) clip(name string, data []byte) {
	s.calls = append(s.calls, clipCalls(s.file(name, data), name)...)
}

func modelWith(change func(*ft.Model)) []byte {
	m := ft.NewModel()
	change(&m)
	return m.Bytes()
}

func clipWith(change func(*ft.Clip)) []byte {
	c := ft.NewClip()
	change(&c)
	return c.Bytes()
}

func entityWith(change func(*ft.Entity)) []byte {
	e := ft.NewEntity()
	change(&e)
	return e.Bytes()
}

func TestOracleSyntheticModels(t *testing.T) {
	s := &synthetic{t: t, root: pypath.Resolve(t.TempDir())}
	model := ft.NewModel().Bytes()
	for n := 0; n < len(model); n++ { // every truncation point
		s.asset(fmt.Sprintf("trunc/m%04d.owmdl", n), model[:n], false)
	}
	s.asset("ok.owmdl", model, false)
	s.asset("trailing.owmdl", append(append([]byte{}, model...), 1, 2, 3, 4, 5), false)
	s.asset("v3.owmdl", modelWith(func(m *ft.Model) { m.Version = [2]uint16{3, 1} }), false)
	s.asset("v2_7.owmdl", modelWith(func(m *ft.Model) { m.Version = [2]uint16{2, 7} }), false)
	s.asset("long-name.owmdl", modelWith(func(m *ft.Model) { m.Name = strings.Repeat("ü", 300) }), false)
	s.asset("unnamed.owmdl", modelWith(func(m *ft.Model) { m.Name = "null" }), false)
	s.asset("bad-parent.owmdl", modelWith(func(m *ft.Model) { m.Bones[1].Parent = 5 }), false)
	s.asset("self-parent.owmdl", modelWith(func(m *ft.Model) { m.Bones[1].Parent = 1 }), false)
	s.asset("dup-bone.owmdl", modelWith(func(m *ft.Model) { m.Bones[1].Name = "bone_0001" }), false)
	s.asset("null-bone.owmdl", modelWith(func(m *ft.Model) { m.Bones[1].Name = "null" }), false)
	s.asset("quote-bone.owmdl", modelWith(func(m *ft.Model) { m.Bones[1].Name = "it's"; m.Bones[0].Name = "it's" }), false)
	s.asset("cycle.owmdl", modelWith(func(m *ft.Model) {
		m.Bones = []ft.Bone{{Name: "a", Parent: 2}, {Name: "b", Parent: 0}, {Name: "c", Parent: 1}}
		m.Mesh = false
	}), false)
	s.asset("no-bones.owmdl", modelWith(func(m *ft.Model) { m.Bones = nil }), false)
	s.asset("empty.owmdl", modelWith(func(m *ft.Model) { m.Bones = nil; m.Mesh = false }), false)
	s.asset("slots0.owmdl", modelWith(func(m *ft.Model) { m.Slots = 0 }), false)
	s.asset("slots3.owmdl", modelWith(func(m *ft.Model) { m.Slots = 3 }), false)
	s.asset("slots33.owmdl", modelWith(func(m *ft.Model) { m.Slots = 33 }), false)
	s.asset("euler.owmdl", modelWith(func(m *ft.Model) {
		m.Bones = []ft.Bone{{Name: "r", Parent: -1, Position: [3]float64{1, 2, 3}, Rotation: [3]float64{0.3, -1.2, 2.9}},
			{Name: "c", Parent: 0, Position: [3]float64{-0.5, 0.25, 4}, Rotation: [3]float64{-2.5, 0.7, 0.1}},
			{Name: "d", Parent: 1, Position: [3]float64{0.1, 0.2, 0.3}, Rotation: [3]float64{1, 1, 1}}}
		m.Sockets = []ft.Socket{
			{Name: "s1", Bone: "d", Position: [3]float64{1, 1, 1}, Rotation: [4]float64{0.1, 0.2, 0.3, 0.9}},
			{Name: "s2", Bone: "unknown", Position: [3]float64{1, 2, 1}, Rotation: [4]float64{0, 0, 0, 0}},
			{Name: "s3", Bone: "bone_0000", Position: [3]float64{1, 2, 3}, Rotation: [4]float64{0, 1, 0, 1}}}
	}), false)
	// Hand-built variants of the mesh block.
	mesh := func(name string, uv, vertices, faces, slots int, body []byte) []byte {
		blob := ft.Pack("HH", 2, 0)
		blob = append(blob, ft.String("")...)
		blob = append(blob, ft.String("Hand")...)
		blob = append(blob, ft.Pack("IHII", 9, 1, 1, 0)...)
		blob = append(blob, ft.String("bone_0001")...)
		blob = append(blob, ft.Pack("h9f", -1, 0, 0, 0, 1, 1, 1, 0, 0, 0)...)
		blob = append(blob, ft.String(name)...)
		blob = append(blob, ft.Pack("QBIIB", uint64(1)<<63+5, uv, vertices, faces, slots)...)
		return append(blob, body...)
	}
	vertexBlock := func(uv, slots int, weights []float64, nonFinite bool, face uint32) []byte {
		var blob []byte
		x := 1.0
		if nonFinite {
			x = math.NaN()
		}
		blob = append(blob, ft.Pack("9f", 0, 0, 0, x, 0, 0, 0, 0, 1)...)
		blob = append(blob, ft.Pack("9f", 0, -1, 0, 0, -1, 0, 0, -1, 0)...)
		blob = append(blob, make([]byte, 3*16)...)
		for i := 0; i < uv; i++ {
			blob = append(blob, ft.Pack("6f", 0.25*float64(i), 0, 1, 0.5, 0, 1)...)
		}
		for i := 0; i < 3*slots; i++ {
			blob = append(blob, ft.Pack("H", i%3)...)
		}
		for i := 0; i < 3*slots; i++ {
			blob = append(blob, ft.Pack("f", weights[i%len(weights)])...)
		}
		blob = append(blob, make([]byte, 3*32)...)
		return append(blob, ft.Pack("III", 0, 1, face)...)
	}
	s.asset("uv3.owmdl", mesh("", 3, 3, 1, 2, vertexBlock(3, 2, []float64{0.5, 0.5}, false, 2)), false)
	s.asset("uv0.owmdl", mesh("NoUV", 0, 3, 1, 1, vertexBlock(0, 1, []float64{1}, false, 2)), false)
	s.asset("orphans.owmdl", mesh("Orphan", 1, 3, 1, 2, vertexBlock(1, 2, []float64{0, 0}, false, 2)), false)
	s.asset("nan.owmdl", mesh("Nan", 1, 3, 1, 1, vertexBlock(1, 1, []float64{1}, true, 2)), false)
	s.asset("face.owmdl", mesh("Bad 'face'", 1, 3, 1, 1, vertexBlock(1, 1, []float64{1}, false, 3)), false)
	s.asset("huge.owmdl", mesh("Huge", 1, 2_000_001, 0, 1, nil), false)
	s.asset("faces.owmdl", mesh("Faces", 1, 3, 4_000_001, 1, nil), false)
	s.asset("uv17.owmdl", mesh("UV", 17, 3, 1, 1, nil), false)
	s.asset("short-mesh.owmdl", mesh("Short", 1, 1000, 10, 4, vertexBlock(1, 4, []float64{1}, false, 2)), false)
	// Strings: unterminated length, oversize, invalid UTF-8.
	header := ft.Pack("HH", 2, 0)
	s.asset("strlen.owmdl", append(append([]byte{}, header...), 0x80, 0x80, 0x80, 0x80, 0x80, 0), false)
	s.asset("strbig.owmdl", append(append([]byte{}, header...), 0x81, 0x80, 0x41), false)
	s.asset("utf8.owmdl", append(append([]byte{}, header...), 2, 0xc3, 0x28), false)
	s.asset("counts.owmdl", append(append(append([]byte{}, header...), 0, 0), ft.Pack("IHII", 1, 0, 16385, 0)...), false)
	s.asset("sockets.owmdl", append(append(append([]byte{}, header...), 0, 0), ft.Pack("IHII", 1, 0, 0, 65537)...), false)
	// Path errors.
	s.calls = append(s.calls,
		pyCall{"missing", "load_asset", []any{filepath.Join(s.root, "missing.owmdl"), nil}},
		pyCall{"extension", "load_asset", []any{filepath.Join(s.root, "ok.txt"), nil}},
		pyCall{"directory", "load_asset", []any{s.file("dir.owmdl/x", nil) + "/..", nil}})
	compareWithPython(t, s.calls)
}

func TestOracleSyntheticEntities(t *testing.T) {
	s := &synthetic{t: t, root: pypath.Resolve(t.TempDir())}
	s.file("Models/Body.00C/Body.owmdl", modelWith(func(m *ft.Model) {
		m.Sockets = []ft.Socket{{Name: "hardpoint_0001", Bone: "bone_0002", Position: [3]float64{1, 2, 0},
			Rotation: [4]float64{0, 0, math.Sqrt(.5), math.Sqrt(.5)}}}
	}))
	s.file("Models/Weapon.00C/Weapon.owmdl", modelWith(func(m *ft.Model) { m.Name = "Weapon"; m.Slots = 0 }))
	s.file("Models/Broken.00C/Broken.owmdl", ft.NewModel().Bytes()[:40])
	s.file("Entities/Weapon/Weapon.owentity", entityWith(func(e *ft.Entity) { e.Name = "Weapon"; e.Model = "Weapon.00C" }))
	s.file("Entities/Broken/Broken.owentity", entityWith(func(e *ft.Entity) { e.Name = "Broken"; e.Model = "Broken.00C" }))
	s.file("Entities/Empty/Empty.owentity", entityWith(func(e *ft.Entity) { e.Name = ""; e.Model = "null" }))
	s.file("Entities/Loop/Loop.owentity", entityWith(func(e *ft.Entity) {
		e.Model = "Weapon.00C"
		e.Children = []ft.Child{{Reference: "Loop", Attachment: ""}}
	}))
	entity := entityWith(func(e *ft.Entity) {
		e.Children = []ft.Child{
			{Reference: "Weapon", Attachment: "hardpoint_0001"},
			{Reference: "Weapon", Attachment: "bone_0001"},
			{Reference: "Weapon", Attachment: "nowhere"},
			{Reference: "Missing", Attachment: ""},
			{Reference: "", Attachment: "x"},
			{Reference: "Broken", Attachment: ""},
			{Reference: "Empty", Attachment: ""},
			{Reference: "Loop", Attachment: ""},
			{Reference: `..\Weapon\Weapon.owentity`, Attachment: ""},
		}
	})
	for n := 0; n < len(entity); n++ {
		s.asset(fmt.Sprintf("Entities/Trunc%03d/Trunc%03d.owentity", n, n), entity[:n], false)
	}
	s.asset("Entities/Hero/Hero.owentity", entity, false)
	s.asset("Entities/Hero/HeroRoot.owentity", entity, true)
	s.asset("Entities/Hero20/Hero20.owentity", entityWith(func(e *ft.Entity) { e.Minor = 0 }), false)
	s.asset("Entities/Hero22/Hero22.owentity", entityWith(func(e *ft.Entity) { e.Minor = 2 }), false)
	s.asset("Entities/NoModel/NoModel.owentity", entityWith(func(e *ft.Entity) { e.Model = "Nope.00C" }), false)
	s.asset("Entities/OnlyEmpty/OnlyEmpty.owentity", entityWith(func(e *ft.Entity) {
		e.Model = ""
		e.Children = []ft.Child{{Reference: "Weapon", Attachment: ""}}
	}), false)
	s.asset("Entities/Bad/Bad.owentity", append(ft.String("owentitx"), ft.Pack("HH", 2, 1)...), false)
	s.asset("Entities/Neg/Neg.owentity", append(append(append(ft.String("owentity"), ft.Pack("HH", 2, 1)...),
		append(ft.String("a"), append(ft.String("b"), ft.String("c")...)...)...), ft.Pack("IIIi", 0, 0, 0, -1)...), false)
	s.asset("Copied/Hero.owentity", entityWith(func(e *ft.Entity) { e.Relative = `..\..\missing` }), true)
	// Ambiguous names under the selected root.
	s.file("Copies/A/Models/Dup.owmdl", ft.NewModel().Bytes())
	s.file("Copies/B/Models/Dup.owmdl", ft.NewModel().Bytes())
	s.asset("Elsewhere/Amb.owentity", entityWith(func(e *ft.Entity) { e.Model = "Dup"; e.Relative = "none" }), true)
	// Depth limit: a chain of 50 entities.
	for i := 0; i < 50; i++ {
		child := []ft.Child{{Reference: fmt.Sprintf("Chain%02d", i+1)}}
		if i == 49 {
			child = nil
		}
		data := entityWith(func(e *ft.Entity) { e.Model = "Weapon.00C"; e.Children = child })
		path := fmt.Sprintf("Entities/Chain%02d/Chain%02d.owentity", i, i)
		if i == 0 {
			s.asset(path, data, false)
		} else {
			s.file(path, data)
		}
	}
	// Model-count limit: 600 children of the same entity.
	s.asset("Entities/Crowd/Crowd.owentity", entityWith(func(e *ft.Entity) {
		e.Model = "Weapon.00C"
		for i := 0; i < 600; i++ {
			e.Children = append(e.Children, ft.Child{Reference: "Weapon"})
		}
	}), false)
	compareWithPython(t, s.calls)
}

func TestOracleSyntheticClips(t *testing.T) {
	s := &synthetic{t: t, root: pypath.Resolve(t.TempDir())}
	clip := ft.NewClip().Bytes()
	for n := 0; n < len(clip); n++ {
		s.clip(fmt.Sprintf("trunc/c%03d.owanimclip", n), clip[:n])
	}
	s.clip("ok.owanimclip", clip)
	s.clip("trailing.owanimclip", append(append([]byte{}, clip...), 9, 9))
	for name, fps := range map[string]float64{"zero": 0, "nan": math.NaN(), "inf": math.Inf(1), "big": 1500, "neg": -2.5, "tiny": 1e-30, "odd": 23.976} {
		s.clip("fps-"+name+".owanimclip", clipWith(func(c *ft.Clip) { c.FPS = fps }))
	}
	s.clip("v3.owanimclip", clipWith(func(c *ft.Clip) { c.Version = [2]uint16{3, 0} }))
	s.clip("duration0.owanimclip", clipWith(func(c *ft.Clip) { c.Duration = 0 }))
	s.clip("duration-big.owanimclip", clipWith(func(c *ft.Clip) { c.Duration = 100_000_001 }))
	s.clip("bones-big.owanimclip", ft.Pack("HHIfI", 2, 0, 65537, 30, 2))
	s.clip("dup-bone.owanimclip", clipWith(func(c *ft.Clip) { c.Bones = append(c.Bones, c.Bones[0]) }))
	s.clip("empty-bone.owanimclip", clipWith(func(c *ft.Clip) { c.Bones[0].Name = "null" }))
	s.clip("no-tracks.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones = append(c.Bones, ft.ClipBone{Name: "bare"})
	}))
	s.clip("dup-track.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones[0].Tracks = append(c.Bones[0].Tracks, c.Bones[0].Tracks[2])
	}))
	s.clip("components.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones[0].Tracks[2].Keys = []ft.Key{ft.K(0, 1, 1, 1, 1)}
	}))
	s.clip("components17.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones[0].Tracks = []ft.Track{{Kind: 7, Keys: []ft.Key{ft.K(0, make([]float64, 17)...)}}}
	}))
	s.clip("unknown.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones[0].Tracks = append(c.Bones[0].Tracks, ft.Track{Kind: 9, Keys: []ft.Key{ft.K(3, 1, 2)}}, ft.Track{Kind: 9})
	}))
	s.clip("frames.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones[0].Tracks[0].Keys = []ft.Key{ft.K(9, 1, 1, 1), ft.K(2, 2, 2, 2), ft.K(9, 3, 3, 3), ft.K(5, 4, 4, 4), ft.K(2, 5, 5, 5)}
		c.Bones[0].Tracks[1].Keys = []ft.Key{ft.K(4, 0, 0, 0, 0), ft.K(1, 1, 2, 3, 4), ft.K(70, -1, 0, 0, 0)}
	}))
	s.clip("frame-big.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones[0].Tracks[0].Keys = []ft.Key{ft.K(100_000_001, 1, 1, 1)}
	}))
	s.clip("inf.owanimclip", clipWith(func(c *ft.Clip) {
		c.Bones[0].Tracks[1].Keys = []ft.Key{ft.K(0, 0, math.Inf(-1), 0, 1)}
	}))
	s.clip("tracks17.owanimclip", clipWith(func(c *ft.Clip) {
		for len(c.Bones[0].Tracks) < 17 {
			c.Bones[0].Tracks = append(c.Bones[0].Tracks, ft.Track{Kind: 5})
		}
	}))
	s.clip("keys.owanimclip", append(ft.Pack("HHIfI", 2, 0, 1, 30, 2),
		append(ft.String("b"), ft.Pack("IIII", 1, 0, 10_000_001, 3)...)...))
	s.clip("keys-short.owanimclip", append(ft.Pack("HHIfI", 2, 0, 1, 30, 2),
		append(ft.String("b"), ft.Pack("IIII", 1, 0, 1000, 3)...)...))
	s.calls = append(s.calls,
		pyCall{"missing", "load_clip", []any{filepath.Join(s.root, "missing.owanimclip")}},
		pyCall{"missing", "read_clip_header", []any{filepath.Join(s.root, "missing.owanimclip")}},
		pyCall{"extension", "read_clip_binding_metadata", []any{filepath.Join(s.root, "ok.OWANIMCLIP.bak")}},
		pyCall{"upper", "load_clip", []any{s.file("UPPER.OWANIMCLIP", clip)}},
		pyCall{"relative", "load_clip", []any{relativeTo(t, s.root, "ok.owanimclip")}})
	compareWithPython(t, s.calls)
}

// relativeTo returns a path to root/name relative to the working directory,
// exercising Path.resolve() of relative input.
func relativeTo(t *testing.T, root, name string) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(wd, filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return relative
}

func TestOracleSyntheticLargeModel(t *testing.T) {
	if testing.Short() {
		t.Skip("large fixture")
	}
	s := &synthetic{t: t, root: pypath.Resolve(t.TempDir())}
	s.asset("Large.owmdl", ft.LargeModel(70_000, 40), false)
	s.clip("Large.owanimclip", ft.LargeClip(40, 120))
	compareWithPython(t, s.calls)
}
