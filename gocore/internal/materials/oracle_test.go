package materials

// Oracle tests: run owpreview.materials (and formats.load_asset, which now
// uses it) on the same inputs and compare JSON results, warnings and errors.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/pypath"
)

const oraclePython = `
from pathlib import Path
from owpreview import formats, materials

def run(call):
    kind = call[0]
    if kind == 'load_asset':
        return formats.load_asset(call[1], call[2])
    if kind == 'read_owmat':
        return materials.read_owmat(call[1])
    if kind == 'descriptor':
        warnings = []
        resolver = materials.MaterialResolver(call[1], call[2], warnings)
        results = []
        for path in call[3]:
            try:
                results.append({'ok': True, 'result': resolver.descriptor(path)})
            except Exception as error:
                results.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
        return {'results': results, 'textures': resolver.textures, 'warnings': warnings, 'root': resolver.root}
    if kind == 'resolve':
        warnings = []
        resolver = materials.MaterialResolver(call[1], call[2], warnings)
        results = [resolver.resolve(owner, ref, file_relative=rel, extension=ext) for owner, ref, rel, ext in call[3]]
        return {'results': [str(r) if r else None for r in results], 'warnings': warnings}
    if kind == 'for_model':
        warnings = []
        resolver = materials.MaterialResolver(call[1], call[2], warnings)
        out = []
        for model, look, partial in call[3]:
            result = resolver.for_model(model, look, partial=partial)
            out.append({'result': result, 'materialLook': model.get('materialLook')})
        return {'results': out, 'textures': resolver.textures, 'warnings': warnings}
    raise ValueError(kind)

out = []
for call in args[0]:
    try:
        out.append({'ok': True, 'result': run(call)})
    except Exception as error:
        out.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
result = out
`

type call struct {
	label string
	args  []any
}

