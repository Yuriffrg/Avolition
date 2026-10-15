package c4dexport_test

// Oracle tests: run owpreview/c4d_export.py on the same inputs and compare
// payloads key for key, and run the unchanged Cinema 4D writer
// (owpreview/c4d_scene.py build_scene with tests/test_c4d_scene.py's fake C4D
// API) on the Go payload after a JSON round trip, exactly as the bridge
// delivers it.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"avolition/gocore/internal/c4dexport"
	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
)

// payloadPython evaluates a batch of make_payload/make_set_payload calls and
// returns one result per call, so a case table costs one Python start.
const payloadPython = `
from owpreview.c4d_export import make_payload, make_set_payload, _normalized_normal_asset, select_model_asset
out = []
for kind, call_args, kwargs in args[0]:
    try:
        if kind == 'set':
            value = make_set_payload(*call_args, **kwargs)
        elif kind == 'normals':
            value = _normalized_normal_asset(*call_args)
        elif kind == 'select':
            value = select_model_asset(*call_args)
        else:
            value = make_payload(*call_args, **kwargs)
        out.append({'ok': True, 'result': value})
    except Exception as error:
        out.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
result = out
`

type oracleCase struct {
	label  string
	kind   string // "payload", "set", "normals" or "select"
	args   []any
	kwargs map[string]any
	run    func() (any, error)
}

type batchResult struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// Payload asset arrays hold float32 data in Go (shortest float32 text) and
// float64 expansions in Python: jsoncmp.Default absorbs that. Baked tracks
// are float64 in both and must agree to 1e-9.
var tight = jsoncmp.Options{RelTol: 1e-9, AbsTol: 1e-9}

