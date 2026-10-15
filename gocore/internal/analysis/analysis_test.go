package analysis

// Port of tests/test_animation_analysis.py. Every analyze() fixture is also
// run through the Python implementation, and the inventory text must be
// byte-identical (json.dumps(..., ensure_ascii=False, indent=2)) apart from
// analyzedAt.
//
// Not ported here (BrowserAPI level, ported with the API):
// test_api_fresh_scan_does_not_automatically_analyze_without_cache and
// test_api_rescan_leaves_dormant_analysis_idle_until_explicitly_requested.
// test_api_exclusions_remove_only_managed_directory_and_tracked_copy and
// test_api_detail_uses_requested_root_when_scanned_roots_overlap are ported
// at the manager level (the exclusion and root callbacks the API passes).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"avolition/gocore/internal/evidence"
	et "avolition/gocore/internal/evidence/evidencetest"
	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/oracle"
)

type fixture struct {
	t          *testing.T
	base, root string
}

func newFixture(t *testing.T) *fixture {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "Export with spaces 非ASCII")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return &fixture{t, base, root}
}

func (f *fixture) write(relative string, content []byte) string {
	return ft.Write(f.t, f.root, relative, content)
}

func (f *fixture) clip(relative string, bones []ft.ClipBone, duration uint32) string {
	if relative == "" {
		relative = "animations/00000000007B.owanimclip"
	}
	c := ft.NewClip()
	if bones != nil {
		c.Bones = bones
	}
	if duration != 0 {
		c.Duration = duration
	}
	return f.write(relative, c.Bytes())
}

func hexValue(reference string) uint32 {
	value, err := strconv.ParseUint(reference[:len(reference)-4], 16, 32)
	if err != nil {
		panic(err)
	}
	return uint32(value)
}

func (f *fixture) model(reference string, bones []ft.Bone, prefix string) string {
	if reference == "" {
		reference = "000000000123.00C"
	}
	m := ft.NewModel()
	m.Mesh, m.Name = false, reference
	if bones != nil {
		m.Bones = bones
	}
	blob := m.Bytes()
	offset := 4 + len(ft.String("")) + len(ft.String(reference))
	copy(blob[offset:], ft.Pack("I", hexValue(reference)))
	stem := reference[:len(reference)-4]
	return f.write(prefix+"Models/"+reference+"/"+stem+".owmdl", blob)
}

func (f *fixture) entity(reference, model string, children ...string) string {
	e := ft.NewEntity()
	e.Name, e.Model = reference, model
	for _, c := range children {
		e.Children = append(e.Children, ft.Child{Reference: c})
	}
	blob := e.Bytes()
	offset := len(ft.String("owentity")) + 4 + len(ft.String(reference)) + len(ft.String(model)) + len(ft.String("null")) + 4
	copy(blob[offset:], ft.Pack("I", hexValue(model)))
	return f.write("Entities/"+reference+"/"+reference+".owentity", blob)
}

func (f *fixture) referenceCompanion(index uint32, modelReference, relative string) string {
	stem := fmt.Sprintf("%012X", index)
	target := `Models\` + modelReference + `\AnimationEffects\` + stem + `\` + stem + ".oweffect"
	if relative == "" {
		relative = "Entities/E/AnimationEffects/" + stem + ".owanim"
	}
	return f.write(relative, et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, index, 30, 1), ft.String(target)))
}

func (f *fixture) exactGUIDChain(folder string, fullGUID uint64, duration uint32) string {
	index := uint32(fullGUID & 0xFFFFFFFF)
	stem := fmt.Sprintf("%012X", index)
	clip := f.clip(folder+"/"+stem+".owanimclip", nil, duration)
	companion := et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, index, 30, 0))
	companion = et.Cat(companion, ft.String(filepath.Base(clip)), ft.String("unavailable.owmdl"), et.DefaultEffectHeader())
	f.write(folder+"/motion.owanim", companion)
	effect := et.Cat(et.EffectHeader([7]int32{1, 0, 0, 0, 0, 0, 0}, [2]uint16{1, 2}), et.Playback(true))
	effect = et.Cat(effect, ft.Pack("QQQ", fullGUID, 0, 0), ft.String("null"), ft.String("motion.owanim"))
	f.write(folder+"/use.oweffect", effect)
	return clip
}

func guid(index uint32, engine uint64, upperKey uint16) uint64 {
	return et.GUID(index, 6, engine, upperKey)
}

const analyzePython = `
import json
from owpreview import animation_analysis as analysis
inventory = analysis.analyze(args[0], annotations=args[1], installed_toolchain=args[2], labels_available=args[3])
result = json.dumps(inventory, ensure_ascii=False, indent=2)
`

var analyzedAt = regexp.MustCompile(`"analyzedAt": "[^"]*"`)

