package evidence_test

// Port of tests/test_animation_evidence_formats.py, plus oracle tests that
// run owpreview.animation_evidence_formats on the same files.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/evidence"
	et "avolition/gocore/internal/evidence/evidencetest"
	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/pyjson"
)

func entityBlob(minor uint16, count int32) []byte {
	blob := et.Cat(ft.String("owentity"), ft.Pack("HH", 2, minor))
	blob = et.Cat(blob, ft.String("Gameplay1P"), ft.String("00000000013E.00C"), ft.String("000000000123.00D"))
	blob = et.Cat(blob, ft.Pack("IIIi", 0x1D, 0x13E, 0x123, count))
	for i := int32(0); i < max(0, count); i++ {
		blob = et.Cat(blob, ft.String("child.003"), ft.Pack("QQII", uint64(0x0DC0000000000068), uint64(0xFD80000000001AFF), 0x68, 0x1AFF))
		blob = et.Cat(blob, ft.String("hardpoint_0068"))
	}
	if minor == 1 {
		blob = et.Cat(blob, ft.String("null"), ft.String("../.."))
	}
	return blob
}

func modelBlob(names []string, parents []int, version [2]uint16) []byte {
	if names == nil {
		names, parents = []string{"bone_0001", "bone_00A2"}, []int{-1, 0}
	}
	blob := et.Cat(ft.Pack("HH", version[0], version[1]), ft.String("look.owmat"), ft.String("model.00C"))
	blob = et.Cat(blob, ft.Pack("IHII", uint32(0xFFFFFFFF), len(names), 0, 0))
	for i, name := range names {
		blob = et.Cat(blob, ft.String(name), ft.Pack("h9f", parents[i], 0, 0, 0, 0, 0, 0, 0, 0, 0))
	}
	return blob
}

var v20 = [2]uint16{2, 0}

func write(t *testing.T, name string, blob []byte) string {
	t.Helper()
	return ft.Write(t, t.TempDir(), name, blob)
}

func isFormatError(err error) bool {
	var formatError *formats.FormatError
	return errors.As(err, &formatError)
}

func TestGUIDIsLosslessAboveJavaScriptSafeInteger(t *testing.T) {
	value := et.GUID(0xDEADBEEF, 6, 0xF, 0xABCD)
	result := evidence.DecodeGUID(value)
	if result.FullGUID != fmt.Sprintf("%016X", value) || result.Type != "006" || result.LongKey != "ABCDDEADBEEF" ||
		result.Index != "DEADBEEF" || result.Engine != "F" {
		t.Fatalf("%+v", result)
	}
	data, _ := json.Marshal(result)
	var back evidence.GUID
	if err := json.Unmarshal(data, &back); err != nil || back != result {
		t.Fatalf("round trip %+v %v", back, err)
	}
	parsed, err := evidence.ParseGUID("0x" + fmt.Sprintf("%016x", value))
	if err != nil || parsed != result {
		t.Fatalf("%+v %v", parsed, err)
	}
}

func TestSameShortDisplayCanHaveDifferentFullIdentity(t *testing.T) {
	a, b := evidence.DecodeGUID(et.GUID(123, 6, 0, 0)), evidence.DecodeGUID(et.GUID(123, 6, 1, 0))
	if a.Display != b.Display || a.FullGUID == b.FullGUID {
		t.Fatal(a, b)
	}
}

func TestGUIDRejectsPartialOrUnsafeInput(t *testing.T) {
	big := json.Number("18446744073709551616")
	for _, value := range []any{"000000000123.006", "123", 1.0, true, -1, big} {
		if _, err := evidence.DecodeGUIDValue(value); err == nil {
			t.Errorf("%v accepted", value)
		}
	}
	if result, err := evidence.DecodeGUIDValue(0); err != nil || !result.IsNull {
		t.Fatal(result, err)
	}
}

func TestModelPreservesBoneIdentifierSeparateFromArrayPosition(t *testing.T) {
	result, err := evidence.ReadModel(write(t, "model.owmdl", modelBlob(nil, nil, v20)))
	if err != nil {
		t.Fatal(err)
	}
	if result.Bones[1] != (evidence.Bone{Name: "bone_00A2", ParentIndex: 0}) || result.Index != "FFFFFFFF" ||
		result.FullGUID != nil || result.UnparsedBytes != 0 {
		t.Fatalf("%+v", result)
	}
}

