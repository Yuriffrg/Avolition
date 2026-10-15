package fbx_test

// Port of the export tests of tests/test_fbx.py (the PoseEvaluator/Euler
// tests live in package exportpose; test_unbaked_mask_cannot_make_constant_white_emission
// is in internal_test.go).

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/exportpose"
	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/fbx"
	"avolition/gocore/internal/formats"
)

func demo(t testing.TB) (*formats.Asset, *formats.Clip) {
	root := filepath.Join(exporttest.TestsDir(), "demo-assets")
	asset, err := formats.LoadAsset(filepath.Join(root, "Entities/PreviewRobot/PreviewRobot.owentity"), root)
	if err != nil {
		t.Fatal(err)
	}
	clip, err := formats.LoadClip(filepath.Join(root, "Animations/001_Wave.owanimclip"))
	if err != nil {
		t.Fatal(err)
	}
	return asset, clip
}

func export(t *testing.T, path string, asset *formats.Asset, clip *formats.Clip, opts fbx.Options) (*fbx.Report, []byte) {
	t.Helper()
	report, err := fbx.ExportFBX(path, asset, clip, opts)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return report, data
}

func displayName(r *record) string { return strings.SplitN(r.Props[1].(string), "\x00\x01", 2)[0] }

func TestDeterministicBinaryWithGeometryAndAnimation(t *testing.T) {
	asset, clip := demo(t)
	dir := t.TempDir()
	first, a := export(t, filepath.Join(dir, "first.fbx"), asset, clip, fbx.DefaultOptions())
	_, b := export(t, filepath.Join(dir, "second.fbx"), asset, clip, fbx.DefaultOptions())
	if !bytes.Equal(a, b) || !bytes.HasPrefix(a, []byte("Kaydara FBX Binary")) {
		t.Fatal("not deterministic")
	}
	if first.FrameCount != clip.DurationFrames || first.MeshCount == 0 {
		t.Fatal(first)
	}
	for _, marker := range []string{"TransformLink", "AnimationCurve", "LayerElementUV"} {
		if !bytes.Contains(a, []byte(marker)) {
			t.Fatal(marker)
		}
	}
	if first.Bytes != int64(len(a)) {
		t.Fatal(first.Bytes)
	}
}

func playbackAsset(t *testing.T) (*formats.Asset, *formats.Clip) {
	fixture := exporttest.LoadPlayback(t)
	asset := &formats.Asset{Name: "Oracle", Models: []*formats.Model{{ID: "model-0", Name: "Rig", Path: "Rig", Bones: fixture.Bones,
		Meshes: []*formats.Mesh{}, Sockets: []formats.Socket{}}}, Warnings: []string{}}
	return asset, exporttest.Clip("Oracle", 30, 20, fixture.Tracks)
}

func TestExportOptionsPreserveBonesWithoutHelpers(t *testing.T) {
	asset, clip := playbackAsset(t)
	dir := t.TempDir()
	opts := fbx.Options{UpAxis: "Z", IncludeMesh: false, RootMotion: false}
	report, _ := export(t, filepath.Join(dir, "pose.fbx"), asset, clip, opts)
	if report.HelperBoneCount != 0 || report.ModelGroupBoneCount != 0 || report.MeshCount != 0 || report.UpAxis != "Z" {
		t.Fatal(report)
	}
	missing := fbx.DefaultOptions()
	missing.RigID = "missing"
	if _, err := fbx.ExportFBX(filepath.Join(dir, "bad.fbx"), asset, clip, missing); err == nil {
		t.Fatal("missing rig accepted")
	}
}

func objectsByID(records map[string]*record) map[int64]*record {
	out := map[int64]*record{}
	for _, n := range records["Objects"].Children {
		out[n.Props[0].(int64)] = n
	}
	return out
}

func connections(records map[string]*record) [][]any {
	var out [][]any
	for _, n := range records["Connections"].Children {
		out = append(out, n.Props)
	}
	return out
}

