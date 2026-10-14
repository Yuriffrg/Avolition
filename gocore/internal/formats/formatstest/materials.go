package formatstest

// Ports of tests/create_material_demo.py (material_bytes, look_bytes,
// png_bytes, textured_model, generate) and the parts of tests/create_demo.py
// they use (BONES, _cube, quaternion).

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"path/filepath"
	"testing"
)

// TextureRef, StaticInput and LookEntry are aliases of unnamed struct types
// so tests can write them positionally, like the Python tuples, without
// go vet's composites warning.

// TextureRef is one material_bytes texture: reference and binding hash.
type TextureRef = struct {
	Reference string
	Binding   uint32
}

// StaticInput is one material_bytes static input: key and raw value.
type StaticInput = struct {
	Key   uint32
	Value []byte
}

// MaterialBytes is material_bytes(textures, inputs, shader): an OWMAT 3.0
// material record.
func MaterialBytes(textures []TextureRef, inputs []StaticInput, shader uint32) []byte {
	blob := Pack("HHIQQI", 3, 0, 0, len(textures), len(inputs), shader)
	for _, texture := range textures {
		blob = append(blob, String(texture.Reference)...)
		blob = append(blob, Pack("I", texture.Binding)...)
	}
	for _, input := range inputs {
		blob = append(blob, Pack("II", input.Key, len(input.Value))...)
		blob = append(blob, input.Value...)
	}
	return blob
}

// LookEntry is one look_bytes entry: uint64 material key and reference.
type LookEntry = struct {
	Key       uint64
	Reference string
}

// LookBytes is look_bytes(entries): an OWMAT 3.0 model look.
func LookBytes(entries ...LookEntry) []byte {
	blob := Pack("HHIQ", 3, 0, 1, len(entries))
	for _, entry := range entries {
		blob = append(blob, Pack("Q", entry.Key)...)
		blob = append(blob, String(entry.Reference)...)
	}
	return blob
}

// PNGBytes is png_bytes(width, height, pixel): a deterministic RGBA PNG.
// (Its compressed bytes differ from Python's zlib output; the image is the
// same.)
func PNGBytes(width, height int, pixel func(x, y int) [4]byte) []byte {
	chunk := func(kind string, value []byte) []byte {
		out := binary.BigEndian.AppendUint32(nil, uint32(len(value)))
		out = append(out, kind...)
		out = append(out, value...)
		return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(append([]byte(kind), value...)))
	}
	var raw []byte
	for y := 0; y < height; y++ {
		raw = append(raw, 0)
		for x := 0; x < width; x++ {
			p := pixel(x, y)
			raw = append(raw, p[:]...)
		}
	}
	var compressed bytes.Buffer
	writer, _ := zlib.NewWriterLevel(&compressed, 9)
	writer.Write(raw)
	writer.Close()
	header := binary.BigEndian.AppendUint32(nil, uint32(width))
	header = binary.BigEndian.AppendUint32(header, uint32(height))
	header = append(header, 8, 6, 0, 0, 0)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", header)...)
	out = append(out, chunk("IDAT", compressed.Bytes())...)
	return append(out, chunk("IEND", nil)...)
}

// DemoBone is one create_demo.BONES entry.
type DemoBone struct {
	Name     string
	Parent   int
	Position [3]float64
}

// DemoBones is create_demo.BONES.
var DemoBones = []DemoBone{
	{"bone_0001", -1, [3]float64{0, 1, 0}},
	{"bone_0002", 0, [3]float64{-.36, .65, 0}},
	{"bone_0003", 0, [3]float64{.36, .65, 0}},
}