// analyze runs Analyze and checks the inventory text against Python's.
func (f *fixture) analyze(opts Options) *Inventory {
	f.t.Helper()
	inventory, err := Analyze(f.root, opts)
	if err != nil {
		f.t.Fatal(err)
	}
	got, err := Encode(inventory)
	if err != nil {
		f.t.Fatal(err)
	}
	annotations, _ := normalizeJSON(opts.Annotations)
	if opts.Annotations == nil {
		annotations = json.RawMessage("{}")
	}
	installed, _ := normalizeJSON(opts.InstalledToolchain)
	var want string
	if err := json.Unmarshal(oracle.Exec(f.t, analyzePython, f.root, annotations, installed, !opts.LabelsUnavailable).MustValue(f.t), &want); err != nil {
		f.t.Fatal(err)
	}
	normalize := func(text string) string { return analyzedAt.ReplaceAllString(text, `"analyzedAt": ""`) }
	if normalize(string(got)) != normalize(want) {
		gotLines, wantLines := strings.Split(normalize(string(got)), "\n"), strings.Split(normalize(want), "\n")
		for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
			if gotLines[i] != wantLines[i] {
				f.t.Fatalf("inventory differs from Python at line %d:\nwant %s\ngot  %s", i+1, wantLines[i], gotLines[i])
			}
		}
		f.t.Fatalf("inventory differs from Python: %d vs %d lines", len(wantLines), len(gotLines))
	}
	// The persisted form round-trips through the typed structs.
	var back Inventory
	if err := json.Unmarshal(got, &back); err != nil {
		f.t.Fatal(err)
	}
	if again, err := Encode(&back); err != nil || string(again) != string(got) {
		f.t.Fatalf("cache round trip changed the inventory: %v", err)
	}
	return inventory
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func TestMissingMetadataStaysUnknownAndTrackDefinitionIsExplicit(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	result := f.analyze(Options{})
	asset := result.Assets[0]
	if asset.SourceName != nil || asset.Resource.FullGUID != nil || result.GameBuild != nil || result.DataToolVersion != nil ||
		fmt.Sprint(asset.Statuses) != "[unknown]" || *asset.TrackCount != 1 || *asset.TransformChannelCount != 3 ||
		!strings.Contains(asset.TrackDefinition, "bone records") {
		t.Fatalf("%+v", asset)
	}
}

func TestSameBytesDifferentShortResourceIDsAreSeparateAssets(t *testing.T) {
	f := newFixture(t)
	f.clip("A/00000000007B.owanimclip", nil, 0)
	f.clip("B/00000000007C.owanimclip", nil, 0)
	result := f.analyze(Options{})
	if result.AssetCount != 2 || len(result.ContentGroups) != 1 || len(result.ContentGroups[0].AssetIDs) != 2 {
		t.Fatalf("%+v", result.ContentGroups)
	}
}

func TestSameShortFilenameDifferentFullGUIDsStaySeparate(t *testing.T) {
	f := newFixture(t)
	one, two := guid(123, 0, 0), guid(123, 15, 0)
	f.exactGUIDChain("one", one, 31)
	f.exactGUIDChain("two", two, 31)
	result := f.analyze(Options{})
	if result.AssetCount != 2 {
		t.Fatal(result.AssetCount)
	}
	guids := map[string]bool{}
	for _, a := range result.Assets {
		guids[*a.Resource.FullGUID] = true
		if len(a.Evidence) == 0 {
			t.Fatal("no evidence")
		}
	}
	if !guids[fmt.Sprintf("%016X", one)] || !guids[fmt.Sprintf("%016X", two)] {
		t.Fatal(guids)
	}
}

func TestConflictingFullGUIDReferenceChainsRemainUnresolved(t *testing.T) {
	f := newFixture(t)
	f.exactGUIDChain("one", guid(123, 0, 0), 31)
	effect := et.Cat(et.EffectHeader([7]int32{1, 0, 0, 0, 0, 0, 0}, [2]uint16{1, 2}), et.Playback(true))
	effect = et.Cat(effect, ft.Pack("QQQ", guid(123, 15, 0), 0, 0), ft.String("null"), ft.String("motion.owanim"))
	f.write("one/conflicting.oweffect", effect)
	result := f.analyze(Options{})
	asset := result.Assets[0]
	if asset.Resource.FullGUID != nil || !strings.Contains(strings.Join(asset.Conflicts, "|"), "Multiple full GUID") {
		t.Fatalf("%+v", asset)
	}
}