func runCases(t *testing.T, cases []oracleCase) {
	t.Helper()
	var requests []any
	for _, c := range cases {
		kwargs := c.kwargs
		if kwargs == nil {
			kwargs = map[string]any{}
		}
		requests = append(requests, []any{c.kind, c.args, kwargs})
	}
	raw := oracle.Exec(t, payloadPython, requests).MustValue(t)
	var results []batchResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		want := results[i]
		got, err := c.run()
		if !want.OK {
			if err == nil {
				t.Errorf("%s: Python raised %q, Go succeeded", c.label, want.Error)
			} else if err.Error() != want.Error {
				t.Errorf("%s: error\n got  %q\n want %q", c.label, err.Error(), want.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: Go failed with %q", c.label, err)
			continue
		}
		before := t.Failed()
		jsoncmp.Equal(t, want.Result, got, jsoncmp.Default)
		if payload, ok := got.(*c4dexport.Payload); ok {
			var python struct {
				Animation json.RawMessage `json:"animation"`
			}
			if err := json.Unmarshal(want.Result, &python); err != nil {
				t.Fatal(err)
			}
			jsoncmp.Equal(t, python.Animation, payload.Animation, tight)
		}
		if t.Failed() && !before {
			t.Logf("case %s differs", c.label)
		}
	}
}

func kw(includeMesh, rootMotion bool, extra ...any) map[string]any {
	out := map[string]any{"include_mesh": includeMesh, "root_motion": rootMotion}
	for i := 0; i+1 < len(extra); i += 2 {
		out[extra[i].(string)] = extra[i+1]
	}
	return out
}

func payloadCase(t *testing.T, label string, asset *formats.Asset, clip *formats.Clip, materials *c4dexport.Materials, opts c4dexport.PayloadOptions) oracleCase {
	kwargs := kw(opts.IncludeMesh, opts.RootMotion, "rig_id", opts.RigID)
	if opts.ClipName != "" {
		kwargs["clip_name"] = opts.ClipName
	}
	if opts.ScopeModelID != nil {
		kwargs["scope_model_id"] = *opts.ScopeModelID
	}
	var pyMaterials any = map[string]any{}
	if materials != nil {
		pyMaterials = materials
	}
	return oracleCase{label: label, kind: "payload",
		args:   []any{exporttest.PyJSON(t, asset), exporttest.PyJSON(t, clip), pyMaterials},
		kwargs: kwargs,
		run:    func() (any, error) { return c4dexport.MakePayload(asset, clip, materials, opts) }}
}

func setCase(t *testing.T, label string, asset *formats.Asset, list []c4dexport.Assignment, materials *c4dexport.Materials, opts c4dexport.SetOptions) oracleCase {
	var pyAssignments []any
	for _, a := range list {
		record := map[string]any{"modelId": a.ModelID}
		if a.Clip != nil {
			record["clip"] = exporttest.PyJSON(t, a.Clip)
		}
		pyAssignments = append(pyAssignments, record)
	}
	if pyAssignments == nil {
		pyAssignments = []any{}
	}
	var pyMaterials any = map[string]any{}
	if materials != nil {
		pyMaterials = materials
	}
	return oracleCase{label: label, kind: "set",
		args:   []any{exporttest.PyJSON(t, asset), pyAssignments, pyMaterials},
		kwargs: kw(opts.IncludeMesh, opts.RootMotion, "name", opts.Name),
		run:    func() (any, error) { return c4dexport.MakeSetPayload(asset, list, materials, opts) }}
}

func loadEntity(t *testing.T, root, entity string) *formats.Asset {
	asset, err := formats.LoadAsset(filepath.Join(root, entity), root)
	if err != nil {
		t.Fatal(err)
	}
	return asset
}

func loadClip(t *testing.T, path string) *formats.Clip {
	clip, err := formats.LoadClip(path)
	if err != nil {
		t.Fatal(err)
	}
	return clip
}

func optionVariants(base c4dexport.PayloadOptions) []c4dexport.PayloadOptions {
	var out []c4dexport.PayloadOptions
	for _, mesh := range []bool{true, false} {
		for _, root := range []bool{true, false} {
			opts := base
			opts.IncludeMesh, opts.RootMotion = mesh, root
			out = append(out, opts)
		}
	}
	return out
}

func materialsOf(asset *formats.Asset) *c4dexport.Materials {
	materials := ordered.New[*ordered.Map[*formats.Material]]()
	for _, model := range asset.Models {
		if model.Materials != nil {
			materials.Set(model.ID, model.Materials)
		}
	}
	return materials
}

func TestMakePayloadMatchesPython(t *testing.T) {
	var cases []oracleCase
	for _, library := range []struct{ root, entity string }{
		{"demo-assets", "Entities/PreviewRobot/PreviewRobot.owentity"},
		{"material-assets", "Entities/MaterialRobot/MaterialRobot.owentity"},
	} {
		root := filepath.Join(exporttest.TestsDir(), library.root)
		asset := loadEntity(t, root, library.entity)
		clips, _ := filepath.Glob(filepath.Join(root, "Animations", "*.owanimclip"))
		for _, path := range clips {
			clip := loadClip(t, path)
			for _, opts := range optionVariants(c4dexport.DefaultPayloadOptions()) {
				label := fmt.Sprintf("%s/%s mesh=%v root=%v", library.root, filepath.Base(path), opts.IncludeMesh, opts.RootMotion)
				cases = append(cases, payloadCase(t, label, asset, clip, nil, opts))
			}
		}
	}

	// Multi-object entity with attachments loaded from disk.
	dir := t.TempDir()
	attached, err := formats.LoadAsset(exporttest.AttachedEntity(t, dir), dir)
	if err != nil {
		t.Fatal(err)
	}
	clipPath := ft.Write(t, dir, "clip.owanimclip", ft.NewClip().Bytes())
	attachedClip := loadClip(t, clipPath)
	for _, model := range attached.Models {
		for _, opts := range optionVariants(c4dexport.DefaultPayloadOptions()) {
			scoped := opts
			scoped.ScopeModelID = &model.ID
			cases = append(cases, payloadCase(t, "attached scope "+model.ID, attached, attachedClip, nil, scoped))
			rig := opts
			rig.RigID = model.ID
			cases = append(cases, payloadCase(t, "attached rig "+model.ID, attached, attachedClip, nil, rig))
		}
	}

	// Synthetic selected-object fixture with materials and textures.
	asset, clip := exporttest.SelectedFixture(t)
	materials := materialsOf(asset)
	for _, scope := range []string{"arms", "gun", "charm"} {
		for _, opts := range optionVariants(c4dexport.DefaultPayloadOptions()) {
			opts.ScopeModelID = ptr(scope)
			opts.ClipName = "Named " + scope
			cases = append(cases, payloadCase(t, "selected "+scope, asset, clip, materials, opts))
		}
		opts := c4dexport.DefaultPayloadOptions()
		opts.RigID = scope
		cases = append(cases, payloadCase(t, "rig "+scope, asset, clip, materials, opts))
	}
	conflict := c4dexport.DefaultPayloadOptions()
	conflict.RigID, conflict.ScopeModelID = "arms", ptr("gun")
	cases = append(cases, payloadCase(t, "conflicting target", asset, clip, materials, conflict))
	missing := c4dexport.DefaultPayloadOptions()
	missing.ScopeModelID = ptr("missing")
	cases = append(cases, payloadCase(t, "missing scope", asset, clip, materials, missing))
	noRig := c4dexport.DefaultPayloadOptions()
	noRig.RigID = "missing"
	cases = append(cases, payloadCase(t, "missing rig", asset, clip, materials, noRig))
	unmatched := exporttest.Clip("Other", 30, 2, []formats.ClipBone{{Name: "bone_FFFF"}})
	cases = append(cases, payloadCase(t, "no matching bones", asset, unmatched, nil, c4dexport.DefaultPayloadOptions()))
	for _, timing := range []struct {
		fps  float64
		last int64
	}{{0, 2}, {1001, 2}, {30, -1}} {
		bad := exporttest.Clip("Bad", timing.fps, timing.last, clip.Bones)
		cases = append(cases, payloadCase(t, "invalid timing", asset, bad, nil, c4dexport.DefaultPayloadOptions()))
	}
	sheared, _ := exporttest.SelectedFixture(t)
	nodeByID(sheared, "arms:bone:1").Matrix[4] = .4
	gun := c4dexport.DefaultPayloadOptions()
	gun.ScopeModelID = ptr("gun")
	cases = append(cases, payloadCase(t, "sheared rest attachment", sheared, clip, nil, gun))
	noGraph, _ := exporttest.SelectedFixture(t)
	noGraph.ExportHierarchy = []formats.Node{}
	cases = append(cases, payloadCase(t, "missing hierarchy", noGraph, clip, nil, c4dexport.DefaultPayloadOptions()))

	// Nonuniform scale (Blender oracle fixture) and the sample budget.
	fixture := exporttest.LoadPlayback(t)
	rig := &formats.Asset{Name: "Oracle", Models: []*formats.Model{{ID: "rig", Name: "Rig", Bones: fixture.Bones,
		Meshes: []*formats.Mesh{}, Sockets: []formats.Socket{}}}, Warnings: []string{}}
	if rig.ExportHierarchy, err = formats.BuildExportHierarchy(rig); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, payloadCase(t, "shear", rig, exporttest.Clip("Oracle", 30, 20, fixture.Tracks), nil, c4dexport.DefaultPayloadOptions()))
	cases = append(cases, payloadCase(t, "budget", rig, exporttest.Clip("Long", 30, 62_500, fixture.Tracks), nil, c4dexport.DefaultPayloadOptions()))
	cases = append(cases, payloadCase(t, "rest pose", rig, exporttest.Clip("Rest", 30, 40, nil), nil, c4dexport.DefaultPayloadOptions()))
	runCases(t, cases)
	// The exact budget boundary (62,500 frames x 4 bones) is accepted; Python
	// needs a minute for it, so only Go runs it.
	if _, err := c4dexport.MakePayload(rig, exporttest.Clip("Rest", 30, 62_499, nil), nil, c4dexport.DefaultPayloadOptions()); err != nil {
		t.Fatal(err)
	}
}