func TestModelValidatesHeaderSkeletonAndCycles(t *testing.T) {
	blobs := [][]byte{
		modelBlob(nil, nil, [2]uint16{2, 7}),
		modelBlob([]string{"same", "same"}, []int{-1, 0}, v20),
		modelBlob(nil, []int{1, 0}, v20),
		modelBlob([]string{"bone_0001", "bone_00A2"}, []int{-1, 2}, v20),
	}
	full := modelBlob(nil, nil, v20)
	blobs = append(blobs, full[:len(full)-1])
	blobs[2] = modelBlob([]string{"bone_0001", "bone_00A2"}, []int{1, 0}, v20)
	for i, blob := range blobs {
		if _, err := evidence.ReadModel(write(t, "model.owmdl", blob)); !isFormatError(err) {
			t.Errorf("%d: %v", i, err)
		}
	}
}

func TestModelGeometryIsExplicitlyUnparsed(t *testing.T) {
	result, err := evidence.ReadModel(write(t, "model.owmdl", et.Cat(modelBlob(nil, nil, v20), []byte("unparsed geometry"))))
	if err != nil || result.UnparsedBytes != 17 {
		t.Fatal(result, err)
	}
}

func TestEntityKeepsFullChildIdentifiersAndShortIndicesDistinct(t *testing.T) {
	result, err := evidence.ReadEntity(write(t, "entity.owentity", entityBlob(1, 1)))
	if err != nil {
		t.Fatal(err)
	}
	child := result.Children[0]
	if result.ModelReference != "00000000013E.00C" || result.EffectReference != "000000000123.00D" ||
		child.Identifier.FullGUID != "FD80000000001AFF" || child.IdentifierIndex != "00001AFF" ||
		child.Hardpoint.FullGUID != "0DC0000000000068" || result.FullGUID != nil {
		t.Fatalf("%+v", result)
	}
}

func TestEntity20Omits21TailAndPreservesMissingReferences(t *testing.T) {
	result, err := evidence.ReadEntity(write(t, "entity.owentity", entityBlob(0, 0)))
	if err != nil || result.RelativeRoot != "../../" || result.MaterialLookReference != "" {
		t.Fatal(result, err)
	}
}

func TestEntityBadMagicVersionOrCountIsNotEvidence(t *testing.T) {
	full := entityBlob(1, 1)
	blobs := [][]byte{et.Cat([]byte{0}, entityBlob(1, 1)[9:]), entityBlob(9, 1), entityBlob(1, -1), full[:len(full)-3]}
	for i, blob := range blobs {
		if _, err := evidence.ReadEntity(write(t, "entity.owentity", blob)); !isFormatError(err) {
			t.Errorf("%d: %v", i, err)
		}
	}
}

func TestReferenceCompanionKeepsUnresolvedLiteralPath(t *testing.T) {
	target := `Models\枪 0000000001D4.00C\AnimationEffects\0000000001A6\0000000001A6.oweffect`
	blob := et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, 0x1A6, 30, 1), ft.String(target))
	result, err := evidence.ReadCompanion(write(t, "reference.owanim", blob))
	if err != nil {
		t.Fatal(err)
	}
	if result.AnimationIndex != "000001A6" || *result.EffectReference != target || result.Effect != nil || result.FullGUID != nil {
		t.Fatalf("%+v", result)
	}
}

func TestCompanionValidatesKindVersionFPSAndTruncation(t *testing.T) {
	cases := []struct {
		version [2]uint16
		fps     float64
		kind    int
	}{{[2]uint16{1, 9}, 30, 1}, {[2]uint16{1, 0}, 0, 1}, {[2]uint16{1, 0}, math.NaN(), 1}, {[2]uint16{1, 0}, 30, 2}}
	for _, c := range cases {
		blob := et.Cat(ft.String("owanim"), ft.Pack("HHIfi", c.version[0], c.version[1], 123, c.fps, c.kind), ft.String("x"))
		if _, err := evidence.ReadCompanion(write(t, "bad.owanim", blob)); !isFormatError(err) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func embeddedEffectCompanion() ([]byte, uint64, uint64) {
	animation, model, material, identifier := et.GUID(123, 6, 0, 0), et.GUID(456, 12, 0, 0), et.GUID(789, 8, 0, 0), et.GUID(44, 28, 0, 0)
	effect := et.EffectHeader([7]int32{1, 1, 0, 0, 0, 0, 0}, [2]uint16{1, 2})
	effect = et.Cat(effect, et.Playback(true), ft.Pack("QQQ", animation, material, model), ft.String("m.owmdl"), ft.String("a.owanim"))
	effect = et.Cat(effect, et.Playback(false), ft.Pack("BQQI", 7, animation, identifier, 44), ft.String("other.owanim"))
	blob := et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, 123, 30, 0))
	blob = et.Cat(blob, ft.String("Models/m/Animations/0/32/a.owanimclip"), ft.String("Models/m/m.owmdl"), effect)
	return blob, animation, identifier
}

