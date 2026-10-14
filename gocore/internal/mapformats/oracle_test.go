package mapformats

// Oracle tests: run owpreview.map_formats on the same inputs and compare
// JSON results (including warning order) and error messages.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/jsoncmp"
	mt "avolition/gocore/internal/mapformats/maptest"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/pypath"
)

const oraclePython = `
import json
from owpreview import map_formats as mf

def limits(value):
    return mf.MapLimits(**value) if value is not None else None

def run(call):
    kind = call[0]
    if kind == 'read_map_header':
        return mf.read_map_header(call[1], limits=limits(call[2]))
    if kind == 'read_map':
        return mf.read_map(call[1], limits=limits(call[2]))
    if kind == 'load_map':
        return mf.load_map(call[1], call[2], compact=call[3], limits=limits(call[4]))
    if kind == 'scan_maps':
        return mf.scan_maps(call[1], **({'max_files': call[2]} if call[2] else {}))
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

func pythonType(err error) string {
	var formatErr *formats.FormatError
	var valueErr *ValueError
	switch {
	case errors.As(err, &formatErr):
		return "FormatError"
	case errors.As(err, &valueErr):
		return "ValueError"
	case errors.Is(err, fs.ErrNotExist):
		return "FileNotFoundError"
	}
	return "OSError"
}

func limitsArg(value any) *Limits {
	if value == nil {
		return nil
	}
	return value.(*Limits)
}

func runGo(c call) (any, error) {
	str := func(i int) string {
		if c.args[i] == nil {
			return ""
		}
		return c.args[i].(string)
	}
	switch c.args[0] {
	case "read_map_header":
		return ReadMapHeader(str(1), limitsArg(c.args[2]))
	case "read_map":
		return ReadMap(str(1), limitsArg(c.args[2]))
	case "load_map":
		return LoadMapWith(str(1), str(2), Options{Expanded: !c.args[3].(bool), Limits: limitsArg(c.args[4])})
	case "scan_maps":
		maxFiles, _ := c.args[2].(int)
		return ScanMaps(str(1), maxFiles)
	}
	panic(fmt.Sprint("unknown call ", c.args[0]))
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if asset, ok := value.(*formats.Asset); ok {
		fast, err := formats.AppendAssetJSON(nil, asset)
		if err != nil || !json.Valid(fast) {
			t.Fatalf("fast JSON: %v", err)
		}
		if diffs, _ := jsoncmp.Diff(encoded, fast, jsoncmp.Options{}); len(diffs) > 0 {
			t.Errorf("fast JSON differs from json.Marshal: %v", diffs)
		}
	}
	return encoded
}

func compareWithPython(t *testing.T, calls []call) {
	t.Helper()
	request := make([]any, len(calls))
	for i, c := range calls {
		request[i] = c.args
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
			if os.Getenv("MAP_ORACLE_VERBOSE") != "" {
				t.Logf("%s: %s: %s", label, want.Type, want.Error)
			}
			if err.Error() != want.Error || pythonType(err) != want.Type {
				t.Errorf("%s: error mismatch\n  python: %s: %q\n  go:     %s: %q", label, want.Type, want.Error, pythonType(err), err)
			}
		default:
			encoded := encode(t, got)
			compared += len(encoded)
			diffs, diffErr := jsoncmp.Diff(want.Value, encoded, jsoncmp.Options{RelTol: 1e-6, AbsTol: 1e-6, MaxDiffs: 10})
			if diffErr != nil {
				t.Fatal(diffErr)
			}
			if len(diffs) > 0 {
				t.Errorf("%s: differs from Python:\n  %s", label, strings.Join(diffs, "\n  "))
			}
		}
	}
	t.Logf("%d calls compared (%d JSON bytes), %d raised in both", len(calls), compared, raised)
}

// loads adds load_map calls: compact and expanded, without and with root.
func loads(label, path, root string, limits *Limits) []call {
	var out []call
	for _, compact := range []bool{true, false} {
		var limitsValue any
		if limits != nil {
			limitsValue = limits
		}
		var rootValue any
		if root != "" {
			rootValue = root
		}
		out = append(out, call{fmt.Sprintf("%s compact=%v", label, compact), []any{"load_map", path, rootValue, compact, limitsValue}})
	}
	return out
}

func TestOracleReadMap(t *testing.T) {
	root := tempRoot(t)
	var calls []call
	add := func(name string, data []byte, limits *Limits) {
		path := write(t, root, name, data)
		var limitsValue any
		if limits != nil {
			limitsValue = limits
		}
		calls = append(calls, call{name, []any{"read_map", path, limitsValue}}, call{name, []any{"read_map_header", path, limitsValue}})
	}
	light21 := ft.Pack("7fIf3ffQQ", 1, 2, 3, 0, 0.5, 0, 1, 7, 45, .1, .2, .3, 100, uint64(1)<<63, 13)
	light20 := append(ft.Pack("7fIf3f", 0, 0, 0, 0, 0, 0, 1, 0, 0, 1, 1, 1), make([]byte, light20Extra)...)
	full := mt.NewMap()
	full.Name = "Máp " + strings.Repeat("x", 128)
	full.Objects = []mt.Object{
		{Model: "Models/Prop.owmdl", Groups: []mt.Group{
			{Look: "Look.owmat", Records: [][]byte{mt.RecordBytes(), mt.At(1e-7, -3, 1e30).Bytes()}},
			{Look: "null", Records: [][]byte{(mt.Record{Position: [3]float64{1, 2, 3}, Scale: [3]float64{-1, 2, 0},
				Rotation: [4]float64{1, 2, 3, 4}}).Bytes()}},
			{Look: "Empty.owmat"}}},
		{Model: "", Groups: nil},
		{Model: "Other.owentity", Groups: []mt.Group{{Look: "", Records: [][]byte{mt.RecordBytes()}}}},
	}
	full.Details = []mt.Detail{{Model: "Entity.owentity", Look: "null", Record: mt.At(1, 2, 3).Bytes()},
		{Model: "ü/" + strings.Repeat("ß", 2000), Look: "x", Record: (mt.Record{Rotation: [4]float64{0, 0, 1e-20, 0}}).Bytes()}}
	full.Lights = [][]byte{light21, light21}
	full.Sounds = []mt.Sound{{Position: [3]float64{3, 4, 5}, Files: []string{"Sounds/é.ogg", "Sounds/b.ogg"}}, {Files: nil}}
	data := full.Bytes()
	add("full.owmap", data, nil)
	for n := 0; n < len(data); n += 1 + n/40 {
		add(fmt.Sprintf("trunc/t%05d.owmap", n), data[:n], nil)
	}
	legacy := mt.NewMap()
	legacy.Version = [2]uint16{2, 0}
	legacy.Name = "null"
	legacy.Lights = [][]byte{light20}
	legacy.Sounds = []mt.Sound{{Position: [3]float64{9, 8, 7}, Files: []string{"sound.ogg"}}}
	add("legacy.owmap", legacy.Bytes(), nil)
	future := mt.NewMap()
	future.Version = [2]uint16{2, 2}
	add("future.owmap", future.Bytes(), nil)
	future.Version = [2]uint16{3, 0}
	add("future3.owmap", future.Bytes(), nil)
	add("trailing.owmap", append(full.Bytes(), "extra"...), nil)
	add("zero-quat.owmap", mt.Details(mt.Detail{Model: "a.owmdl", Record: (mt.Record{}).Bytes()}), nil)
	add("nan.owmap", mt.Details(mt.Detail{Model: "a.owmdl", Record: mt.At(math.NaN(), 0, 0).Bytes()}), nil)
	add("inf-scale.owmap", mt.Details(mt.Detail{Model: "a.owmdl", Record: (mt.Record{Scale: [3]float64{1, math.Inf(1), 1}, Rotation: [4]float64{0, 0, 0, 1}}).Bytes()}), nil)
	add("nan-rot.owmap", mt.Details(mt.Detail{Model: "a.owmdl", Record: (mt.Record{Rotation: [4]float64{0, math.NaN(), 0, 1}}).Bytes()}), nil)
	empty := mt.NewMap().Bytes()
	add("neg-sound.owmap", append(append(append([]byte{}, empty[:len(empty)-4]...), ft.Pack("I", 1)...), ft.Pack("3fi", 0, 0, 0, -1)...), nil)
	add("nul-name.owmap", mt.Details(mt.Detail{Model: "a\x00.owmdl", Record: mt.RecordBytes()}), nil)
	add("long-name.owmap", mt.Details(mt.Detail{Model: strings.Repeat("a", 4097), Record: mt.RecordBytes()}), nil)
	three := mt.NewMap()
	three.Objects = []mt.Object{{Model: "a.owmdl", Groups: []mt.Group{{Records: [][]byte{mt.RecordBytes(), mt.RecordBytes(), mt.RecordBytes()}}}}}
	limits := DefaultLimits
	limits.Instances = 2
	add("instances.owmap", three.Bytes(), &limits)
	small := DefaultLimits
	small.FileBytes = 8
	add("bytes.owmap", three.Bytes(), &small)
	groups := DefaultLimits
	groups.Groups = 0
	add("groups.owmap", three.Bytes(), &groups)
	lights := DefaultLimits
	lights.Lights, lights.Sounds, lights.SoundFiles = 1, 1, 1
	add("lights.owmap", full.Bytes(), &lights)
	sounds := DefaultLimits
	sounds.SoundFiles = 1
	add("sounds.owmap", full.Bytes(), &sounds)
	negative := DefaultLimits
	negative.Models = -1
	add("negative.owmap", three.Bytes(), &negative)
	bomb := append(ft.Pack("HH", 2, 1), ft.String("Bad")...)
	bomb = append(bomb, ft.Pack("III", 1, 0, 0)...)
	bomb = append(bomb, ft.String("a.owmdl")...)
	bomb = append(bomb, ft.Pack("I", 1)...)
	bomb = append(bomb, ft.String("")...)
	add("bomb.owmap", append(bomb, ft.Pack("I", 99_999)...), nil)
	add("bomb2.owmap", append(bomb, ft.Pack("I", uint32(math.MaxUint32))...), nil)
	add("counts.owmap", append(append(ft.Pack("HH", 2, 1), ft.String("")...), ft.Pack("III", 100_001, 0, 0)...), nil)
	calls = append(calls,
		call{"missing", []any{"read_map", filepath.Join(root, "missing.owmap"), nil}},
		call{"extension", []any{"read_map_header", write(t, root, "map.txt", data), nil}},
		call{"upper", []any{"read_map", write(t, root, "UPPER.OWMAP", data), nil}})
	compareWithPython(t, calls)
}

func TestOracleLoadMap(t *testing.T) {
	var calls []call
	// write_fixture, with and without explicit root.
	fixture := tempRoot(t)
	path := mt.WriteFixture(t, fixture)
	calls = append(calls, loads("fixture", path, "", nil)...)
	calls = append(calls, loads("fixture root", path, fixture, nil)...)
	calls = append(calls, loads("fixture outside root", path, filepath.Join(fixture, "Models"), nil)...)
	calls = append(calls, loads("fixture missing root", path, filepath.Join(fixture, "nope"), nil)...)
	for _, change := range []func(*Limits){
		func(l *Limits) { l.UniqueVertices = 2 }, func(l *Limits) { l.Models = 2 },
		func(l *Limits) { l.MeshInstances = 2 }, func(l *Limits) { l.GraphNodes = 2 },
		func(l *Limits) { l.GraphNodes = -2 }, func(l *Limits) { l.Models = 4 },
	} {
		limits := DefaultLimits
		change(&limits)
		calls = append(calls, loads(fmt.Sprintf("limits %+v", limits), path, "", &limits)...)
	}
	// The material demo with looks: collision, partial, null slot, entity
	// look, missing look, and direct/entity references to one model.
	demo := tempRoot(t)
	ft.GenerateMaterialDemo(t, demo)
	folder := "Models/MaterialRobot.00C/"
	write(t, demo, folder+"Materials/0000000007A1.owmat", ft.MaterialBytes(nil, nil, 33))
	write(t, demo, folder+"Materials/000000002DD4.owmat", ft.MaterialBytes([]ft.TextureRef{{Reference: "../Textures/000000A006.png", Binding: 2903569922}}, nil, 0))
	write(t, demo, folder+"ModelLooks/Collision.owmat", ft.LookBytes(
		ft.LookEntry{Key: 1, Reference: "../../Materials/0000000007A1.owmat"},
		ft.LookEntry{Key: 2, Reference: "../../Materials/000000B002.owmat"},
		ft.LookEntry{Key: 3, Reference: "../../Materials/000000B003.owmat"}))
	write(t, demo, folder+"ModelLooks/AllCollision.owmat", ft.LookBytes(
		ft.LookEntry{Key: 1, Reference: "../../Materials/0000000007A1.owmat"},
		ft.LookEntry{Key: 2, Reference: "../../Materials/000000002DD4.owmat"},
		ft.LookEntry{Key: 3, Reference: "../../Materials/0000000007A1.owmat"}))
	write(t, demo, "Partial.owmat", ft.LookBytes(ft.LookEntry{Key: 2, Reference: "Override.owmat"}, ft.LookEntry{Key: 3, Reference: "null"},
		ft.LookEntry{Key: 9, Reference: "Override.owmat"}))
	write(t, demo, "Override.owmat", ft.MaterialBytes([]ft.TextureRef{{Reference: folder + "Textures/000000A003.png", Binding: 1239794147}}, nil, 55))
	write(t, demo, "Material.owmat", ft.MaterialBytes(nil, nil, 1))
	write(t, demo, "Broken.owmat", []byte("broken"))
	write(t, demo, "Broken.owmdl", []byte("broken"))
	write(t, demo, "Empty.owentity", ft.Entity{Name: "Empty", Model: "", Minor: 1, Relative: "."}.Bytes())
	model := folder + "MaterialRobot.owmdl"
	entity := "Entities/MaterialRobot/MaterialRobot.owentity"
	m := mt.NewMap()
	m.Name = ""
	m.Objects = []mt.Object{
		{Model: model, Groups: []mt.Group{
			{Look: folder + "ModelLooks/Collision.owmat", Records: [][]byte{mt.RecordBytes(), mt.At(5, 0, 0).Bytes()}},
			{Look: folder + "ModelLooks/Default.owmat", Records: [][]byte{mt.At(0, 0, 5).Bytes()}},
			{Look: folder + "ModelLooks/AllCollision.owmat", Records: [][]byte{mt.At(0, 5, 5).Bytes()}},
			{Look: "", Records: [][]byte{mt.At(1, 1, 1).Bytes()}},
			{Look: "Missing.owmat", Records: [][]byte{mt.At(2, 2, 2).Bytes()}},
			{Look: "Material.owmat", Records: [][]byte{mt.At(3, 3, 3).Bytes()}},
			{Look: "Broken.owmat", Records: [][]byte{mt.At(4, 4, 4).Bytes()}}}},
		{Model: entity, Groups: []mt.Group{
			{Look: "Partial.owmat", Records: [][]byte{(mt.Record{Position: [3]float64{1, 2, 3}, Scale: [3]float64{-1, 1, 2},
				Rotation: [4]float64{.1, .2, .3, .9}}).Bytes()}},
			{Look: folder + "ModelLooks/Collision.owmat", Records: [][]byte{mt.RecordBytes()}}}},
		{Model: "", Groups: []mt.Group{{Records: [][]byte{mt.RecordBytes()}}}},
		{Model: "Broken.owmdl", Groups: []mt.Group{{Records: [][]byte{mt.RecordBytes(), mt.RecordBytes()}}}},
		{Model: "Empty.owentity", Groups: []mt.Group{{Records: [][]byte{mt.RecordBytes()}}}},
		{Model: "missing.owmdl", Groups: []mt.Group{{Records: [][]byte{mt.RecordBytes()}}}},
	}
	m.Details = []mt.Detail{{Model: entity, Look: "", Record: mt.At(9, 9, 9).Bytes()}}
	m.Lights = [][]byte{ft.Pack("7fIf3ffQQ", 1, 2, 3, 0, 0, 0, 1, 1, 45, .1, .2, .3, 100, 12, 13)}
	m.Sounds = []mt.Sound{{Files: []string{"a.ogg"}}}
	calls = append(calls, loads("demo", write(t, demo, "demo.owmap", m.Bytes()), "", nil)...)
	calls = append(calls, loads("demo nested", write(t, demo, "Maps/nested.owmap", m.Bytes()), demo, nil)...)
	// Reference validation errors, in placement order.
	for _, reference := range []string{"../outside.owmdl", `C:\outside.owmdl`, "//host/a.owmdl", "/absolute.owmdl",
		`\\host\share\a.owmdl`, "a:stream.owmdl", "models/../../outside.owmdl", "bad.exe", "", "./" + model, model + "/"} {
		for variant, look := range []string{"../look.owmat", "Partial.owmat"} {
			bad := mt.Details(mt.Detail{Model: model, Record: mt.RecordBytes()},
				mt.Detail{Model: reference, Look: "x.owmat", Record: mt.RecordBytes()},
				mt.Detail{Model: model, Look: look, Record: mt.RecordBytes()})
			name := fmt.Sprintf("bad%02d-%d.owmap", len(calls), variant)
			calls = append(calls, loads("reference "+reference, write(t, demo, name, bad), demo, nil)[:1]...)
		}
	}
	calls = append(calls, loads("bad look", write(t, demo, "badlook.owmap", mt.Details(mt.Detail{Model: model, Look: "look.png", Record: mt.RecordBytes()})), "", nil)...)
	calls = append(calls, loads("empty", write(t, demo, "empty.owmap", mt.NewMap().Bytes()), "", nil)...)
	calls = append(calls, loads("missing only", write(t, demo, "missing.owmap", mt.Details(
		mt.Detail{Model: "missing.owmdl", Record: mt.RecordBytes()}, mt.Detail{Model: "Broken.owmdl", Record: mt.RecordBytes()},
		mt.Detail{Record: mt.RecordBytes()})), "", nil)...)
	// Over 500 distinct warnings, plus collision meshes: truncation order.
	many := mt.NewMap()
	for i := 0; i < 520; i++ {
		many.Details = append(many.Details, mt.Detail{Model: fmt.Sprintf("missing%03d.owmdl", i), Record: mt.RecordBytes()})
	}
	many.Details = append(many.Details, mt.Detail{Model: model, Look: folder + "ModelLooks/Collision.owmat", Record: mt.RecordBytes()})
	calls = append(calls, loads("many warnings", write(t, demo, "many.owmap", many.Bytes()), "", nil)...)
	// Many placements of a few sources.
	crowd := mt.NewMap()
	for i := 0; i < 40; i++ {
		look := []string{"", folder + "ModelLooks/Collision.owmat", "Partial.owmat", folder + "ModelLooks/Default.owmat"}[i%4]
		source := []string{model, entity}[i%2]
		crowd.Objects = append(crowd.Objects, mt.Object{Model: source, Groups: []mt.Group{{Look: look,
			Records: [][]byte{mt.At(float64(i), 0, 0).Bytes(), mt.At(0, float64(i), 0).Bytes()}}}})
	}
	calls = append(calls, loads("crowd", write(t, demo, "crowd.owmap", crowd.Bytes()), "", nil)...)
	calls = append(calls,
		call{"missing map", []any{"load_map", filepath.Join(demo, "nope.owmap"), nil, true, nil}},
		call{"not a map", []any{"load_map", filepath.Join(demo, "Broken.owmdl"), nil, true, nil}},
		call{"corrupt map", []any{"load_map", write(t, demo, "corrupt.owmap", []byte("xx")), nil, true, nil}})
	compareWithPython(t, calls)
}

func TestOracleScanMaps(t *testing.T) {
	root := tempRoot(t)
	mt.WriteFixture(t, root)
	named := func(name string) []byte {
		m := mt.NewMap()
		m.Name = name
		return m.Bytes()
	}
	write(t, root, "broken.owmap", []byte("bad"))
	write(t, root, "ignored.txt", []byte("not a map"))
	write(t, root, "a/Zeta.owmap", named("zeta"))
	write(t, root, "a/b/alpha.OWMAP", named("Alpha"))
	write(t, root, "a/b/c/beta.owmap", named("ALPHA"))
	write(t, root, "c/Straße.owmap", named("Straße"))
	write(t, root, "c/strasse.owmap", named("STRASSE"))
	write(t, root, "c/unnamed.owmap", named(""))
	write(t, root, "c/v3.owmap", append(ft.Pack("HH", 3, 0), 0))
	write(t, root, "d/e/broken2.owmap", []byte("bad2"))
	write(t, root, "d/f/broken3.owmap", nil)
	outside := t.TempDir()
	write(t, outside, "outside.owmap", named("Outside"))
	write(t, outside, "dir/inside.owmap", named("Linked dir"))
	symlink := func(target, link string) {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
	}
	symlink(filepath.Join(outside, "outside.owmap"), "link.owmap")
	symlink(filepath.Join(outside, "dir"), "linkdir")
	symlink(filepath.Join(root, "a/Zeta.owmap"), "c/zeta-link.owmap")
	symlink(filepath.Join(root, "gone.owmap"), "dangling.owmap")
	os.Mkdir(filepath.Join(root, "dir.owmap"), 0o755)
	calls := []call{
		{"root", []any{"scan_maps", root, nil}},
		{"sub", []any{"scan_maps", filepath.Join(root, "a"), nil}},
		{"limit", []any{"scan_maps", root, 5}},
		{"limit exact", []any{"scan_maps", filepath.Join(root, "c"), 5}},
		{"missing", []any{"scan_maps", filepath.Join(root, "missing"), nil}},
		{"file", []any{"scan_maps", filepath.Join(root, "ignored.txt"), nil}},
	}
	compareWithPython(t, calls)
}

// The DataTool-layout benchmark map from gocore/bench/make_library.py, if
// generated (AVOLITION_BENCH_MAP overrides its location).
func benchMap() (path, root string) {
	path = os.Getenv("AVOLITION_BENCH_MAP")
	if path == "" {
		path = "/tmp/claude-0/-home-user-Avolition/e70be9b6-149e-503d-a8ec-7b456af224a1/scratchpad/lib/Maps/Bench/Bench/Default/Bench.owmap"
	}
	root = os.Getenv("AVOLITION_BENCH_MAP_ROOT")
	if root == "" {
		root = filepath.Dir(filepath.Dir(filepath.Dir(path)))
	}
	return path, root
}

const benchPython = `
import json, time
from owpreview import map_formats as mf
path, root, out = args
best = None
for _ in range(3):
    start = time.perf_counter()
    asset = mf.load_map(path, root)
    elapsed = time.perf_counter() - start
    best = elapsed if best is None else min(best, elapsed)