func TestMakeSetPayloadMatchesPython(t *testing.T) {
	var cases []oracleCase
	asset, list := exporttest.AssignedFixture(t)
	materials := materialsOf(asset)
	for _, mesh := range []bool{true, false} {
		for _, root := range []bool{true, false} {
			opts := c4dexport.SetOptions{IncludeMesh: mesh, RootMotion: root, Name: "Reload set"}
			cases = append(cases, setCase(t, fmt.Sprintf("set mesh=%v root=%v", mesh, root), asset, assignments(list), materials, opts))
		}
	}
	nonGrid, nonGridList := exporttest.AssignedFixture(t)
	nonGridList[0].Clip.LastFrame = 23
	cases = append(cases, setCase(t, "non-grid", nonGrid, assignments(nonGridList), nil, c4dexport.DefaultSetOptions()))
	cases = append(cases, setCase(t, "single", asset, assignments(list[1:]), nil, c4dexport.DefaultSetOptions()))

	for _, name := range []string{"empty", "missing-model", "duplicate", "missing-clip", "no-match", "missing-joint",
		"fps0", "fpsNaN", "last-1", "budget", "duplicate-model", "no-graph", "sheared-graph"} {
		bad, badList := exporttest.AssignedFixture(t)
		records := assignments(badList)
		switch name {
		case "empty":
			records = nil
		case "missing-model":
			records[0].ModelID = "absent"
		case "duplicate":
			records = append(records, records[0])
		case "missing-clip":
			records[0].Clip = nil
		case "no-match":
			records[0].Clip.Bones = []formats.ClipBone{{Name: "bone_FFFF"}}
		case "missing-joint":
			var kept []formats.Node
			for _, node := range bad.ExportHierarchy {
				if node.ID != "gun:bone:0" {
					if node.ParentID != nil && *node.ParentID == "gun:bone:0" {
						node.ParentID = ptr("gun:skeleton")
					}
					kept = append(kept, node)
				}
			}
			bad.ExportHierarchy = kept
		case "fps0":
			records[0].Clip.FPS = 0
		case "fpsNaN":
			records[0].Clip.FPS = math.NaN()
		case "last-1":
			records[0].Clip.LastFrame = -1
		case "budget":
			for _, r := range records {
				r.Clip.FPS, r.Clip.LastFrame = 30, 70_000
			}
		case "duplicate-model":
			bad.Models = append(bad.Models, bad.Models[0])
		case "no-graph":
			bad.ExportHierarchy = []formats.Node{}
		case "sheared-graph":
			bad.ExportHierarchy[3].Matrix[1] = .5
		}
		if name == "fpsNaN" {
			// JSON has no NaN; Python receives float('nan') through the
			// oracle's exec instead (see TestSetNaNTimingMatchesPython).
			continue
		}
		cases = append(cases, setCase(t, name, bad, records, nil, c4dexport.DefaultSetOptions()))
	}
	runCases(t, cases)
}