func TestEmbeddedEffectRetainsFullGUIDRoleReferences(t *testing.T) {
	blob, animation, identifier := embeddedEffectCompanion()
	result, err := evidence.ReadCompanion(write(t, "data.owanim", blob))
	if err != nil {
		t.Fatal(err)
	}
	if *result.ClipReference != "Models/m/Animations/0/32/a.owanimclip" ||
		result.Effect.DMCE[0].Animation.FullGUID != fmt.Sprintf("%016X", animation) ||
		result.Effect.DMCE[0].Model.Type != "00C" || result.Effect.CECE[0].Action != 7 ||
		result.Effect.CECE[0].Identifier.FullGUID != fmt.Sprintf("%016X", identifier) {
		t.Fatalf("%+v", result)
	}
}

func otherRecordsEffect() ([]byte, uint64) {
	entity, model, material := et.GUID(33, 3, 0, 0), et.GUID(44, 12, 0, 0), et.GUID(55, 8, 0, 0)
	blob := et.EffectHeader([7]int32{0, 0, 1, 1, 2, 3, 1}, [2]uint16{1, 2})
	blob = et.Cat(blob, et.Playback(true), ft.Pack("QI", entity, 44), ft.String("entity.owentity"))
	blob = et.Cat(blob, et.Playback(true), ft.Pack("QQ", model, material), ft.String("model.owmdl"))
	blob = et.Cat(blob, et.Playback(true), ft.Pack("Iii", 19, 1, 2), ft.String("one.ogg"), ft.String("two.ogg"))
	return blob, entity
}

func TestEffectOtherRecordsAndOmittedSectionsKeepStreamAligned(t *testing.T) {
	blob, entity := otherRecordsEffect()
	result, err := evidence.ReadEffect(write(t, "effect.oweffect", blob))
	if err != nil {
		t.Fatal(err)
	}
	if result.NECE[0].Entity.FullGUID != fmt.Sprintf("%016X", entity) || result.RPCE[0].Model.Type != "00C" ||
		fmt.Sprint(result.SVCE[0].SoundReferences) != "[[one.ogg two.ogg]]" || len(result.Warnings) != 2 ||
		*result.TrailingBytes != 0 {
		t.Fatalf("%+v", result)
	}
}

func badEffects() [][]byte {
	return [][]byte{
		et.EffectHeader([7]int32{}, [2]uint16{1, 1}),
		et.EffectHeader([7]int32{-1, 0, 0, 0, 0, 0, 0}, [2]uint16{1, 2}),
		et.EffectHeader([7]int32{100001, 0, 0, 0, 0, 0, 0}, [2]uint16{1, 2}),
		et.EffectHeader([7]int32{1, 0, 0, 0, 0, 0, 0}, [2]uint16{1, 2}),
		et.Cat(et.EffectHeader([7]int32{0, 0, 0, 0, 0, 0, 1}, [2]uint16{1, 2}), et.Playback(true), ft.Pack("Ii", 0, -1)),
	}
}

func TestEffectRejectsUnsupportedOrUnboundedData(t *testing.T) {
	for i, blob := range badEffects() {
		if _, err := evidence.ReadEffect(write(t, "bad.oweffect", blob)); !isFormatError(err) {
			t.Errorf("%d: %v", i, err)
		}
	}
}

func TestUnknownTrailingBytesAreDiagnosticOnly(t *testing.T) {
	result, err := evidence.ReadEffect(write(t, "effect.oweffect", et.Cat(et.DefaultEffectHeader(), []byte("123"))))
	if err != nil || *result.TrailingBytes != 3 || len(result.Warnings) == 0 {
		t.Fatal(result, err)
	}
}

// Oracle: every reader on every fixture (valid and invalid) produces the same
// json.dumps text, or the same error message.

const readersPython = `
import json
from owpreview import animation_evidence_formats as ev
out = []
for name, path in args[0]:
    try:
        out.append({'ok': True, 'text': json.dumps(getattr(ev, name)(path), ensure_ascii=False)})
    except Exception as error:
        out.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
result = out
`