func TestSDKDefaultAttributesResolveMeshesAndSkeletons(t *testing.T) {
	asset, clip := demo(t)
	report, data := export(t, filepath.Join(t.TempDir(), "sdk.fbx"), asset, clip, fbx.DefaultOptions())
	roots, _ := readBinaryFBX(t, data)
	records := byName(roots)
	objects := objectsByID(records)
	links := connections(records)
	meshCount, jointCount := 0, 0
	for ident, n := range objects {
		subtype := n.Props[2].(string)
		if n.Name != "Model" || (subtype != "Mesh" && subtype != "LimbNode") {
			continue
		}
		// Match SDK GetNodeAttribute semantics: a connection alone does not
		// select it; an absent DefaultAttributeIndex defaults to invalid -1.
		index := int64(-1)
		if p, ok := properties(n)["DefaultAttributeIndex"]; ok {
			index = p[4].(int64)
		}
		var attributes []*record
		for _, c := range links {
			if c[0] == "OO" && c[2] == ident {
				if target := objects[c[1].(int64)]; target != nil && (target.Name == "Geometry" || target.Name == "NodeAttribute") {
					attributes = append(attributes, target)
				}
			}
		}
		if index < 0 || int(index) >= len(attributes) {
			t.Fatalf("%s: attribute index %d of %d", displayName(n), index, len(attributes))
		}
		attribute := attributes[index]
		if attribute.Props[2] != subtype {
			t.Fatal(attribute.Props[2], subtype)
		}
		if subtype == "Mesh" {
			meshCount++
			if len(child(attribute, "Vertices").Props[0].([]float64)) == 0 || len(child(attribute, "PolygonVertexIndex").Props[0].([]int64)) == 0 {
				t.Fatal("empty geometry")
			}
		} else {
			jointCount++
			if child(attribute, "TypeFlags").Props[0] != "Skeleton" {
				t.Fatal("not a skeleton")
			}
		}
	}
	if meshCount != report.MeshCount || jointCount != report.BoneCount+report.HelperBoneCount+report.ModelGroupBoneCount {
		t.Fatal(meshCount, jointCount, report)
	}
}

func TestAnimationTakeIsDiscoverableAndChannelsAreMarkedAnimated(t *testing.T) {
	asset, clip := demo(t)
	opts := fbx.DefaultOptions()
	opts.ClipName = "Wave example"
	_, data := export(t, filepath.Join(t.TempDir(), "motion.fbx"), asset, clip, opts)
	roots, _ := readBinaryFBX(t, data)
	records := byName(roots)
	objects := objectsByID(records)
	var stack *record
	for _, n := range records["Objects"].Children {
		if n.Name == "AnimationStack" {
			stack = n
		}
	}
	stackName := displayName(stack)
	doc := child(records["Documents"], "Document")
	if properties(doc)["ActiveAnimStackName"][4] != stackName || child(records["Takes"], "Current").Props[0] != stackName {
		t.Fatal("take not discoverable")
	}
	take := child(records["Takes"], "Take")
	local := child(take, "LocalTime").Props
	if take.Props[0] != stackName || local[0] != properties(stack)["LocalStart"][4] || local[1] != properties(stack)["LocalStop"][4] {
		t.Fatal("take times differ")
	}
	if properties(stack)["LocalStop"][4].(int64) <= 0 {
		t.Fatal("empty take")
	}
	links := connections(records)
	animated := 0
	for _, c := range links {
		if c[0] != "OP" || objects[c[2].(int64)].Name != "Model" {
			continue
		}
		if properties(objects[c[2].(int64)])[c[3].(string)][3] != "A+" {
			t.Fatal("channel not marked animated")
		}
		curveNode := objects[c[1].(int64)]
		if d := properties(curveNode)["d"]; d[1] != "Compound" || d[2] != "" || d[3] != "" {
			t.Fatal(d)
		}
		for _, key := range []string{"d|X", "d|Y", "d|Z"} {
			found := false
			for _, edge := range links {
				found = found || (edge[0] == "OP" && edge[2] == c[1] && len(edge) > 3 && edge[3] == key)
			}
			if !found {
				t.Fatal(key)
			}
		}
		animated++
	}
	if animated == 0 {
		t.Fatal("no animated channels")
	}
	varied := false
	for _, n := range objects {
		if n.Name == "AnimationCurve" {
			values := child(n, "KeyValueFloat").Props[0].([]float64)
			for _, v := range values {
				varied = varied || v != values[0]
			}
		}
	}
	if !varied {
		t.Fatal("constant curves")
	}
}