func TestSetNaNTimingMatchesPython(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	want := oracle.Exec(t, `
from owpreview.c4d_export import make_set_payload
asset, assignments = args
assignments[0]['clip']['fps'] = float('nan')
try:
    make_set_payload(asset, assignments, {})
    result = None
except ValueError as error:
    result = str(error)
`, exporttest.PyJSON(t, asset), []any{
		map[string]any{"modelId": list[0].ModelID, "clip": exporttest.PyJSON(t, list[0].Clip)},
		map[string]any{"modelId": list[1].ModelID, "clip": exporttest.PyJSON(t, list[1].Clip)},
	}).MustValue(t)
	list[0].Clip.FPS = math.NaN()
	_, err := c4dexport.MakeSetPayload(asset, assignments(list), nil, c4dexport.DefaultSetOptions())
	var message string
	_ = json.Unmarshal(want, &message)
	if err == nil || err.Error() != message {
		t.Fatalf("got %v, want %q", err, message)
	}
}

func TestNormalizedNormalAssetMatchesPython(t *testing.T) {
	var cases []oracleCase
	add := func(label string, mesh *formats.Mesh) {
		asset := normalAsset(mesh)
		cases = append(cases, oracleCase{label: label, kind: "normals", args: []any{exporttest.PyJSON(t, asset)},
			run: func() (any, error) { return c4dexport.NormalizedNormalAsset(asset) }})
	}
	tri := [][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}
	add("authored", meshFixture(tri, []uint32{0, 1, 2}, [][3]float64{{.1, .2, .96}, {-.3, .9, .1}, {.7, -.2, -.6}}))
	add("corner angles", meshFixture([][3]float64{{0, 0, 0}, {100, 0, 0}, {0, 100, 0}, {0, 0, 1}, {0, math.Sqrt(3) / 2, .5}},
		[]uint32{0, 1, 2, 0, 3, 4}, [][3]float64{{0, 0, 0}, {.2, .3, .4}, {.2, .3, .4}, {.2, .3, .4}, {.2, .3, .4}}))
	add("split", meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {0, 0, 0}, {0, 1, 0}, {0, 0, 1}},
		[]uint32{0, 1, 2, 3, 4, 5}, [][3]float64{{0, 0, 0}, {0, 2, 0}, {0, 2, 0}, {0, 0, 0}, {0, 2, 0}, {0, 2, 0}}))
	add("cancel", meshFixture([][3]float64{{0, 0, 0}, {2, 0, 0}, {0, 2, 0}, {0, 1, 0}, {1, 0, 0}},
		[]uint32{0, 3, 4, 0, 1, 2}, [][3]float64{{0, 0, 0}, {0, 0, 2}, {0, 0, 2}, {0, 0, 2}, {0, 0, 2}}))
	add("degenerate", meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {2, 0, 0}, {7, 8, 9}},
		[]uint32{0, 1, 2, 0, 0, 1}, [][3]float64{{0, 0, 0}, {0, 0, 2}, {0, 0, 2}, {0, 0, 0}}))
	add("absent", meshFixture(tri, []uint32{0, 1, 2}, nil))
	add("bad length", &formats.Mesh{Name: "Bad", Positions: make([]float32, 9), Normals: make([]float32, 6), Indices: []uint32{0, 1, 2}})
	add("bad index", meshFixture(tri, []uint32{0, 1, 3}, [][3]float64{{0, 0, 0}, {0, 0, 1}, {0, 0, 1}}))
	add("bad topology", meshFixture(tri, []uint32{0, 1}, [][3]float64{{0, 0, 0}, {0, 0, 1}, {0, 0, 1}}))
	// A real mesh with every third normal zeroed and repeated warnings.
	root := filepath.Join(exporttest.TestsDir(), "material-assets")
	asset := loadEntity(t, root, "Entities/MaterialRobot/MaterialRobot.owentity")
	for _, mesh := range asset.Models[0].Meshes {
		for i := 0; i < len(mesh.Normals); i += 9 {
			mesh.Normals[i], mesh.Normals[i+1], mesh.Normals[i+2] = 0, 0, 0
		}
	}
	asset.Warnings = append(asset.Warnings, "Repeated", "Repeated")
	cases = append(cases, oracleCase{label: "material robot", kind: "normals", args: []any{exporttest.PyJSON(t, asset)},
		run: func() (any, error) { return c4dexport.NormalizedNormalAsset(asset) }})
	runCases(t, cases)
	// JSON cannot carry infinity to Python; the message is the one Python
	// raises (tests/test_export_normals.py checks the same inputs).
	infinite := meshFixture(tri, []uint32{0, 1, 2}, [][3]float64{{math.Inf(1), 0, 0}, {0, 0, 1}, {0, 0, 1}})
	if _, err := c4dexport.NormalizedNormalAsset(normalAsset(infinite)); err == nil ||
		err.Error() != "Authored mesh: normals must contain finite directions for C4D export." {
		t.Fatal(err)
	}
}