// Cube is create_demo._cube: one hard-normal box with consistent outward
// winding and rigid skinning.
func Cube(name string, center, size [3]float64, boneIndex int) []byte {
	cx, cy, cz := center[0], center[1], center[2]
	hx, hy, hz := size[0]/2, size[1]/2, size[2]/2
	faces := []struct {
		normal  [3]int // Python ints: negating 0 gives 0, not -0.0
		corners [4][3]float64
	}{
		{[3]int{1, 0, 0}, [4][3]float64{{1, -1, -1}, {1, 1, -1}, {1, 1, 1}, {1, -1, 1}}},
		{[3]int{-1, 0, 0}, [4][3]float64{{-1, -1, 1}, {-1, 1, 1}, {-1, 1, -1}, {-1, -1, -1}}},
		{[3]int{0, 1, 0}, [4][3]float64{{-1, 1, -1}, {-1, 1, 1}, {1, 1, 1}, {1, 1, -1}}},
		{[3]int{0, -1, 0}, [4][3]float64{{-1, -1, 1}, {-1, -1, -1}, {1, -1, -1}, {1, -1, 1}}},
		{[3]int{0, 0, 1}, [4][3]float64{{1, -1, 1}, {1, 1, 1}, {-1, 1, 1}, {-1, -1, 1}}},
		{[3]int{0, 0, -1}, [4][3]float64{{-1, -1, -1}, {-1, 1, -1}, {1, 1, -1}, {1, -1, -1}}},
	}
	var positions, normals []any
	var indices []any
	for faceIndex, face := range faces {
		for _, c := range face.corners {
			// OWLib exports +90 X: (x, -z, y). Reader undoes this once.
			positions = append(positions, cx+float64(c[0]*hx), -(cz + float64(c[2]*hz)), cy+float64(c[1]*hy))
			normals = append(normals, face.normal[0], -face.normal[2], face.normal[1])
		}
		start := faceIndex * 4
		indices = append(indices, start, start+1, start+2, start, start+2, start+3)
	}
	vertexCount := len(positions) / 3
	blob := String(name)
	blob = append(blob, Pack("QBIIB", boneIndex+1, 1, vertexCount, len(indices)/3, 1)...)
	blob = append(blob, Pack(fmt.Sprintf("%df", len(positions)), positions...)...)
	blob = append(blob, Pack(fmt.Sprintf("%df", len(normals)), normals...)...)
	repeat := func(count int, values ...any) []any {
		var out []any
		for i := 0; i < count; i++ {
			out = append(out, values...)
		}
		return out
	}
	blob = append(blob, Pack(fmt.Sprintf("%df", vertexCount*4), repeat(vertexCount, 1, 0, 0, 1)...)...)
	blob = append(blob, Pack(fmt.Sprintf("%df", vertexCount*2), repeat(6, 0, 0, 1, 0, 1, 1, 0, 1)...)...)
	blob = append(blob, Pack(fmt.Sprintf("%dh", vertexCount), repeat(vertexCount, boneIndex)...)...)
	blob = append(blob, Pack(fmt.Sprintf("%df", vertexCount), repeat(vertexCount, 1)...)...)
	blob = append(blob, Pack(fmt.Sprintf("%df", vertexCount*8), repeat(vertexCount*2, 1, 1, 1, 1)...)...)
	blob = append(blob, Pack(fmt.Sprintf("%dI", len(indices)), indices...)...)
	return blob
}

// TexturedModel is create_material_demo.textured_model: eight rigidly skinned
// boxes with two distinct UV sets and the look reference
// ModelLooks/Default.owmat.
func TexturedModel() []byte {
	meshes := []struct {
		name         string
		center, size [3]float64
		bone         int
	}{
		{"Body", [3]float64{0, 1.30, 0}, [3]float64{.65, .83, .32}, 0},
		{"Head", [3]float64{0, 1.99, 0}, [3]float64{.46, .38, .37}, 0},
		{"Left leg", [3]float64{-.16, .44, 0}, [3]float64{.22, .88, .25}, 0},
		{"Right leg", [3]float64{.16, .44, 0}, [3]float64{.22, .88, .25}, 0},
		{"Left arm", [3]float64{-.69, 1.65, 0}, [3]float64{.67, .22, .26}, 1},
		{"Left hand", [3]float64{-1.07, 1.65, 0}, [3]float64{.18, .27, .29}, 1},
		{"Right arm", [3]float64{.69, 1.65, 0}, [3]float64{.67, .22, .26}, 2},
		{"Right hand", [3]float64{1.07, 1.65, 0}, [3]float64{.18, .27, .29}, 2},
	}
	blob := Pack("HH", 2, 0)
	blob = append(blob, String("ModelLooks/Default.owmat")...)
	blob = append(blob, String("Material Robot (synthetic)")...)
	blob = append(blob, Pack("IHII", 1, len(DemoBones), len(meshes), 0)...)
	for _, bone := range DemoBones {
		blob = append(blob, String(bone.Name)...)
		blob = append(blob, Pack("h", bone.Parent)...)
		blob = append(blob, Pack("9f", bone.Position[0], bone.Position[1], bone.Position[2], 1, 1, 1, 0, 0, 0)...)
	}
	var uv2 []any
	for i := 0; i < 6; i++ {
		uv2 = append(uv2, .125, .125, .875, .125, .875, .875, .125, .875)
	}
	uv2Bytes := Pack("48f", uv2...)
	for _, mesh := range meshes {
		// Add a distinct second UV set to the original rigidly skinned box.
		cube := Cube(mesh.name, mesh.center, mesh.size, mesh.bone)
		header := len(String(mesh.name))
		cube[header+8] = 2
		uvEnd := header + 18 + 24*(12+12+16+8)
		blob = append(blob, cube[:uvEnd]...)
		blob = append(blob, uv2Bytes...)
		blob = append(blob, cube[uvEnd:]...)
	}
	return blob
}

// Quaternion is create_demo.quaternion(axis, degrees).
func Quaternion(axis int, degrees float64) []float64 {
	pi := math.Pi // float64 division, like math.radians
	angle := degrees * (pi / 180) / 2
	values := []float64{0, 0, 0, math.Cos(angle)}
	values[axis] = math.Sin(angle)
	return values
}