func optional(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// sameType accepts Python's OSError subclasses (IsADirectoryError and so on)
// for a Go OSError; their messages are still compared exactly.
func sameType(goType, pyType string) bool {
	return goType == pyType || (goType == "OSError" && strings.HasSuffix(pyType, "Error") &&
		pyType != "FormatError" && pyType != "ValueError")
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

// recordJSON is read_owmat's dict as the oracle prints it.
func recordJSON(record *Record) any {
	if record.Kind == "look" {
		entries := make([][2]string, len(record.Entries))
		for i, entry := range record.Entries {
			entries[i] = [2]string{entry.Key, entry.Reference}
		}
		return map[string]any{"kind": record.Kind, "path": record.Path, "entries": entries}
	}
	textures := make([][2]any, len(record.Textures))
	for i, texture := range record.Textures {
		textures[i] = [2]any{texture.Reference, texture.Binding}
	}
	inputs := map[string]any{}
	for _, key := range record.InputKeys {
		inputs[strconv.FormatUint(uint64(key), 10)] = map[string]string{"__bytes__": base64.StdEncoding.EncodeToString(record.Inputs[key])}
	}
	return map[string]any{"kind": record.Kind, "path": record.Path, "shader": record.Shader, "textures": textures, "inputs": inputs}
}

type resultJSON struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	Type   string `json:"type,omitempty"`
}

func wrap(value any, err error) resultJSON {
	if err != nil {
		return resultJSON{OK: false, Error: err.Error(), Type: pythonType(err)}
	}
	return resultJSON{OK: true, Result: value}
}

type modelCase struct {
	Model   map[string]any
	Look    string
	Partial bool
}

func runGo(c call) (any, error) {
	args := c.args
	str := func(i int) string {
		if args[i] == nil {
			return ""
		}
		return args[i].(string)
	}
	switch args[0] {
	case "load_asset":
		return formats.LoadAsset(str(1), str(2))
	case "read_owmat":
		record, err := ReadOWMat(str(1))
		if err != nil {
			return nil, err
		}
		return recordJSON(record), nil
	case "descriptor":
		var warnings []string
		resolver := New(str(1), str(2), &warnings)
		var results []resultJSON
		for _, path := range args[3].([]string) {
			results = append(results, wrap(resolver.Descriptor(path)))
		}
		return map[string]any{"results": results, "textures": resolver.Textures(), "warnings": nonNil(warnings), "root": resolver.Root}, nil
	case "resolve":
		var warnings []string
		resolver := New(str(1), str(2), &warnings)
		var results []any
		for _, r := range args[3].([][4]any) {
			extension, _ := r[3].(string)
			results = append(results, optional(resolver.Resolve(r[0].(string), r[1].(string), r[2].(bool), extension)))
		}
		return map[string]any{"results": results, "warnings": nonNil(warnings)}, nil
	case "for_model":
		var warnings []string
		resolver := New(str(1), str(2), &warnings)
		var results []any
		for _, mc := range args[3].([]modelCase) {
			raw, _ := json.Marshal(mc.Model)
			var model formats.Model
			if err := json.Unmarshal(raw, &model); err != nil {
				return nil, err
			}
			result := resolver.ForModel(&model, mc.Look, mc.Partial)
			var look any
			if model.MaterialLook.Value != nil {
				look = *model.MaterialLook.Value
			}
			results = append(results, map[string]any{"result": result, "materialLook": look})
		}
		return map[string]any{"results": results, "textures": resolver.Textures(), "warnings": nonNil(warnings)}, nil
	}
	panic(fmt.Sprint("unknown call ", args[0]))
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// pythonArgs converts Go call arguments to the oracle's JSON form.
func pythonArgs(c call) []any {
	out := append([]any{}, c.args...)
	switch c.args[0] {
	case "resolve":
		var rows []any
		for _, r := range c.args[3].([][4]any) {
			rows = append(rows, []any{r[0], r[1], r[2], r[3]})
		}
		out[3] = rows
	case "for_model":
		var rows []any
		for _, mc := range c.args[3].([]modelCase) {
			rows = append(rows, []any{mc.Model, mc.Look, mc.Partial})
		}
		out[3] = rows
	}
	return out
}

func compareWithPython(t *testing.T, calls []call) {
	t.Helper()
	request := make([]any, len(calls))
	for i, c := range calls {
		request[i] = pythonArgs(c)
	}
	raw := oracle.Exec(t, oraclePython, request).MustValue(t)
	var results []oracle.Result
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != len(calls) {
		t.Fatalf("oracle returned %d results for %d calls", len(results), len(calls))
	}
	raised, compared := 0, 0
	for i, c := range calls {
		want := results[i]
		got, err := runGo(c)
		label := fmt.Sprint(c.args[0], " ", c.label)
		switch {
		case !want.OK && err == nil:
			t.Errorf("%s: Python raised %s: %q, Go succeeded", label, want.Type, want.Error)
		case want.OK && err != nil:
			t.Errorf("%s: Go failed with %q, Python succeeded", label, err)
		case !want.OK:
			raised++
			if err.Error() != want.Error || !sameType(pythonType(err), want.Type) {
				t.Errorf("%s: error mismatch\n  python: %s: %q\n  go:     %s: %q", label, want.Type, want.Error, pythonType(err), err)
			}
		default:
			encoded, marshalErr := json.Marshal(got)
			compared += len(encoded)
			if marshalErr != nil {
				t.Fatalf("%s: marshal: %v", label, marshalErr)
			}
			diffs, diffErr := jsoncmp.Diff(want.Value, encoded, jsoncmp.Options{RelTol: 1e-6, AbsTol: 1e-6, MaxDiffs: 10})
			if diffErr != nil {
				t.Fatal(diffErr)
			}
			if len(diffs) > 0 {
				t.Errorf("%s: differs from Python:\n  %s", label, strings.Join(diffs, "\n  "))
			}
			if asset, ok := got.(*formats.Asset); ok {
				fast, err := formats.AppendAssetJSON(nil, asset)
				if err != nil {
					t.Fatal(err)
				}
				if diffs, _ := jsoncmp.Diff(encoded, fast, jsoncmp.Options{}); len(diffs) > 0 {
					t.Errorf("%s: fast JSON differs: %v", label, diffs)
				}
			}
		}
	}
	t.Logf("%d calls compared (%d JSON bytes), %d raised in both", len(calls), compared, raised)
}

func TestOracleMaterialAssets(t *testing.T) {
	root := filepath.Join(repoRoot(), "tests", "material-assets")
	var calls []call
	for _, folder := range []string{"material-assets", "demo-assets"} {
		base := filepath.Join(repoRoot(), "tests", folder)
		filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".owmdl", ".owentity":
				calls = append(calls, call{path, []any{"load_asset", path, nil}}, call{path + " (root)", []any{"load_asset", path, base}})
			case ".owmat":
				calls = append(calls, call{path, []any{"read_owmat", path}})
			}
			return nil
		})
	}
	var materialsPaths []string
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if strings.HasSuffix(path, ".owmat") {
			materialsPaths = append(materialsPaths, path)
		}
		return err
	})
	calls = append(calls, call{"fixture materials", []any{"descriptor", filepath.Join(root, "Models/MaterialRobot.00C/MaterialRobot.owmdl"), nil, materialsPaths}})
	if len(calls) < 10 {
		t.Fatalf("only %d fixture calls", len(calls))
	}
	compareWithPython(t, calls)
}