func TestSameShortFilenameDifferentPayloadsAreSeparateAssets(t *testing.T) {
	f := newFixture(t)
	f.clip("A/00000000007B.owanimclip", nil, 31)
	f.clip("B/00000000007B.owanimclip", nil, 61)
	if result := f.analyze(Options{}); result.AssetCount != 2 {
		t.Fatal(result.AssetCount)
	}
}

func TestShortResourceScopeChangesBetweenExportRoots(t *testing.T) {
	f := newFixture(t)
	resource := ExportedResource("00000000007B.owanimclip")
	same := "same"
	a, _ := LocalIdentity(f.root, resource, &same, "")
	b, _ := LocalIdentity(filepath.Join(f.base, "Other"), resource, &same, "")
	if a == b {
		t.Fatal(a)
	}
	const python = `
from owpreview import animation_analysis as analysis
result = analysis.local_identity(args[0], analysis.exported_resource('00000000007B.owanimclip'), 'same')
`
	if want := oracle.Exec(t, python, f.root).MustValue(t); string(want) != strconv.Quote(a) {
		t.Fatalf("want %s, got %s", want, a)
	}
	bad := resource
	bad.FullGUID = ptr("123")
	if _, err := LocalIdentity(f.root, bad, &same, ""); err == nil || err.Error() != "A full GUID must be a lossless 16-hex-character string." {
		t.Fatal(err)
	}
}

func TestDuplicateOccurrencesKeepDistinctNamedContexts(t *testing.T) {
	f := newFixture(t)
	f.clip("Heroes/Widowmaker/HighlightIntro/Default/Test Intro/Animations/00000000007B.owanimclip", nil, 0)
	f.clip("Heroes/Widowmaker/Emote/Default/Test Emote/Animations/00000000007B.owanimclip", nil, 0)
	result := f.analyze(Options{})
	if result.AssetCount != 1 || result.OccurrenceCount != 2 {
		t.Fatal(result.AssetCount, result.OccurrenceCount)
	}
	asset := result.Assets[0]
	labels := map[string]bool{}
	for _, c := range asset.Contexts {
		labels[c.Label] = true
		if !strings.Contains(c.Role, "actor role unknown") {
			t.Fatal(c.Role)
		}
	}
	if !labels["Test Intro"] || !labels["Test Emote"] || len(labels) != 2 || result.Coverage.NamedContexts != 1 || asset.SourceName != nil {
		t.Fatal(labels, result.Coverage)
	}
	if result.Occurrences[0].Contexts[0].ID == result.Occurrences[1].Contexts[0].ID {
		t.Fatal("contexts merged")
	}
}

func TestGenericHighlightIntroEntityFolderIsNotANamedUnlock(t *testing.T) {
	f := newFixture(t)
	f.clip("Entities/HighlightIntro/Animations/00000000007B.owanimclip", nil, 0)
	result := f.analyze(Options{})
	if len(result.Contexts) != 0 || result.Coverage.NamedContexts != 0 {
		t.Fatal(result.Contexts)
	}
}

func TestExplicitPathKeepsPriorityGroupWithoutAssigningAction(t *testing.T) {
	f := newFixture(t)
	f.model("", nil, "")
	f.clip("Models/000000000123.00C/Animations/1/32/00000000007B.owanimclip", nil, 0)
	result := f.analyze(Options{})
	occurrence, asset := result.Occurrences[0], result.Assets[0]
	if occurrence.Priority.String() != "1" || occurrence.Group.String() != "32" || asset.Models[0].Kind != "explicit-export-path" ||
		asset.SourceName != nil || !contains(asset.Statuses, "unknown") {
		t.Fatalf("%+v", asset)
	}
}

func threeBones() []ft.Bone {
	return []ft.Bone{{Name: "bone_0001", Parent: -1}, {Name: "bone_0002", Parent: 0}, {Name: "bone_0003", Parent: 0}}
}

func TestPartialBodyAndMultipleSkeletonCandidatesAreRetained(t *testing.T) {
	f := newFixture(t)
	f.model("000000000123.00C", nil, "")
	f.model("000000000124.00C", threeBones(), "")
	f.clip("", nil, 0)
	asset := f.analyze(Options{}).Assets[0]
	if len(asset.Models) != 2 {
		t.Fatal(len(asset.Models))
	}
	counts := map[int]bool{}
	for _, m := range asset.Models {
		if m.MatchedBoneCount != 1 {
			t.Fatal(m.MatchedBoneCount)
		}
		counts[m.SkeletonBoneCount] = true
	}
	if !counts[2] || !counts[3] || !contains(asset.Statuses, "suggested") || !contains(asset.Statuses, "unknown") || len(asset.Conflicts) == 0 {
		t.Fatalf("%+v", asset)
	}
}