func TestSelectModelAssetMatchesPython(t *testing.T) {
	var cases []oracleCase
	asset, _ := exporttest.SelectedFixture(t)
	for _, id := range []string{"arms", "gun", "charm", "missing"} {
		id := id
		cases = append(cases, oracleCase{label: id, kind: "select", args: []any{exporttest.PyJSON(t, asset), id},
			run: func() (any, error) { return c4dexport.SelectModelAsset(asset, id) }})
	}
	for _, change := range []struct {
		label string
		apply func(*formats.Asset)
	}{
		{"missing parent", func(a *formats.Asset) { nodeByID(a, "gun:entity").ParentID = ptr("missing-parent") }},
		{"shear", func(a *formats.Asset) { nodeByID(a, "arms:bone:1").Matrix[4] = .4 }},
		{"not affine", func(a *formats.Asset) { nodeByID(a, "arms:bone:1").Matrix[3] = .4 }},
		{"duplicate", func(a *formats.Asset) { a.ExportHierarchy = append(a.ExportHierarchy, a.ExportHierarchy[0]) }},
		{"cycle", func(a *formats.Asset) { nodeByID(a, "arms:entity").ParentID = ptr("gun:entity") }},
		{"no graph", func(a *formats.Asset) { a.ExportHierarchy = []formats.Node{} }},
	} {
		bad, _ := exporttest.SelectedFixture(t)
		change.apply(bad)
		cases = append(cases, oracleCase{label: change.label, kind: "select", args: []any{exporttest.PyJSON(t, bad), "gun"},
			run: func() (any, error) { return c4dexport.SelectModelAsset(bad, "gun") }})
	}
	runCases(t, cases)
}

const graphPython = `
from owpreview.c4d_scene import _ordered_graph, _asset_models
out = []
for kind, value in args[0]:
    try:
        if kind == 'graph':
            out.append({'ok': True, 'result': [node['id'] for node in _ordered_graph(value)]})
        else:
            out.append({'ok': True, 'result': _asset_models(value)})
    except Exception as error:
        out.append({'ok': False, 'error': str(error)})
result = out
`