func TestOracleReadOWMat(t *testing.T) {
	dir := pypath.Resolve(t.TempDir())
	var calls []call
	add := func(name string, data []byte) {
		calls = append(calls, call{name, []any{"read_owmat", ft.Write(t, dir, name, data)}})
	}
	material := ft.MaterialBytes([]ft.TextureRef{{"a.png", 2903569922}, {strings.Repeat("ü", 100) + ".png", 1}},
		[]ft.StaticInput{{3, []byte("test")}, {7, nil}, {3, []byte("again")}}, 51)
	look := ft.LookBytes(ft.LookEntry{Key: math.MaxUint64, Reference: "../x.owmat"}, ft.LookEntry{Key: 2, Reference: "null"})
	for n := 0; n <= len(material); n++ {
		add(fmt.Sprintf("m%03d.owmat", n), material[:n])
	}
	for n := 0; n <= len(look); n++ {
		add(fmt.Sprintf("l%03d.owmat", n), look[:n])
	}
	add("look-null.owmat", append(ft.LookBytes(), ft.Pack("Q", 0)...))
	add("look-null1.owmat", append(ft.LookBytes(), ft.Pack("Q", 1)...))
	add("look-trailing.owmat", append(look, 1))
	add("look-trailing9.owmat", append(ft.LookBytes(), make([]byte, 9)...))
	add("look-many.owmat", ft.Pack("HHIQ", 3, 0, 1, uint64(1)<<63))
	add("look-16385.owmat", ft.Pack("HHIQ", 3, 0, 1, 16385))
	add("v2.owmat", ft.Pack("HHI", 2, 0, 0))
	add("v31.owmat", ft.Pack("HHI", 3, 1, 0))
	add("kind2.owmat", ft.Pack("HHI", 3, 0, 2))
	add("textures.owmat", ft.Pack("HHIQQI", 3, 0, 0, 5000, 0, 0))
	add("textures-huge.owmat", ft.Pack("HHIQQI", 3, 0, 0, uint64(math.MaxUint64), 0, 0))
	add("statics.owmat", ft.Pack("HHIQQI", 3, 0, 0, 0, 65537, 0))
	add("trailing.owmat", append(material, "extra"...))
	add("input-big.owmat", append(ft.Pack("HHIQQI", 3, 0, 0, 0, 1, 0), ft.Pack("II", 123, 1<<30)...))
	add("bad-utf8.owmat", append(ft.Pack("HHIQQI", 3, 0, 0, 1, 0, 0), 2, 0xc3, 0x28))
	calls = append(calls,
		call{"missing", []any{"read_owmat", filepath.Join(dir, "missing.owmat")}},
		call{"directory", []any{"read_owmat", dir}})
	compareWithPython(t, calls)
}