start = time.perf_counter()
text = json.dumps(asset, separators=(',', ':'))
encode = time.perf_counter() - start
with open(out, 'w', encoding='utf-8') as handle:
    handle.write(text)
result = {'load': best, 'encode': encode, 'size': len(text.encode('utf-8'))}
`

// TestOracleBenchMap compares the full compact output for the large
// benchmark map with Python, top-level key by key (prototypes one by one,
// to bound memory), and reports load time and JSON size for both.
func TestOracleBenchMap(t *testing.T) {
	if testing.Short() {
		t.Skip("large comparison")
	}
	path, root := benchMap()
	if !pypath.IsFile(path) {
		t.Skip("benchmark map not generated: ", path)
	}
	out := filepath.Join(t.TempDir(), "python.json")
	raw := oracle.Exec(t, benchPython, path, root, out).MustValue(t)
	var python struct {
		Load, Encode float64
		Size         int
	}
	if err := json.Unmarshal(raw, &python); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := LoadMap(path, root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := formats.AppendAssetJSON(nil, asset)
	if err != nil {
		t.Fatal(err)
	}
	var wantTop, gotTop map[string]json.RawMessage
	if err := json.Unmarshal(want, &wantTop); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &gotTop); err != nil {
		t.Fatal(err)
	}
	if len(wantTop) != len(gotTop) {
		t.Fatalf("top-level keys: want %d, got %d", len(wantTop), len(gotTop))
	}
	options := jsoncmp.Options{RelTol: 1e-6, AbsTol: 1e-6, MaxDiffs: 10}
	for key, wantValue := range wantTop {
		gotValue, ok := gotTop[key]
		if !ok {
			t.Errorf("missing key %s", key)
			continue
		}
		if key == "meshPrototypes" {
			var wantProto, gotProto map[string]json.RawMessage
			json.Unmarshal(wantValue, &wantProto)
			json.Unmarshal(gotValue, &gotProto)
			if len(wantProto) != len(gotProto) {
				t.Errorf("prototypes: want %d, got %d", len(wantProto), len(gotProto))
			}
			for id, value := range wantProto {
				if diffs, err := jsoncmp.Diff(value, gotProto[id], options); err != nil || len(diffs) > 0 {
					t.Errorf("prototype %s: %v %v", id, err, diffs)
				}
			}
			continue
		}
		if diffs, err := jsoncmp.Diff(wantValue, gotValue, options); err != nil || len(diffs) > 0 {
			t.Errorf("%s: %v\n  %s", key, err, strings.Join(diffs, "\n  "))
		}
	}
	s := summary(asset)
	t.Logf("%d placements, %d models, %d prototypes, %d meshes, %d warnings", s.InstanceCount, s.ModelCount,
		s.PrototypeCount, s.MeshCount, len(asset.Warnings))
	t.Logf("Python: load_map %.3fs (best of 3), json.dumps %.3fs, %d bytes", python.Load, python.Encode, python.Size)
	t.Logf("Go JSON: %d bytes", len(got))
}

// BenchmarkLoadBenchMap measures LoadMap and AppendAssetJSON on the
// benchmark map.
func BenchmarkLoadBenchMap(b *testing.B) {
	path, root := benchMap()
	if !pypath.IsFile(path) {
		b.Skip("benchmark map not generated: ", path)
	}
	b.Run("load", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := LoadMap(path, root); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("load1worker", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := LoadMapWith(path, root, Options{Workers: 1}); err != nil {
				b.Fatal(err)
			}
		}
	})
	asset, err := LoadMap(path, root)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("json", func(b *testing.B) {
		var size int
		for i := 0; i < b.N; i++ {
			out, err := formats.AppendAssetJSON(nil, asset)
			if err != nil {
				b.Fatal(err)
			}
			size = len(out)
		}
		b.ReportMetric(float64(size), "bytes")
	})
	b.Run("json.Marshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := json.Marshal(asset); err != nil {
				b.Fatal(err)
			}
		}
	})
}