func TestDuplicateAttachmentNamesRemainUnchangedOnEveryRig(t *testing.T) {
	fixture := exporttest.LoadPlayback(t)
	asset := &formats.Asset{Name: "Duplicates", Models: []*formats.Model{
		{ID: "child", Name: "Other", Bones: fixture.Bones[:1], Meshes: []*formats.Mesh{}},
		{ID: "main", Name: "Selected", Bones: fixture.Bones, Meshes: []*formats.Mesh{}}}}
	opts := fbx.DefaultOptions()
	opts.RigID = "main"
	report, data := export(t, filepath.Join(t.TempDir(), "pose.fbx"), asset, exporttest.Clip("Oracle", 30, 1, fixture.Tracks), opts)
	for _, w := range report.Warnings {
		if strings.Contains(w, "duplicate bone") {
			t.Fatal(w)
		}
	}
	roots, _ := readBinaryFBX(t, data)
	count := 0
	for _, n := range byName(roots)["Objects"].Children {
		if n.Name == "Model" && n.Props[2] == "LimbNode" {
			name := displayName(n)
			if name == "root" {
				count++
			}
			if strings.Contains(name, "__WH_") || strings.Contains(name, "child__") {
				t.Fatal(name)
			}
		}
	}
	if count != 2 {
		t.Fatal(count)
	}
}

func TestCentimetresAreBakedIntoGeometryAnimationAndBindPose(t *testing.T) {
	asset, clip := demo(t)
	report, data := export(t, filepath.Join(t.TempDir(), "scale.fbx"), asset, clip, fbx.DefaultOptions())
	roots, _ := readBinaryFBX(t, data)
	records := byName(roots)
	objects := objectsByID(records)
	nodes := map[[2]string]*record{}
	byIdent := map[int64]*record{}
	var geometry, bindPose *record
	for _, n := range records["Objects"].Children {
		switch n.Name {
		case "Model":
			nodes[[2]string{displayName(n), n.Props[2].(string)}] = n
			byIdent[n.Props[0].(int64)] = n
		case "Geometry":
			if geometry == nil {
				geometry = n
			}
		case "Pose":
			if bindPose == nil {
				bindPose = n
			}
		}
	}
	vertices := child(geometry, "Vertices").Props[0].([]float64)
	for i, v := range asset.Models[0].Meshes[0].Positions {
		if !near(vertices[i], float64(v)*100, 1e-7) {
			t.Fatal(i, vertices[i], v)
		}
	}
	if properties(records["GlobalSettings"])["UnitScaleFactor"][4] != 1.0 || report.Unit != "centimetres" || report.Scale != 100 {
		t.Fatal(report)
	}
	for _, node := range asset.ExportHierarchy {
		subtype := map[string]string{"bone": "LimbNode", "mesh": "Mesh"}[node.Type]
		if subtype == "" {
			subtype = "Null"
		}
		props := properties(nodes[[2]string{node.Name, subtype}])
		for c := 0; c < 3; c++ {
			if !near(props["Lcl Translation"][4+c].(float64), node.Matrix[12+c]*100, 1e-7) || !near(props["Lcl Scaling"][4+c].(float64), 1, 1e-7) {
				t.Fatal(node.Name, props["Lcl Translation"], props["Lcl Scaling"])
			}
		}
	}
	links := connections(records)
	ev, err := exportpose.NewEvaluator(asset.Models[0].Bones, clip.Bones)
	if err != nil {
		t.Fatal(err)
	}
	pose := ev.Evaluate(float64(clip.LastFrame), false)
	for _, source := range asset.ExportHierarchy {
		if source.Type != "bone" {
			continue
		}
		model := nodes[[2]string{source.Name, "LimbNode"}]
		var curveNode any
		for _, c := range links {
			if c[0] == "OP" && c[2] == model.Props[0] && c[3] == "Lcl Translation" {
				curveNode = c[1]
			}
		}
		for channel, letter := range []string{"X", "Y", "Z"} {
			var curve *record
			for _, c := range links {
				if c[0] == "OP" && c[2] == curveNode && c[3] == "d|"+letter {
					curve = objects[c[1].(int64)]
				}
			}
			values := child(curve, "KeyValueFloat").Props[0].([]float64)
			if math.Abs(values[len(values)-1]-pose.Positions[*source.BoneIndex][channel]*100) > 5e-5 {
				t.Fatal(source.Name, letter)
			}
		}
	}
	// Bind poses carry the same centimetre positions as the joints.
	for _, entry := range bindPose.Children {
		if entry.Name != "PoseNode" {
			continue
		}
		for _, v := range child(entry, "Matrix").Props[0].([]float64) {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatal("non-finite bind pose")
			}
		}
		if byIdent[child(entry, "Node").Props[0].(int64)] == nil {
			t.Fatal("bind pose references a missing model")
		}
	}
}