func TestReadersMatchPython(t *testing.T) {
	dir := t.TempDir()
	var calls [][2]string
	add := func(reader, name string, blob []byte) {
		calls = append(calls, [2]string{reader, ft.Write(t, dir, name, blob)})
	}
	full := modelBlob(nil, nil, v20)
	for i, blob := range [][]byte{full, et.Cat(full, []byte("geometry")), modelBlob(nil, nil, [2]uint16{2, 7}),
		modelBlob([]string{"same", "same"}, []int{-1, 0}, v20), modelBlob([]string{"a", "b"}, []int{1, 0}, v20),
		modelBlob([]string{"a", "b"}, []int{-1, 2}, v20), full[:len(full)-1], ft.NewModel().Bytes(),
		et.Cat(ft.Pack("HH", 2, 0), ft.String(""), ft.String(""), ft.Pack("IHII", 1, 0, 20000, 0))} {
		add("read_model", fmt.Sprintf("m%d.owmdl", i), blob)
	}
	entity := entityBlob(1, 1)
	for i, blob := range [][]byte{entity, entityBlob(0, 0), entityBlob(1, 3), et.Cat(entity, []byte("xy")),
		et.Cat([]byte{0}, entity[9:]), entityBlob(9, 1), entityBlob(1, -1), entity[:len(entity)-3]} {
		add("read_entity", fmt.Sprintf("e%d.owentity", i), blob)
	}
	companion, _, _ := embeddedEffectCompanion()
	reference := et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, 0x1A6, 29.97, 1), ft.String(`Models\枪\x.oweffect`))
	for i, blob := range [][]byte{companion, reference, et.Cat(reference, []byte{1, 2}),
		et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, 1, 1e-30, 1), ft.String("tiny")),
		et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 9, 1, 30, 1)),
		et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, 1, 1001, 1)),
		et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, 1, math.Inf(1), 1)),
		et.Cat(ft.String("owanim"), ft.Pack("HHIfi", 1, 0, 1, 30, 5)),
		companion[:len(companion)-2]} {
		add("read_companion", fmt.Sprintf("c%d.owanim", i), blob)
	}
	other, _ := otherRecordsEffect()
	effects := append([][]byte{other, et.DefaultEffectHeader(), et.Cat(et.DefaultEffectHeader(), []byte("123"))}, badEffects()...)
	effects = append(effects, et.Cat(et.EffectHeader([7]int32{0, 0, 1, 0, 0, 0, 0}, [2]uint16{1, 2}), ft.Pack("Bff", 2, 0, 0)),
		et.Cat(et.EffectHeader([7]int32{0, 0, 1, 0, 0, 0, 0}, [2]uint16{1, 2}), ft.Pack("Bff", 1, math.NaN(), 0)),
		et.Cat(ft.String("oweffect"), ft.Pack("HHIf7i", 1, 2, 1, -1.0, 0, 0, 0, 0, 0, 0, 0)))
	for i, blob := range effects {
		add("read_effect", fmt.Sprintf("f%d.oweffect", i), blob)
	}
	add("read_model", "missing/none.owmdl", nil)
	calls[len(calls)-1][1] = filepath.Join(dir, "absent.owmdl")

	args := make([]any, len(calls))
	for i, c := range calls {
		args[i] = []any{c[0], c[1]}
	}
	var want []struct {
		OK    bool   `json:"ok"`
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(oracle.Exec(t, readersPython, args).MustValue(t), &want); err != nil {
		t.Fatal(err)
	}
	for i, c := range calls {
		var value any
		var err error
		switch c[0] {
		case "read_model":
			value, err = evidence.ReadModel(c[1])
		case "read_entity":
			value, err = evidence.ReadEntity(c[1])
		case "read_companion":
			value, err = evidence.ReadCompanion(c[1])
		case "read_effect":
			value, err = evidence.ReadEffect(c[1])
		}
		label := c[0] + " " + filepath.Base(c[1])
		if !want[i].OK {
			if err == nil || err.Error() != want[i].Error {
				t.Errorf("%s: want error %q, got %v", label, want[i].Error, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", label, err)
			continue
		}
		got, err := pyjson.Dumps(value, pyjson.Options{Indent: -1})
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want[i].Text {
			t.Errorf("%s:\nwant %s\ngot  %s", label, want[i].Text, got)
		}
	}
	if !strings.Contains(fmt.Sprint(want), "true") {
		t.Fatal("no successful oracle calls")
	}
}