func TestOracleDescriptors(t *testing.T) {
	dir := pypath.Resolve(t.TempDir())
	write := func(name string, data []byte) string { return ft.Write(t, dir, name, data) }
	png := ft.PNGBytes(2, 2, func(x, y int) [4]byte { return [4]byte{1, 2, 3, 4} })
	write("image.png", png)
	write("Textures/other.png", png)
	write("Textures/fallback.dds", []byte("dds"))
	write("Textures/note.txt", []byte("text"))
	write("Copy1/dup.png", png)
	write("Copy2/dup.png", png)
	write("Moved/unique.tga", png)
	write("Moved/look.owmat", ft.LookBytes())
	other := ft.Write(t, t.TempDir(), "outside.png", png)
	all := []uint32{2903569922, 3093211343, 67369114, 1716930793, 1239794147, 3411049489, 378934698, 229655782, 562391268,
		2762290483, 548341454, 2018946191, 763550166, 1239777102, 3111105361, 3166598269, 1523270506, 1493127177,
		1482859648, 1140682086, 3335614873, 3761386704, 3989656707, 1117188170}
	inputs := [][]ft.StaticInput{
		nil,
		{{3250491852, ft.Pack("f", 3)}, {2446772623, ft.Pack("f", 20000)}},
		{{3250491852, []byte{0}}, {2446772623, ft.Pack("f", math.NaN())}, {565460110, ft.Pack("f", .5)}},
		{{3344068240, ft.Pack("I", 2)}, {1883253226, ft.Pack("ff", 2, 3)}},
		{{3344068240, ft.Pack("I", 0)}, {1883253226, ft.Pack("ff", 20000, 1)}},
		{{3344068240, ft.Pack("I", 9)}, {1883253226, []byte{1, 2}}, {4081294361, nil}},
		{{1883253226, ft.Pack("ff", math.Inf(1), 1)}, {2446772623, ft.Pack("f", -5)}},
	}
	shaders := []uint32{0, 11, 34, 36, 37, 38, 43, 44, 50, 51, 54, 56, 167, 217, 221}
	var paths []string
	n := 0
	for _, shader := range shaders {
		for i, input := range inputs {
			// Every binding alone, then rotating combinations.
			var bindings [][]uint32
			if i == 0 {
				for _, key := range all {
					bindings = append(bindings, []uint32{key})
				}
			}
			bindings = append(bindings, all, []uint32{all[(n*7)%len(all)], all[(n*3+5)%len(all)], 4101268840, 758934576, 123},
				[]uint32{1239794147, 4101268840, 758934576}, []uint32{67369114, 1493127177, 3166598269})
			for _, keys := range bindings {
				var textures []ft.TextureRef
				for j, key := range keys {
					reference := []string{"image.png", "Textures/other.png", "../Textures/fallback.png", "Textures/note.txt",
						"dup.png", "missing.png", other, "old/unique.tga", "Textures\\other.png", "", "bad\x00.png"}[(j+n)%11]
					if j == 0 {
						reference = "image.png"
					}
					textures = append(textures, ft.TextureRef{Reference: reference, Binding: key})
				}
				paths = append(paths, write(fmt.Sprintf("M/%04d.owmat", n), ft.MaterialBytes(textures, input, shader)))
				n++
			}
		}
	}
	paths = append(paths, paths[3], filepath.Join(dir, "Moved/look.owmat"), filepath.Join(dir, "M/missing.owmat"),
		write("M/bad.owmat", []byte{3, 0}), other)
	calls := []call{{"synthetic", []any{"descriptor", filepath.Join(dir, "model.owmdl"), dir, paths}}}
	calls = append(calls, call{"default root", []any{"descriptor", filepath.Join(dir, "M/Models/x.owmdl"), nil, paths[:40]}})
	compareWithPython(t, calls)
}