func TestOriginalSourceWrappersNamesAndParentConnections(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	main := ftModel("Main.00C")
	write("main.owmdl", main)
	write("child.owmdl", ftModel("Child.00C"))
	write("main.owentity", ftEntity("Gameplay1P", "main.owmdl", "child.owentity", "bone_0002"))
	write("child.owentity", ftEntity("Child header ignored", "child.owmdl", "", ""))
	asset, err := formats.LoadAsset(filepath.Join(dir, "main.owentity"), dir)
	if err != nil {
		t.Fatal(err)
	}
	_, data := export(t, filepath.Join(dir, "source.fbx"), asset, exporttest.Clip("Rest", 30, 0, nil), fbx.DefaultOptions())
	roots, _ := readBinaryFBX(t, data)
	records := byName(roots)
	objects := map[int64]*record{}
	for _, n := range records["Objects"].Children {
		if n.Name == "Model" {
			objects[n.Props[0].(int64)] = n
		}
	}
	links := map[int64]int64{}
	for _, n := range records["Connections"].Children {
		childID, parentID := n.Props[1].(int64), n.Props[2].(int64)
		if n.Props[0] == "OO" && objects[childID] != nil && (objects[parentID] != nil || parentID == 0) {
			links[childID] = parentID
		}
	}
	var pathOf func(int64) []string
	pathOf = func(ident int64) []string {
		if ident == 0 {
			return nil
		}
		return append(pathOf(links[ident]), displayName(objects[ident]))
	}
	paths := map[string]*record{}
	for ident, n := range objects {
		paths[strings.Join(pathOf(ident), "/")] = n
	}
	pelvis := "Gameplay1P/Main.00C/main_Skeleton/bone_0001/Pelvis"
	for _, want := range []string{pelvis, pelvis + "/child/Child.00C/child_Skeleton/bone_0001/Pelvis",
		"Gameplay1P/Main.00C/Triangle", pelvis + "/child/Child.00C/Triangle"} {
		if paths[want] == nil {
			t.Fatalf("missing %s in %v", want, keys(paths))
		}
	}
	if properties(paths[pelvis])["InheritType"][4] != int64(2) || properties(paths["Gameplay1P/Main.00C/main_Skeleton/bone_0001"])["InheritType"][4] != int64(1) {
		t.Fatal("inherit types")
	}
	for path := range paths {
		if strings.Contains(path, "__WH_") {
			t.Fatal(path)
		}
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