func TestBoneArrayPositionsAreNotUsedAsIdentifiers(t *testing.T) {
	f := newFixture(t)
	f.model("", []ft.Bone{{Name: "bone_9000", Parent: -1}, {Name: "bone_9001", Parent: 0}}, "")
	f.clip("", nil, 0)
	asset := f.analyze(Options{}).Assets[0]
	if len(asset.Models) != 0 || asset.SkeletonComparisons[0].MatchedBoneCount != 0 {
		t.Fatalf("%+v", asset)
	}
}

func TestUnanimatedBoneRecordsDoNotCountAsSkeletonEvidence(t *testing.T) {
	f := newFixture(t)
	f.model("", nil, "")
	f.clip("", []ft.ClipBone{{Name: "bone_0002"}}, 0)
	asset := f.analyze(Options{}).Assets[0]
	if *asset.TrackCount != 1 || len(asset.AnimatedBoneIDs) != 0 || len(asset.Models) != 0 {
		t.Fatalf("%+v", asset)
	}
}

func TestDanglingEffectStillRetainsScopedModelReference(t *testing.T) {
	f := newFixture(t)
	f.model("", nil, "")
	f.clip("", nil, 0)
	f.referenceCompanion(123, "000000000123.00C", "")
	result := f.analyze(Options{})
	asset := result.Assets[0]
	if !result.Companions[0].ReferenceMissing || asset.Models[0].Kind != "entity-reference" || asset.Resource.FullGUID != nil || asset.SourceName != nil {
		t.Fatalf("%+v", asset)
	}
}

func TestPartialReferenceCannotReachOtherExportSubtree(t *testing.T) {
	f := newFixture(t)
	f.model("", nil, "First/")
	f.model("", nil, "Second/")
	f.clip("Second/animations/00000000007B.owanimclip", nil, 0)
	f.referenceCompanion(123, "000000000123.00C", "First/Entities/E/AnimationEffects/00000000007B.owanim")
	result := f.analyze(Options{})
	if len(result.Companions[0].CandidateOccurrences) != 0 {
		t.Fatal(result.Companions[0].CandidateOccurrences)
	}
	for _, m := range result.Assets[0].Models {
		if m.Kind == "entity-reference" {
			t.Fatal("entity reference crossed subtrees")
		}
	}
}

func TestFullReferenceKeyDisagreementIsAConflictNotIdentity(t *testing.T) {
	f := newFixture(t)
	f.exactGUIDChain("one", guid(123, 0, 0xABCD), 31)
	asset := f.analyze(Options{}).Assets[0]
	if asset.Resource.FullGUID != nil || !strings.Contains(strings.Join(asset.Conflicts, "|"), "disagrees with clip key/type") {
		t.Fatalf("%+v", asset)
	}
}

func animatedBone(name string) ft.ClipBone {
	return ft.ClipBone{Name: name, Tracks: []ft.Track{{Kind: 0, Keys: []ft.Key{ft.K(0, 0, 0, 0)}}}}
}

func TestExplicitModelIsPreservedWhenOnlySomeBonesMatch(t *testing.T) {
	f := newFixture(t)
	f.model("", []ft.Bone{{Name: "bone_0001", Parent: -1}}, "")
	f.clip("Models/000000000123.00C/Animations/0/0/00000000007B.owanimclip",
		[]ft.ClipBone{animatedBone("bone_0001"), animatedBone("bone_0075")}, 0)
	model := f.analyze(Options{}).Assets[0].Models[0]
	if model.Kind != "explicit-export-path" || model.MatchedBoneCount != 1 || model.AnimatedBoneCount != 2 ||
		fmt.Sprint(model.MissingBoneIDs) != "[bone_0075]" {
		t.Fatalf("%+v", model)
	}
}

func TestAmbiguousPartialIndexDoesNotChoosePayload(t *testing.T) {
	f := newFixture(t)
	f.model("", nil, "")
	f.clip("animations/00000000007B.owanimclip", nil, 0)
	f.clip("Models/000000000123.00C/Animations/0/0/00000000007B.owanimclip", nil, 61)
	f.referenceCompanion(123, "000000000123.00C", "")
	result := f.analyze(Options{})
	if !result.Companions[0].Ambiguous {
		t.Fatal("not ambiguous")
	}
	for _, a := range result.Assets {
		for _, m := range a.Models {
			if m.Kind == "entity-reference" {
				t.Fatal("association chosen")
			}
		}
		if !strings.Contains(strings.Join(a.Conflicts, "|"), "Ambiguous partial") {
			t.Fatal(a.Conflicts)
		}
	}
}