func TestOrderedGraphAndAssetModelsMatchPython(t *testing.T) {
	asset, _ := exporttest.SelectedFixture(t)
	graphs := map[string][]formats.Node{"valid": asset.ExportHierarchy}
	for label, change := range map[string]func([]formats.Node){
		"duplicate": func(g []formats.Node) { g[2].ID = g[1].ID },
		"orphan":    func(g []formats.Node) { g[1].ParentID = ptr("missing") },
		"shear":     func(g []formats.Node) { g[2].Matrix[4] = .5 },
		"zero":      func(g []formats.Node) { g[3].Matrix[0], g[3].Matrix[1], g[3].Matrix[2] = 0, 0, 0 },
		"affine":    func(g []formats.Node) { g[3].Matrix[15] = 2 },
	} {
		g := formats.CloneNodes(asset.ExportHierarchy)
		change(g)
		graphs[label] = g
	}
	var requests []any
	var labels []string
	for label, graph := range graphs {
		labels = append(labels, label)
		requests = append(requests, []any{"graph", graph})
	}
	prototypes := ordered.New[*formats.MeshPrototype]()
	prototypes.Set("p", &formats.MeshPrototype{Meshes: []*formats.Mesh{{Name: "Wall", Positions: []float32{}}, {Name: "Roof"}}})
	names := ordered.New[string]()
	names.Set("1", "Renamed roof")
	compact := &formats.Asset{MeshPrototypes: prototypes, Map: map[string]any{}, Models: []*formats.Model{
		{ID: "a", Name: "A", MeshPrototype: "p"}, {ID: "b", Name: "B", MeshPrototype: "p", MeshNames: names},
		{ID: "c", Name: "C", Meshes: []*formats.Mesh{{Name: "Own"}}}}}
	broken := *compact
	broken.Models = []*formats.Model{{ID: "x", MeshPrototype: "missing"}}
	duplicate := *compact
	duplicate.Models = append([]*formats.Model{}, compact.Models[0], compact.Models[0])
	assets := []*formats.Asset{compact, &broken, &duplicate}
	for _, a := range assets {
		requests = append(requests, []any{"models", exporttest.PyJSON(t, a)})
	}
	raw := oracle.Exec(t, graphPython, requests).MustValue(t)
	var results []batchResult
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	for i, label := range labels {
		ordered, err := c4dexport.OrderedGraph(graphs[label])
		var ids []string
		for _, node := range ordered {
			ids = append(ids, node.ID)
		}
		compare(t, "graph "+label, results[i], ids, err)
	}
	for k, a := range assets {
		models, err := c4dexport.AssetModels(a)
		compare(t, fmt.Sprintf("asset models %d", k), results[len(labels)+k], models, err)
	}
}

// unbakedMaterials is the API's no-texture material set: descriptors with
// their maps removed (dict(value, maps={})).
func unbakedMaterials(asset *formats.Asset) *c4dexport.Materials {
	materials := ordered.New[*ordered.Map[*formats.Material]]()
	for _, model := range asset.Models {
		descriptors := ordered.New[*formats.Material]()
		model.Materials.Range(func(key string, value *formats.Material) bool {
			copied := *value
			copied.Maps = ordered.New[*formats.MaterialMap]()
			descriptors.Set(key, &copied)
			return true
		})
		materials.Set(model.ID, descriptors)
	}
	return materials
}

func compare(t *testing.T, label string, want batchResult, got any, err error) {
	t.Helper()
	if !want.OK {
		if err == nil || err.Error() != want.Error {
			t.Errorf("%s: got error %v, want %q", label, err, want.Error)
		}
		return
	}
	if err != nil {
		t.Errorf("%s: %v", label, err)
		return
	}
	jsoncmp.Equal(t, want.Result, got, jsoncmp.Default)
}

// The unchanged Cinema 4D writer accepts the Go payloads and builds the same
// scenes as from Python's payloads.
const nativePython = `
import json, sys, tempfile
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0, args[0])
from test_c4d_scene import make_api
from owpreview.c4d_scene import build_scene
from owpreview.c4d_export import make_payload, make_set_payload
out = []
for go_payload, kind, call_args, kwargs in args[1]:
    reports = []
    for payload in (json.loads(go_payload), (make_set_payload if kind == 'set' else make_payload)(*call_args, **kwargs)):
        native = make_api()
        with tempfile.TemporaryDirectory() as temp, patch.dict(sys.modules, {'c4d': native, 'c4d.modules': native.modules}):
            report = build_scene(payload, Path(temp) / 'scene.c4d')
        names = []
        def visit(obj, depth):
            names.append((depth, obj.name, len(getattr(obj, 'tracks', []))))
            for child in obj.children:
                visit(child, depth + 1)
        for obj in native.saved[0].roots:
            visit(obj, 0)
        reports.append({'report': report, 'objects': names})
    out.append(reports)
result = out
`