// GenerateMaterialDemo is create_material_demo.generate(root): the textured
// MaterialRobot fixture (tests/material-assets) written under root. It
// returns root.
func GenerateMaterialDemo(t testing.TB, root string) string {
	t.Helper()
	write := func(name string, data []byte) { Write(t, root, name, data) }
	const modelRoot = "Models/MaterialRobot.00C/"
	write(modelRoot+"MaterialRobot.owmdl", TexturedModel())
	entity := NewEntity()
	entity.Name, entity.Model = "Material Robot", "MaterialRobot.00C"
	write("Entities/MaterialRobot/MaterialRobot.owentity", entity.Bytes())
	write(modelRoot+"ModelLooks/Default.owmat", LookBytes(
		LookEntry{1, "../../Materials/000000B001.owmat"},
		LookEntry{2, "../../Materials/000000B002.owmat"},
		LookEntry{3, "../../Materials/000000B003.owmat"}))
	paths := func(entries ...TextureRef) []TextureRef {
		for i := range entries {
			entries[i].Reference = "../Textures/" + entries[i].Reference + ".png"
		}
		return entries
	}
	write(modelRoot+"Materials/000000B001.owmat", MaterialBytes(paths(
		TextureRef{"000000A001", 2903569922}, TextureRef{"000000A004", 378934698}, TextureRef{"000000A005", 548341454},
		TextureRef{"000000A006", 3335614873}, TextureRef{"000000A007", 3166598269}),
		[]StaticInput{{3250491852, Pack("f", 2)}}, 0))
	write(modelRoot+"Materials/000000B002.owmat", MaterialBytes(paths(
		TextureRef{"000000A002", 2903569922}, TextureRef{"000000A004", 378934698}, TextureRef{"000000A005", 548341454}),
		nil, 0))
	write(modelRoot+"Materials/000000B003.owmat", MaterialBytes(paths(
		TextureRef{"000000A003", 1239794147}, TextureRef{"000000A004", 378934698}, TextureRef{"000000A005", 1117188170}),
		nil, 51))
	gray := func(v byte) [4]byte { return [4]byte{v, v, v, 255} }
	pick := func(cond bool, a, b byte) byte {
		if cond {
			return a
		}
		return b
	}
	pixels := []struct {
		name  string
		pixel func(x, y int) [4]byte
	}{
		{"000000A001", func(x, y int) [4]byte {
			if (x/8+y/8)%2 != 0 {
				return [4]byte{220, 70, 45, 255}
			}
			return [4]byte{235, 225, 190, 255}
		}},
		{"000000A002", func(x, y int) [4]byte {
			if (x/4+y/4)%2 != 0 {
				return [4]byte{20, 170, 220, 255}
			}
			return [4]byte{25, 60, 80, 255}
		}},
		{"000000A003", func(x, y int) [4]byte { return [4]byte{185, 110, 230, pick(x%8 < 2, 0, 255)} }},
		{"000000A004", func(x, y int) [4]byte { return [4]byte{pick(x%8 < 4, 160, 96), pick(y%8 < 4, 160, 96), 32, 255} }},
		{"000000A005", func(x, y int) [4]byte { return [4]byte{pick(x < 16, 220, 100), pick(y < 16, 180, 60), 0, 255} }},
		{"000000A006", func(x, y int) [4]byte { return gray(pick((x/8+y/8)%2 != 0, 150, 255)) }},
		{"000000A007", func(x, y int) [4]byte { return gray(pick(13 <= y && y < 18, 35, 0)) }},
	}
	for _, p := range pixels {
		write(modelRoot+"Textures/"+p.name+".png", PNGBytes(32, 32, p.pixel))
	}
	keys := func(axis int, frames ...[2]float64) []Key {
		var out []Key
		for _, f := range frames {
			out = append(out, Key{uint32(f[0]), Quaternion(axis, f[1])})
		}
		return out
	}
	write("Animations/001_Wave.owanimclip", Clip{Bones: []ClipBone{{"bone_0003", []Track{{1, keys(2,
		[2]float64{0, 0}, [2]float64{15, 80}, [2]float64{30, 40}, [2]float64{45, 85}, [2]float64{60, 0})}}}},
		Duration: 61, FPS: 30, Version: [2]uint16{2, 0}}.Bytes())
	write("Animations/002_Turn.owanimclip", Clip{Bones: []ClipBone{{"bone_0001", []Track{{1, keys(1,
		[2]float64{0, 0}, [2]float64{30, 80}, [2]float64{60, 0}, [2]float64{90, -80}, [2]float64{120, 0})}}}},
		Duration: 121, FPS: 30, Version: [2]uint16{2, 0}}.Bytes())
	write("README.txt", []byte("ORIGINAL SYNTHETIC MATERIAL TEST ASSETS\n\n"+
		"Generated by tests/create_material_demo.py; no game artwork.\n"+
		"Load Entities/MaterialRobot/MaterialRobot.owentity with this folder as export root.\n"+
		"Eight rigidly skinned boxes, three bones, two clips, two distinct UV sets.\n"+
		"Body: red/cream checker, decoded normal, packed PBR, UV2 AO, masked tinted emission.\n"+
		"Left arm: cyan checker, decoded normal, packed PBR.\n"+
		"Right arm: lavender stripes with explicit hair alpha cutout and roughness map.\n"+
		"Textures deliberately use arbitrary IDs; OWMAT binding hashes select their role.\n"))
	return filepath.Clean(root)
}