func TestExportedEntityModelAndChildConventionsResolve(t *testing.T) {
	f := newFixture(t)
	f.model("00000000013E.00C", nil, "")
	f.model("0000000001D4.00C", nil, "")
	f.entity("Gameplay1P", "00000000013E.00C", "00000000002A.003")
	f.entity("00000000002A.003", "0000000001D4.00C")
	f.clip("", nil, 0)
	result := f.analyze(Options{})
	var parent, child *EntityRow
	for _, e := range result.Entities {
		switch e.Name {
		case "Gameplay1P":
			parent = e
		case "00000000002A.003":
			child = e
		}
	}
	if len(parent.ModelIDs) != 1 || fmt.Sprint(parent.ChildEntityIDs) != "["+child.ID+"]" || len(child.ModelIDs) != 1 {
		t.Fatalf("%+v %+v", parent, child)
	}
}

func TestParseErrorsSeparateFromMissingMetadata(t *testing.T) {
	f := newFixture(t)
	f.write("animations/00000000007B.owanimclip", []byte("broken"))
	result := f.analyze(Options{})
	if len(result.ParseErrors) != 1 || len(result.Assets[0].ParseErrors) != 1 || result.Assets[0].TrackCount != nil {
		t.Fatalf("%+v", result.ParseErrors)
	}
}

func TestNicknamesAndAssetsUnchangedByAnalysis(t *testing.T) {
	f := newFixture(t)
	path := f.clip("", nil, 0)
	before, _ := os.ReadFile(path)
	labels := map[string]any{Key(path): map[string]any{"name": "Reload arms", "notes": "User context only", "favorite": true}}
	snapshot, _ := json.Marshal(labels)
	result := f.analyze(Options{Annotations: labels})
	after, _ := os.ReadFile(path)
	again, _ := json.Marshal(labels)
	if string(before) != string(after) || string(snapshot) != string(again) {
		t.Fatal("inputs changed")
	}
	if result.Occurrences[0].Nickname.Value != "Reload arms" || result.Assets[0].SourceName != nil ||
		result.Coverage.UserVerified != 0 || *result.Coverage.UserLabels != 1 {
		t.Fatalf("%+v", result.Coverage)
	}
}

func TestUnavailableLabelsAndInstalledVersionAreNotExportFacts(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	result := f.analyze(Options{InstalledToolchain: map[string]any{"version": "fixture installed version", "n": json.Number("1.50")},
		LabelsUnavailable: true})
	if result.Coverage.UserLabels != nil || result.DataToolVersion != nil ||
		!strings.Contains(string(result.InstalledToolchain), `"version":"fixture installed version"`) {
		t.Fatalf("%+v", result.Coverage)
	}
}

func TestCompanionsEntitiesEffectsAndProvenanceMatchPython(t *testing.T) {
	// A denser fixture exercising every reader and resolution path at once.
	f := newFixture(t)
	f.model("00000000013E.00C", nil, "")
	f.model("000000000123.00C", threeBones(), "")
	f.entity("Gameplay1P", "00000000013E.00C", "00000000002A.003", "Missing.003")
	f.entity("00000000002A.003", "000000000123.00C")
	f.entity("Wrong", "0000000001D4.00C")
	f.clip("Models/000000000123.00C/Animations/1/32/00000000007B.owanimclip", nil, 0)
	f.clip("Models/000000000123.00C/Animations/x/٣/00000000007C.owanimclip", []ft.ClipBone{animatedBone("bone_0003")}, 0)
	f.clip("Heroes/Ana/VictoryPose/Default/Pose/00000000007D.owanimclip", nil, 0)
	f.clip("animations/named clip.owanimclip", nil, 0)
	f.referenceCompanion(0x7B, "000000000123.00C", "")
	f.referenceCompanion(0x7C, "000000000123.00C", "Models/000000000123.00C/AnimationEffects/00000000007C/00000000007C.owanim")
	f.exactGUIDChain("chain", guid(0x99, 3, 0), 45)
	f.write("Models/000000000123.00C/AnimationEffects/00000000007C/00000000007C.oweffect", et.DefaultEffectHeader())
	f.write("broken.owanim", []byte("x"))
	f.write("broken.oweffect", et.Cat(et.DefaultEffectHeader(), []byte("tail")))
	f.write("export-manifest.json", []byte(`{"build": "fixture"}`))
	f.write(".build.info", []byte("info"))
	f.write("notes.txt", []byte("ignored"))
	f.write(".git/ignored.owanimclip", []byte("ignored"))
	f.analyze(Options{Annotations: map[string]any{"unused": map[string]any{"name": "x"}}})
}