func TestNativeSceneAcceptsGoPayloads(t *testing.T) {
	type entry struct {
		label   string
		payload *c4dexport.Payload
		request oracleCase
	}
	var entries []entry
	add := func(c oracleCase) {
		value, err := c.run()
		if err != nil {
			t.Fatalf("%s: %v", c.label, err)
		}
		entries = append(entries, entry{c.label, value.(*c4dexport.Payload), c})
	}
	asset, clip := exporttest.SelectedFixture(t)
	gun := c4dexport.DefaultPayloadOptions()
	gun.ScopeModelID = ptr("gun")
	add(payloadCase(t, "selected gun", asset, clip, nil, gun))
	add(payloadCase(t, "whole entity", asset, clip, unbakedMaterials(asset), c4dexport.DefaultPayloadOptions()))
	setAsset, list := exporttest.AssignedFixture(t)
	add(setCase(t, "set", setAsset, assignments(list), nil, c4dexport.DefaultSetOptions()))
	noMesh := c4dexport.DefaultSetOptions()
	noMesh.IncludeMesh = false
	add(setCase(t, "set without meshes", setAsset, assignments(list), nil, noMesh))
	root := filepath.Join(exporttest.TestsDir(), "demo-assets")
	demo := loadEntity(t, root, "Entities/PreviewRobot/PreviewRobot.owentity")
	add(payloadCase(t, "demo", demo, loadClip(t, filepath.Join(root, "Animations/003_Root_Move.owanimclip")), nil, c4dexport.DefaultPayloadOptions()))

	var requests []any
	for _, e := range entries {
		data, err := json.Marshal(e.payload)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, []any{string(data), e.request.kind, e.request.args, e.request.kwargs})
	}
	raw := oracle.Exec(t, nativePython, exporttest.TestsDir(), requests).MustValue(t)
	var results [][2]struct {
		Report  map[string]any  `json:"report"`
		Objects json.RawMessage `json:"objects"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		goReport, pyReport := results[i][0], results[i][1]
		if goReport.Report["verifiedNativeScene"] != true {
			t.Errorf("%s: not verified: %v", e.label, goReport.Report)
		}
		a, _ := json.Marshal(goReport.Report)
		jsoncmp.Equal(t, a, pyReport.Report, jsoncmp.Default)
		jsoncmp.Equal(t, goReport.Objects, pyReport.Objects, jsoncmp.Options{})
	}
	// tests/test_combined_export.py native checks on the Go payload.
	set := results[2][0].Report
	for key, want := range map[string]float64{"meshCount": 3, "boneCount": 6, "skinCount": 3, "trackCount": 36, "frameCount": 31} {
		if set[key] != want {
			t.Errorf("set %s = %v, want %v", key, set[key], want)
		}
	}
	noMeshes := results[3][0].Report
	if noMeshes["meshCount"] != 0.0 || noMeshes["boneCount"] != 6.0 || noMeshes["trackCount"] != 36.0 {
		t.Errorf("set without meshes: %v", noMeshes)
	}
	selected := results[0][0].Report
	for key, want := range map[string]float64{"meshCount": 1, "boneCount": 2, "skinCount": 1, "trackCount": 18} {
		if selected[key] != want {
			t.Errorf("selected %s = %v, want %v", key, selected[key], want)
		}
	}
}

// Port of test_unit_normals_keep_tags_and_follow_native_polygon_corners and
// test_repaired_normal_follows_native_mirrored_polygon_corners: c4d_scene._mesh
// receives Go's normalized normals.
func TestNativeMeshNormalTagsFollowPolygonCorners(t *testing.T) {
	authored := meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, []uint32{0, 1, 2},
		[][3]float64{{.1, .2, .96}, {-.3, .9, .1}, {.7, -.2, -.6}})
	repairedMesh := meshFixture([][3]float64{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, []uint32{0, 1, 2},
		[][3]float64{{0, 0, 0}, {0, 2, 2}, {3, 0, 4}})
	var meshes []*formats.Mesh
	for _, mesh := range []*formats.Mesh{authored, repairedMesh} {
		result, err := c4dexport.NormalizedNormalAsset(normalAsset(mesh))
		if err != nil {
			t.Fatal(err)
		}
		meshes = append(meshes, result.Models[0].Meshes[0])
	}
	raw := oracle.Exec(t, `
import json, sys
sys.path.insert(0, args[0])
from test_c4d_scene import make_api
from owpreview.c4d_scene import _mesh
out = []
for mesh in args[1]:
    obj = _mesh(make_api(), json.loads(mesh))
    out.append({'polygons': obj.polygons, 'tags': len(obj.tags), 'normals': obj.tags[-1].data[0]})
result = out
`, exporttest.TestsDir(), []string{mustJSON(t, meshes[0]), mustJSON(t, meshes[1])}).MustValue(t)
	var results []struct {
		Polygons [][4]int              `json:"polygons"`
		Tags     int                   `json:"tags"`
		Normals  map[string][3]float64 `json:"normals"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	if results[0].Tags != 1 {
		t.Fatal("Retain the authored Normal tag.")
	}
	n := [][3]float64{{.1, .2, .96}, {-.3, .9, .1}, {.7, -.2, -.6}}
	for k, v := range n {
		f := [3]float64{float64(float32(v[0])), float64(float32(v[1])), float64(float32(v[2]))}
		l := math.Sqrt(f[0]*f[0] + f[1]*f[1] + f[2]*f[2])
		n[k] = [3]float64{f[0] / l, f[1] / l, -f[2] / l}
	}
	s := 1 / math.Sqrt(2)
	for i, want := range []map[string][3]float64{
		{"a": n[0], "b": n[2], "c": n[1], "d": n[1]},
		{"a": {0, 0, -1}, "b": {.6, 0, -.8}, "c": {0, s, -s}, "d": {0, s, -s}},
	} {
		if results[i].Polygons[0] != [4]int{0, 2, 1, 1} {
			t.Fatal(results[i].Polygons)
		}
		for corner, value := range want {
			for c := 0; c < 3; c++ {
				if math.Abs(results[i].Normals[corner][c]-value[c]) > 1e-7 {
					t.Fatalf("mesh %d corner %s = %v, want %v", i, corner, results[i].Normals[corner], value)
				}
			}
		}
	}
}