func TestOracleResolveAndForModel(t *testing.T) {
	dir := pypath.Resolve(t.TempDir())
	write := func(name string, data []byte) string { return ft.Write(t, dir, name, data) }
	png := ft.PNGBytes(1, 1, func(x, y int) [4]byte { return [4]byte{} })
	write("Models/A/Textures/123.png", png)
	write("Models/A/Materials/a.owmat", ft.MaterialBytes([]ft.TextureRef{{"../Textures/123.png", 2903569922}}, nil, 0))
	write("Models/A/Materials/b.owmat", ft.MaterialBytes(nil, nil, 34))
	write("Models/A/Materials/bad.owmat", []byte("bad"))
	write("Models/A/ModelLooks/default.owmat", ft.LookBytes(
		ft.LookEntry{Key: 1, Reference: "../../Materials/a.owmat"}, ft.LookEntry{Key: 2, Reference: "../../Materials/b.owmat"},
		ft.LookEntry{Key: 2, Reference: "null"}, ft.LookEntry{Key: 3, Reference: "null"}, ft.LookEntry{Key: 3, Reference: "../../Materials/a.owmat"},
		ft.LookEntry{Key: 4, Reference: "../../Materials/bad.owmat"}, ft.LookEntry{Key: 5, Reference: "../../Materials/missing.owmat"},
		ft.LookEntry{Key: 9, Reference: "../../Materials/a.owmat"}, ft.LookEntry{Key: 6, Reference: "../../ModelLooks/default.owmat"}))
	write("Models/A/ModelLooks/Skin.owmat", ft.LookBytes(ft.LookEntry{Key: 1, Reference: "../../Materials/b.owmat"}))
	write("Models/A/ModelLooks/broken.owmat", []byte("xx"))
	write("Models/A/ModelLooks/material.owmat", ft.MaterialBytes(nil, nil, 0))
	write("Elsewhere/123.png", png)
	write("Dup/1/same.png", png)
	write("Dup/2/same.png", png)
	write("Dds/tex.dds", []byte("dds"))
	model := filepath.Join(dir, "Models/A/A.owmdl")
	resolveRows := [][4]any{
		{filepath.Join(dir, "Models/A/ModelLooks/default.owmat"), "../../Materials/a.owmat", true, ".owmat"},
		{filepath.Join(dir, "Models/A/ModelLooks/default.owmat"), "../../Materials/a.owmat", false, ".owmat"},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), `..\Textures\123.png`, false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "old/123.png", false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "same.png", false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "Dds/tex.png", false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "Dds/tex.tif", false, ".png"},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "../../../../escape.png", false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "/etc/passwd", false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "nul\x00l.png", false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "", false, nil},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "Models/A/Textures/123.png", false, ".owmat"},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "123.png", false, ".owmat"},
		{filepath.Join(dir, "Models/A/Materials/a.owmat"), "a.owmat", false, ".owmat"},
	}
	mesh := func(key string) map[string]any { return map[string]any{"name": "m", "materialKey": key} }
	modelDict := func(materialPath any, keys ...string) map[string]any {
		var meshes []any
		for _, key := range keys {
			meshes = append(meshes, mesh(key))
		}
		m := map[string]any{"name": "A", "path": model, "bones": []any{}, "sockets": []any{}, "meshes": meshes}
		if materialPath != nil {
			m["materialPath"] = materialPath
		}
		return m
	}
	keys := []string{"1", "2", "3", "4", "5", "6", "7", "1"}
	cases := []modelCase{
		{modelDict("ModelLooks/default.owmat", keys...), "", false},
		{modelDict("ModelLooks/default.owmat", keys...), "", true},
		{modelDict(nil, keys...), "", false},
		{modelDict("", keys...), "", false},
		{modelDict("ModelLooks/default.owmat", keys...), "Skin.01A", false},
		{modelDict("ModelLooks/default.owmat", keys...), `Looks\Skin.OWMAT`, false},
		{modelDict("ModelLooks/default.owmat", keys...), "Missing", false},
		{modelDict("ModelLooks/broken.owmat", keys...), "", false},
		{modelDict("ModelLooks/material.owmat", keys...), "", false},
		{modelDict(filepath.Join(dir, "Models/A/ModelLooks/default.owmat"), "1", "2"), "", true},
		{modelDict("../../../outside.owmat", "1"), "", false},
	}
	compareWithPython(t, []call{
		{"rows", []any{"resolve", model, dir, resolveRows}},
		{"rows default root", []any{"resolve", model, nil, resolveRows}},
		{"models", []any{"for_model", model, dir, cases}},
	})

}