func TestMissingRootIsRejected(t *testing.T) {
	_, err := Analyze(filepath.Join(t.TempDir(), "absent"), Options{})
	if err == nil || err.Error() != "Scan an existing export folder before analyzing its library." {
		t.Fatal(err)
	}
}

func TestDemoAndMaterialAssetsMatchPython(t *testing.T) {
	for _, name := range []string{"demo-assets", "material-assets"} {
		root, _ := filepath.Abs(filepath.Join("..", "..", "..", "tests", name))
		f := &fixture{t: t, root: root}
		inventory := f.analyze(Options{})
		summary := Summarize(inventory)
		const python = `
from owpreview import animation_analysis as analysis
result = analysis.index_summary(analysis.analyze(args[0]))
`
		want := oracle.Exec(t, python, root).MustValue(t)
		var wantSummary, gotSummary any
		got, _ := json.Marshal(summary)
		_ = json.Unmarshal(want, &wantSummary)
		_ = json.Unmarshal(got, &gotSummary)
		if fmt.Sprint(wantSummary) != fmt.Sprint(gotSummary) {
			t.Fatalf("%s summary:\nwant %s\ngot  %s", name, want, got)
		}
	}
}

func TestBenchmarkLibraryMatchesPython(t *testing.T) {
	root := os.Getenv("AVOLITION_BENCH_LIBRARY")
	if root == "" {
		t.Skip("AVOLITION_BENCH_LIBRARY not set")
	}
	// The inventory is large (hundreds of MB), so compare digests of the
	// text with analyzedAt blanked.
	const python = `
import hashlib, json, re
from owpreview import animation_analysis as analysis
text = json.dumps(analysis.analyze(args[0]), ensure_ascii=False, indent=2)
text = re.sub(r'"analyzedAt": "[^"]*"', '"analyzedAt": ""', text, count=1)
result = hashlib.sha256(text.encode('utf-8')).hexdigest()
`
	want := oracle.Exec(t, python, root).MustValue(t)
	inventory, err := Analyze(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	inventory.AnalyzedAt = ""
	hash := sha256.New()
	if err := WriteJSON(hash, inventory); err != nil {
		t.Fatal(err)
	}
	if got := strconv.Quote(hex.EncodeToString(hash.Sum(nil))); got != string(want) {
		t.Fatalf("inventory digest: want %s, got %s", want, got)
	}
}

// Manager tests.

func (f *fixture) runManager(m *Manager, cachedOnly bool) Status {
	f.t.Helper()
	if _, err := m.Start(f.root, cachedOnly); err != nil {
		f.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := m.Status(f.root, false)
		if err != nil {
			f.t.Fatal(err)
		}
		if !status.Busy {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("Analysis worker did not finish within five seconds")
	return Status{}
}

func TestBackgroundWorkerDoesNotBlockOrStartDuplicateJob(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{})
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.snapshot = func(root string, excluded []string, progress func(string)) (*Snapshot, map[string]string, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
			return nil, nil, errors.New("test release timed out")
		}
		return snapshotWith(root, excluded, progress)
	}
	first, err := m.Start(f.root, false)
	if err != nil || !first.Busy {
		t.Fatal(first, err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot did not start")
	}
	if second, _ := m.Start(f.root, false); !second.Busy || calls.Load() != 1 {
		t.Fatal(second, calls.Load())
	}
	if m.Index(f.root) != nil {
		t.Fatal("index while busy")
	}
	close(release)
	if status := f.runManager(m, false); status.State != "Completed" {
		t.Fatal(status)
	}
	m.Wait()
}

func TestManagerCacheInvalidatesBytesEvenWhenStatIsUnchanged(t *testing.T) {
	f := newFixture(t)
	path := f.clip("", nil, 0)
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{})
	first := f.runManager(m, false)
	stamp, _ := os.Stat(path)
	content, _ := os.ReadFile(path)
	copy(content[len(content)-4:], ft.Pack("f", 2.0))
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), stamp.ModTime()); err != nil {
		t.Fatal(err)
	}
	second := f.runManager(m, false)
	if first.State != "Completed" || *first.Revision == *second.Revision {
		t.Fatal(first, second)
	}
}