// Benchmark: make_payload Go vs Python on a large rig and clip.
const benchmarkPython = `
import time
from owpreview.formats import load_asset, load_clip
from owpreview.c4d_export import make_payload
asset = load_asset(args[0], args[1])
clip = load_clip(args[2])
best = None
for _ in range(3):
    start = time.perf_counter()
    payload = make_payload(asset, clip, {}, include_mesh=True)
    elapsed = time.perf_counter() - start
    best = elapsed if best is None else min(best, elapsed)
result = {'seconds': best, 'tracks': len(payload['animation']['tracks']),
          'frames': payload['animation']['lastFrame'] + 1}
`

func benchmarkInputs(t testing.TB) (entity, root, clipPath string) {
	library := "/tmp/claude-0/-home-user-Avolition/e70be9b6-149e-503d-a8ec-7b456af224a1/scratchpad/lib/Heroes/Big/Skin0"
	if entities, _ := filepath.Glob(filepath.Join(library, "Entities", "*", "*.owentity")); len(entities) > 0 {
		if clips, _ := filepath.Glob(filepath.Join(library, "Animations", "*.owanimclip")); len(clips) > 0 {
			return entities[0], library, clips[0]
		}
	}
	dir := t.TempDir()
	model := ft.Write(t, dir, "Models/Big.00C/Big.owmdl", ft.LargeModel(200_000, 120))
	clip := ft.Write(t, dir, "Animations/Big.owanimclip", ft.LargeClip(120, 300))
	return model, dir, clip
}

// TestMakePayloadBenchmarkVsPython times both implementations on the same
// large input. Run with -run MakePayloadBenchmark -v (skipped with -short).
func TestMakePayloadBenchmarkVsPython(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark")
	}
	if os.Getenv("AVOLITION_BENCH") == "" {
		t.Skip("set AVOLITION_BENCH=1 to compare make_payload timings with Python")
	}
	entity, root, clipPath := benchmarkInputs(t)
	asset, err := formats.LoadAsset(entity, root)
	if err != nil {
		t.Fatal(err)
	}
	clip := loadClip(t, clipPath)
	var best time.Duration
	var payload *c4dexport.Payload
	for i := 0; i < 3; i++ {
		start := time.Now()
		payload, err = c4dexport.MakePayload(asset, clip, nil, c4dexport.DefaultPayloadOptions())
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); best == 0 || elapsed < best {
			best = elapsed
		}
	}
	start := time.Now()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	encode := time.Since(start)
	raw := oracle.Exec(t, benchmarkPython, entity, root, clipPath).MustValue(t)
	var python struct {
		Seconds float64 `json:"seconds"`
		Tracks  int     `json:"tracks"`
		Frames  int     `json:"frames"`
	}
	if err := json.Unmarshal(raw, &python); err != nil {
		t.Fatal(err)
	}
	t.Logf("make_payload %s: %d tracks x %d frames; Go %v (JSON %v, %d MB), Python %.3fs (%.1fx)",
		filepath.Base(clipPath), len(payload.Animation.Tracks), payload.Animation.LastFrame+1, best, encode,
		len(data)>>20, python.Seconds, python.Seconds/best.Seconds())
}

func BenchmarkMakePayload(b *testing.B) {
	entity, root, clipPath := benchmarkInputs(b)
	asset, err := formats.LoadAsset(entity, root)
	if err != nil {
		b.Fatal(err)
	}
	clip, err := formats.LoadClip(clipPath)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c4dexport.MakePayload(asset, clip, nil, c4dexport.DefaultPayloadOptions()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPayloadJSON(b *testing.B) {
	entity, root, clipPath := benchmarkInputs(b)
	asset, err := formats.LoadAsset(entity, root)
	if err != nil {
		b.Fatal(err)
	}
	clip, err := formats.LoadClip(clipPath)
	if err != nil {
		b.Fatal(err)
	}
	payload, err := c4dexport.MakePayload(asset, clip, nil, c4dexport.DefaultPayloadOptions())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c4dexport.AppendPayloadJSON(nil, payload); err != nil {
			b.Fatal(err)
		}
	}
}