func TestManagerCacheInvalidatesAggregateParserAndProvenanceMetadata(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{})
	first := *f.runManager(m, false).Revision
	f.write("export-manifest.json", []byte(`{"build":"unverified fixture"}`))
	second := *f.runManager(m, false).Revision
	if first == second {
		t.Fatal("provenance file ignored")
	}
	saved := ParserVersion
	ParserVersion = "fixture next parser"
	third := *f.runManager(m, false).Revision
	ParserVersion = saved
	if second == third {
		t.Fatal("parser version ignored")
	}
}

func TestManagerCacheInvalidatesEvidenceParserVersion(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{})
	first := *f.runManager(m, false).Revision
	saved := evidence.ParserVersion
	evidence.ParserVersion = "fixture next evidence parser"
	second := *f.runManager(m, false).Revision
	evidence.ParserVersion = saved
	if first == second {
		t.Fatal("evidence parser version ignored")
	}
}

func TestCacheHitAndDetailDoNotOverwriteFreshNickname(t *testing.T) {
	f := newFixture(t)
	path := f.clip("", nil, 0)
	name := atomic.Value{}
	name.Store("First")
	annotations := func() any { return map[string]any{Key(path): map[string]any{"name": name.Load()}} }
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{Annotations: annotations})
	if status := f.runManager(m, false); status.State != "Completed" {
		t.Fatal(status)
	}
	m.analyze = func(string, Options, map[string]string) (*Inventory, error) {
		return nil, errors.New("cache should be reused")
	}
	if status := f.runManager(m, false); status.State != "Completed" {
		t.Fatal(status)
	}
	name.Store("Edited since analysis")
	detail := m.Detail(path, nil)
	if detail.Occurrence.Nickname.Value != "Edited since analysis" {
		t.Fatal(detail.Occurrence.Nickname)
	}
	// The cached inventory itself is not modified.
	if m.records[Key(f.root)].inventory.Occurrences[0].Nickname.Value != "First" {
		t.Fatal("inventory modified")
	}
	// A Python-written cache file is reused as-is too.
	var cached Inventory
	data, _ := os.ReadFile(filepath.Join(m.directory(f.root), "animation-index.json"))
	if err := json.Unmarshal(data, &cached); err != nil || cached.Revision != *m.records[Key(f.root)].status.Revision {
		t.Fatal(err)
	}
}

func TestPythonCacheFileIsReusedAndGoCacheFileIsReusedByPython(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	cache := filepath.Join(f.base, "cache")
	const python = `
import time
from owpreview import animation_analysis as analysis
manager = analysis.AnalysisManager(args[1])
manager.start(args[0])
while manager.status(args[0], check_cache=False)['busy']:
    time.sleep(0.01)
original = analysis.analyze
def fail(*a, **k):
    raise AssertionError('analyze called')
analysis.analyze = fail
second = analysis.AnalysisManager(args[1])
second.start(args[0])
while second.status(args[0], check_cache=False)['busy']:
    time.sleep(0.01)
result = second.status(args[0], check_cache=False)
`
	// Python writes the cache; Go must reuse it without analyzing.
	want := oracle.Exec(t, python, f.root, cache).MustValue(t)
	m := NewManager(cache, ManagerOptions{})
	m.analyze = func(string, Options, map[string]string) (*Inventory, error) {
		return nil, errors.New("Python cache should be reused")
	}
	status := f.runManager(m, false)
	var pythonStatus Status
	_ = json.Unmarshal(want, &pythonStatus)
	if status.State != "Completed" || pythonStatus.State != "Completed" || *status.Revision != *pythonStatus.Revision {
		t.Fatalf("go %+v python %s", status, want)
	}
	// Go writes the cache; Python must reuse it (its second manager fails
	// if it analyzes).
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	m = NewManager(cache, ManagerOptions{})
	f.runManager(m, false)
	const reuse = `
import time
from owpreview import animation_analysis as analysis
def fail(*a, **k):
    raise AssertionError('analyze called')
analysis.analyze = fail
manager = analysis.AnalysisManager(args[1])
manager.start(args[0])
while manager.status(args[0], check_cache=False)['busy']:
    time.sleep(0.01)
result = manager.status(args[0], check_cache=False)
`
	var reused Status
	_ = json.Unmarshal(oracle.Exec(t, reuse, f.root, cache).MustValue(t), &reused)
	if reused.State != "Completed" || *reused.Revision != *m.records[Key(f.root)].status.Revision {
		t.Fatalf("%+v", reused)
	}
}

func TestFailedRefreshNeverPresentsOldCacheAsCurrent(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{})
	if status := f.runManager(m, false); status.State != "Completed" {
		t.Fatal(status)
	}
	m.snapshot = func(string, []string, func(string)) (*Snapshot, map[string]string, error) {
		return nil, nil, &formats.OSError{Message: "unavailable"}
	}
	status := f.runManager(m, false)
	if status.State != "Failed" || *status.Error != "unavailable" || status.Revision != nil {
		t.Fatal(status)
	}
	if m.Index(f.root) != nil {
		t.Fatal("old index presented")
	}
}

func TestCachedOnlyStatusWithoutCacheStaysReady(t *testing.T) {
	f := newFixture(t)
	f.clip("", nil, 0)
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{})
	status, err := m.Status(f.root, true)
	if err != nil || status.State != "Checking cache" || !status.Busy {
		t.Fatal(status, err)
	}
	m.Wait()
	status, _ = m.Status(f.root, true)
	if status.State != "Ready" || status.Message != "Analyze this library to inspect local evidence." || m.Index(f.root) != nil {
		t.Fatal(status)
	}
	if _, err := m.Status(filepath.Join(f.base, "absent"), true); err == nil ||
		err.Error() != "The library folder is unavailable. Scan an existing folder first." {
		t.Fatal(err)
	}
}

func TestManagerExclusionsRemoveOnlyManagedDirectoryAndTrackedCopy(t *testing.T) {
	f := newFixture(t)
	original := f.clip("Originals/00000000007B.owanimclip", nil, 0)
	existing := f.clip("Existing destination/00000000007C.owanimclip", nil, 0)
	tracked := f.clip("Existing destination/nicknamed-copy.owanimclip", nil, 0)
	managed := f.clip("Managed destination/nicknamed-copy.owanimclip", nil, 0)
	annotations := func() any {
		return map[string]any{Key(existing): map[string]any{"name": "Keep this original"}}
	}
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{Annotations: annotations,
		Excluded: func() []string { return []string{Key(filepath.Dir(managed)), Key(tracked)} }})
	if status := f.runManager(m, false); status.State != "Completed" {
		t.Fatal(status)
	}
	index := m.Index(f.root)
	paths := map[string]bool{}
	for _, entry := range index.Entries {
		paths[entry.Path] = true
	}
	if len(paths) != 2 || !paths[original] || !paths[existing] {
		t.Fatal(paths)
	}
	root := f.root
	if detail := m.Detail(existing, &root); detail.Occurrence.Nickname.Value != "Keep this original" {
		t.Fatal(detail.Occurrence.Nickname)
	}
	if m.Detail(tracked, &root) != nil || m.Detail(managed, &root) != nil {
		t.Fatal("excluded file analyzed")
	}
	for _, p := range []string{original, existing, tracked, managed} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagerDetailUsesRequestedRootWhenRootsOverlap(t *testing.T) {
	f := newFixture(t)
	path := f.clip("Nested/animations/00000000007B.owanimclip", nil, 0)
	nested := &fixture{t: t, base: f.base, root: filepath.Join(f.root, "Nested")}
	m := NewManager(filepath.Join(f.base, "cache"), ManagerOptions{})
	if status := f.runManager(m, false); status.State != "Completed" {
		t.Fatal(status)
	}
	root := f.root
	broad := m.Detail(path, &root)
	if status := nested.runManager(m, false); status.State != "Completed" {
		t.Fatal(status)
	}
	narrow := m.Detail(path, &nested.root)
	if broad.Asset.ID == narrow.Asset.ID || narrow.Occurrence.RelativePath != "animations/00000000007B.owanimclip" {
		t.Fatal(broad.Asset.ID, narrow.Occurrence.RelativePath)
	}
	if m.Detail(path, &root).Asset.ID != broad.Asset.ID {
		t.Fatal("root ignored")
	}
	detail, _ := json.Marshal(m.Detail(path, nil))
	const python = `
import time
from owpreview import animation_analysis as analysis
manager = analysis.AnalysisManager(args[2])
for root in (args[0], args[1]):
    manager.start(root)
    while manager.status(root, check_cache=False)['busy']:
        time.sleep(0.01)
result = manager.detail(args[3])
`
	want := oracle.Exec(t, python, f.root, nested.root, filepath.Join(f.base, "python-cache"), path).MustValue(t)
	var wantDetail, gotDetail map[string]any
	_ = json.Unmarshal(want, &wantDetail)
	_ = json.Unmarshal(detail, &gotDetail)
	delete(wantDetail, "cache")
	delete(gotDetail, "cache")
	wantText, _ := json.Marshal(wantDetail)
	gotText, _ := json.Marshal(gotDetail)
	if string(wantText) != string(gotText) {
		t.Fatalf("detail differs:\nwant %s\ngot  %s", wantText, gotText)
	}
}
