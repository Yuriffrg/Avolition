// Package fbx is the Go port of owpreview/fbx.py: a deterministic,
// standalone binary FBX 7.4 mesh/skin/animation exporter. (The FBX format is
// currently disabled in the UI but kept.)
//
// Encoding and element layout follow the installed Blender FBX exporter's public
// format conventions. No Blender runtime or Autodesk SDK is required. Geometric
// coordinates and animation translations are baked to centimetres. Native FBX
// Rrs inheritance preserves Overwatch's NONE scale inheritance without inserting
// helper joints or changing the original game hierarchy.
//
// # Python → Go map
//
//	_p / _Node          prop / node (node.go)
//	_value / _prop      value / setProp
//	_name               objectName
//	_Scene              scene
//	export_fbx          ExportFBX
//
// # Differences
//
// Array properties are zlib-compressed at level 1 like Python's
// zlib.compress(raw, 1), but with Go's compress/flate, whose streams differ
// from C zlib's: compressed payloads (and so the file size and offsets) are
// not byte-identical to Python's, while the decompressed content is.
// Options.Compress injects the compressor; with Python's own zlib output the
// files are byte-identical apart from the last bit of a few matrix values
// that NumPy computes with BLAS/LAPACK (see package exportpose).
package fbx

import (
	"errors"
	"math"
	"os"
	"strconv"
	"strings"

	"avolition/gocore/internal/exportpose"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
)

const (
	ktime       = 46186158000
	version     = 7400
	exportScale = 100.
)

// Materials maps a model id to its material descriptors by key
// (export_textures.bake_materials()['materials']).
type Materials = ordered.Map[*ordered.Map[*formats.Material]]

var rad2deg = 180 / pi

var pi = math.Pi

func degrees(v [3]float64) []float64 {
	return []float64{v[0] * rad2deg, v[1] * rad2deg, v[2] * rad2deg}
}

func value(n *node, name string, p prop) *node { return n.add(name, p) }

func properties(n *node) *node { return n.add("Properties70") }

type propType struct {
	typ, subtype string
	code         byte // 0: no values
}

var propTypes = map[string]propType{
	"number": {"Number", "", 'D'}, "double": {"double", "Number", 'D'},
	"int": {"int", "Integer", 'I'}, "enum": {"enum", "", 'I'},
	"bool": {"bool", "", 'I'}, "string": {"KString", "", 'S'},
	"time": {"KTime", "Time", 'L'}, "color": {"Color", "", 'D'},
	"vector":   {"Vector3D", "Vector", 'D'},
	"compound": {"Compound", "", 0}, "object": {"object", "", 0},
	"Lcl Translation": {"Lcl Translation", "", 'D'},
	"Lcl Rotation":    {"Lcl Rotation", "", 'D'},
	"Lcl Scaling":     {"Lcl Scaling", "", 'D'},
}

// setProp is _prop(props, name, value, kind, animated). values holds numbers
// (float64 or int64) or one string.
func setProp(props *node, name string, kind string, animated bool, values ...any) *node {
	t := propTypes[kind]
	flag := ""
	if animated {
		flag = "A"
	}
	record := []prop{sProp(name), sProp(t.typ), sProp(t.subtype), sProp(flag)}
	if t.code != 0 {
		for _, v := range values {
			switch t.code {
			case 'D':
				switch x := v.(type) {
				case float64:
					record = append(record, dProp(x))
				case int:
					record = append(record, dProp(float64(x)))
				}
			case 'I':
				record = append(record, iProp(int64(v.(int))))
			case 'L':
				record = append(record, lProp(v.(int64)))
			case 'S':
				record = append(record, sProp(v.(string)))
			}
		}
	}
	return props.add("P", record...)
}

func vec(v []float64) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}

// objectName is _name(name, cls): name without NULs, "\x00\x01", class.
func objectName(name, cls string) []byte {
	return []byte(strings.ReplaceAll(name, "\x00", "") + "\x00\x01" + cls)
}

// columnMajor is _matrix(value): value.T.reshape(-1).
func columnMajor(m *exportpose.Mat4) []float64 {
	values := m.ColumnMajor()
	return values[:]
}

type take struct {
	name        string
	start, stop int64
}

type scene struct {
	objects, connections *node
	nextID               int64
	modelProperties      map[int64]*node
	takes                []take
}

func newScene() *scene {
	return &scene{objects: newNode("Objects"), connections: newNode("Connections"), nextID: 1000,
		modelProperties: map[int64]*node{}}
}

func (s *scene) obj(kind, name, subtype, cls string) (int64, *node) {
	if cls == "" {
		cls = kind
	}
	s.nextID++
	return s.nextID, s.objects.add(kind, lProp(s.nextID), bProp('S', objectName(name, cls)), sProp(subtype))
}

func (s *scene) connect(child, parent int64, property string) {
	if property != "" {
		s.connections.add("C", sProp("OP"), lProp(child), lProp(parent), sProp(property))
	} else {
		s.connections.add("C", sProp("OO"), lProp(child), lProp(parent))
	}
}

func (s *scene) model(name, subtype string, parent int64, matrix *exportpose.Mat4, inheritType int) (int64, error) {
	ident, n := s.obj("Model", name, subtype, "")
	value(n, "Version", iProp(232))
	p, r, sc, err := exportpose.Decompose(matrix)
	if err != nil {
		return 0, err
	}
	props := properties(n)
	s.modelProperties[ident] = props
	setProp(props, "Lcl Translation", "Lcl Translation", true, vec(p[:])...)
	setProp(props, "Lcl Rotation", "Lcl Rotation", true, vec(degrees(r))...)
	setProp(props, "Lcl Scaling", "Lcl Scaling", true, vec(sc[:])...)
	setProp(props, "RotationOrder", "enum", false, 0)
	setProp(props, "InheritType", "enum", false, inheritType)
	setProp(props, "RotationActive", "bool", false, 1)
	// Blender writes this explicitly: the SDK default is -1, which leaves
	// GetNodeAttribute() empty even when geometry/skeleton data is connected.
	// Cinema 4D then imports both meshes and joints as plain null objects.
	if subtype == "Mesh" || subtype == "LimbNode" {
		setProp(props, "DefaultAttributeIndex", "int", false, 0)
	}
	value(n, "MultiLayer", iProp(0))
	value(n, "MultiTake", iProp(0))
	value(n, "Shading", cProp(true))
	value(n, "Culling", sProp("CullingOff"))
	s.connect(ident, parent, "")
	if subtype == "LimbNode" {
		aid, attr := s.obj("NodeAttribute", name, "LimbNode", "")
		value(attr, "TypeFlags", sProp("Skeleton"))
		setProp(properties(attr), "Size", "number", false, 5.)
		s.connect(aid, ident, "")
	}
	return ident, nil
}

var materialRoles = []struct {
	role       string
	properties []string
}{
	{"baseColor", []string{"DiffuseColor"}}, {"normal", []string{"NormalMap"}},
	{"roughness", []string{"Shininess", "ShininessExponent"}}, {"metalness", []string{"ReflectionFactor"}},
	{"alpha", []string{"TransparencyFactor"}}, {"emissive", []string{"EmissiveColor"}},
}

func (s *scene) material(desc *formats.Material, fallbackName string) int64 {
	name, color := fallbackName, []float64{0.55, 0.6, 0.66}
	rough, metal, emissive, opacityValue := 0.65, 0.0, []float64{1., 1., 1.}, 1.
	unbaked := map[string]bool{}
	intensity := 0.0
	if desc != nil {
		name = desc.Name
		color = desc.BaseColor[:]
		rough, metal, emissive, opacityValue = desc.Roughness, desc.Metalness, desc.Emissive[:], desc.Opacity
		for _, m := range desc.UnbakedMaps {
			unbaked[m] = true
		}
		if desc.EmissiveMode == "baseColorMask" {
			unbaked["emissive"] = true
		}
		if desc.EmissiveIntensity != nil {
			intensity = *desc.EmissiveIntensity
		}
	}
	ident, n := s.obj("Material", name, "", "")
	value(n, "Version", iProp(102))
	value(n, "ShadingModel", sProp("phong"))
	value(n, "MultiLayer", iProp(0))
	props := properties(n)
	emission := intensity
	if unbaked["emissive"] {
		emission = 0
	}
	shininess := (1 - rough) * 10
	shininess = shininess * shininess
	setProp(props, "DiffuseColor", "color", true, vec(color)...)
	setProp(props, "DiffuseFactor", "number", true, 1.)
	setProp(props, "SpecularColor", "color", true, 1., 1., 1.)
	setProp(props, "SpecularFactor", "number", true, 0.25)
	setProp(props, "Shininess", "number", true, shininess)
	setProp(props, "ShininessExponent", "number", true, shininess)
	setProp(props, "ReflectionFactor", "number", true, metal)
	setProp(props, "EmissiveColor", "color", true, vec(emissive)...)
	setProp(props, "EmissiveFactor", "number", true, emission)
	opacity := math.Max(0, math.Min(1, opacityValue))
	setProp(props, "Opacity", "number", true, opacity)
	setProp(props, "TransparencyFactor", "number", true, 1-opacity)
	setProp(props, "TransparentColor", "color", true, 1-opacity, 1-opacity, 1-opacity)
	setProp(props, "NormalMap", "vector", false, 0., 0., 0.)
	setProp(props, "BumpFactor", "number", false, 1.)
	// FBX's portable material model is Phong; these connections follow Blender's
	// exporter convention. Engines differ in mapping reflection/shininess to PBR.
	if desc == nil || desc.Maps == nil {
		return ident
	}
	for _, role := range materialRoles {
		if unbaked[role.role] {
			continue
		}
		mapping, ok := desc.Maps.Get(role.role)
		if !ok || mapping == nil || mapping.RelativePath == "" {
			continue
		}
		relative := strings.ReplaceAll(mapping.RelativePath, "\\", "/")
		tid, texture := s.obj("Texture", fallbackName+"_"+role.role, "", "")
		vid, video := s.obj("Video", relative, "Clip", "")
		value(video, "Type", sProp("Clip"))
		setProp(properties(video), "Path", "string", false, relative)
		value(video, "UseMipMap", iProp(0))
		value(video, "Filename", sProp(relative))
		value(video, "RelativeFilename", sProp(relative))
		value(texture, "Type", sProp("TextureVideoClip"))
		value(texture, "Version", iProp(202))
		value(texture, "TextureName", bProp('S', objectName(fallbackName+"_"+role.role, "Texture")))
		value(texture, "Media", bProp('S', objectName(relative, "Video")))
		value(texture, "FileName", sProp(relative))
		value(texture, "RelativeFilename", sProp(relative))
		tp := properties(texture)
		setProp(tp, "CurrentMappingType", "enum", false, 0)
		setProp(tp, "WrapModeU", "enum", false, 0)
		setProp(tp, "WrapModeV", "enum", false, 0)
		uvSet := "UVMap"
		if mapping.UVIndex != 0 {
			uvSet = "UVMap2"
		}
		setProp(tp, "UVSet", "string", false, uvSet)
		setProp(tp, "UseMaterial", "bool", false, 1)
		setProp(tp, "PremultiplyAlpha", "bool", false, 0)
		scaling := [2]float64{1, 1}
		for i, r := range mapping.Repeat {
			if r != 0 {
				scaling[i] = 1. / r
			}
		}
		setProp(tp, "Scaling", "vector", false, scaling[0], scaling[1], 1.)
		setProp(tp, "Translation", "vector", false, 0., 0., 0.)
		setProp(tp, "Rotation", "vector", false, 0., 0., 0.)
		setProp(tp, "AlphaSource", "enum", false, 0)
		s.connect(vid, tid, "")
		for _, property := range role.properties {
			s.connect(tid, ident, property)
		}
	}
	return ident
}

func float32s(values []float32, scale float64) []float64 {
	out := make([]float64, len(values))
	for i, v := range values {
		out[i] = float64(v) * scale
	}
	return out
}

// reshapeError is NumPy's message for reshape(-1, n) of a wrong-sized array.
func reshapeError(size, n int) error {
	return errors.New("cannot reshape array of size " + strconv.Itoa(size) + " into shape (" + strconv.Itoa(n) + ")")
}

func indexError(index uint32, size int) error {
	return errors.New("index " + strconv.FormatUint(uint64(index), 10) + " is out of bounds for axis 0 with size " + strconv.Itoa(size))
}

func (s *scene) geometry(mesh *formats.Mesh, modelID, materialID int64) (int64, error) {
	gid, geom := s.obj("Geometry", mesh.Name, "Mesh", "")
	value(geom, "GeometryVersion", iProp(124))
	if len(mesh.Positions)%3 != 0 {
		return 0, reshapeError(len(mesh.Positions), 3)
	}
	if len(mesh.Indices)%3 != 0 {
		return 0, reshapeError(len(mesh.Indices), 3)
	}
	vertices := float32s(mesh.Positions, exportScale)
	indices := make([]int64, len(mesh.Indices))
	polygon := make([]int64, len(mesh.Indices))
	for i, index := range mesh.Indices {
		indices[i] = int64(int32(index))
		polygon[i] = indices[i]
		if i%3 == 2 {
			polygon[i] = ^polygon[i]
		}
	}
	value(geom, "Vertices", doubles(vertices))
	value(geom, "PolygonVertexIndex", ints(polygon))
	type layerRef struct {
		name         string
		typed, index int
	}
	var layers []layerRef
	if len(mesh.Normals) > 0 {
		if len(mesh.Normals)%3 != 0 {
			return 0, reshapeError(len(mesh.Normals), 3)
		}
		count := len(mesh.Normals) / 3
		normals := make([]float64, 0, 3*len(mesh.Indices))
		for _, index := range mesh.Indices {
			if int(index) >= count {
				return 0, indexError(index, count)
			}
			normals = append(normals, float64(mesh.Normals[3*index]), float64(mesh.Normals[3*index+1]), float64(mesh.Normals[3*index+2]))
		}
		n := geom.add("LayerElementNormal", iProp(0))
		value(n, "Version", iProp(101))
		value(n, "Name", sProp(""))
		value(n, "MappingInformationType", sProp("ByPolygonVertex"))
		value(n, "ReferenceInformationType", sProp("Direct"))
		value(n, "Normals", doubles(normals))
		layers = append(layers, layerRef{"LayerElementNormal", 0, 0})
	}
	for uvLayer, uvs := range [2][]float32{mesh.UVs, mesh.UVs2} {
		if len(uvs) == 0 {
			continue
		}
		uv := geom.add("LayerElementUV", iProp(int64(uvLayer)))
		value(uv, "Version", iProp(101))
		name := "UVMap"
		if uvLayer == 1 {
			name = "UVMap2"
		}
		value(uv, "Name", sProp(name))
		value(uv, "MappingInformationType", sProp("ByPolygonVertex"))
		value(uv, "ReferenceInformationType", sProp("IndexToDirect"))
		value(uv, "UV", doubles(float32s(uvs, 1)))
		value(uv, "UVIndex", ints(indices))
		layers = append(layers, layerRef{"LayerElementUV", uvLayer, uvLayer})
	}
	m := geom.add("LayerElementMaterial", iProp(0))
	value(m, "Version", iProp(101))
	value(m, "Name", sProp(""))
	value(m, "MappingInformationType", sProp("AllSame"))
	value(m, "ReferenceInformationType", sProp("IndexToDirect"))
	value(m, "Materials", ints([]int64{0}))
	layers = append(layers, layerRef{"LayerElementMaterial", 0, 0})
	for layerIndex := 0; layerIndex <= 1; layerIndex++ {
		used := false
		for _, l := range layers {
			used = used || l.index == layerIndex
		}
		if !used {
			continue
		}
		layer := geom.add("Layer", iProp(int64(layerIndex)))
		value(layer, "Version", iProp(100))
		for _, l := range layers {
			if l.index != layerIndex {
				continue
			}
			element := layer.add("LayerElement")
			value(element, "Type", sProp(l.name))
			value(element, "TypedIndex", iProp(int64(l.typed)))
		}
	}
	s.connect(gid, modelID, "")
	s.connect(materialID, modelID, "")
	return gid, nil
}

func (s *scene) skin(mesh *formats.Mesh, geomID, meshID int64, meshWorld *exportpose.Mat4, boneIDs []int64,
	boneWorld []exportpose.Mat4, rigID int64, rigWorld *exportpose.Mat4) error {
	sid, skin := s.obj("Deformer", mesh.Name, "Skin", "")
	value(skin, "Version", iProp(101))
	value(skin, "Link_DeformAcuracy", dProp(50.))
	s.connect(sid, geomID, "")
	if len(mesh.SkinIndices)%4 != 0 {
		return reshapeError(len(mesh.SkinIndices), 4)
	}
	if len(mesh.SkinWeights) != len(mesh.SkinIndices) {
		return reshapeError(len(mesh.SkinWeights), 4)
	}
	vertices := len(mesh.SkinIndices) / 4
	for i, bid := range boneIDs {
		cid, cluster := s.obj("Deformer", strconv.Itoa(i), "Cluster", "SubDeformer")
		value(cluster, "Version", iProp(100))
		cluster.add("UserData", sProp(""), sProp(""))
		var affected []int64
		var weights []float64
		for v := 0; v < vertices; v++ {
			combined := 0.0
			for k := 0; k < 4; k++ {
				if int(mesh.SkinIndices[4*v+k]) == i {
					combined += mesh.SkinWeights[4*v+k]
				}
			}
			if combined > 0 {
				affected = append(affected, int64(v))
				weights = append(weights, combined)
			}
		}
		value(cluster, "Indexes", ints(affected))
		value(cluster, "Weights", doubles(weights))
		inverse, err := boneWorld[i].Inverse()
		if err != nil {
			return err
		}
		transform := inverse.Mul(meshWorld)
		value(cluster, "Transform", doubles(columnMajor(&transform)))
		value(cluster, "TransformLink", doubles(columnMajor(&boneWorld[i])))
		value(cluster, "TransformAssociateModel", doubles(columnMajor(rigWorld)))
		s.connect(cid, sid, "")
		s.connect(bid, cid, "")
	}
	_, pose := s.obj("Pose", mesh.Name, "BindPose", "")
	value(pose, "Type", sProp("BindPose"))
	value(pose, "Version", iProp(100))
	value(pose, "NbPoseNodes", iProp(int64(2+len(boneIDs))))
	entries := []struct {
		id     int64
		matrix *exportpose.Mat4
	}{{meshID, meshWorld}, {rigID, rigWorld}}
	for i, bid := range boneIDs {
		entries = append(entries, struct {
			id     int64
			matrix *exportpose.Mat4
		}{bid, &boneWorld[i]})
	}
	for _, e := range entries {
		pn := pose.add("PoseNode")
		value(pn, "Node", lProp(e.id))
		value(pn, "Matrix", doubles(columnMajor(e.matrix)))
	}
	return nil
}

// frameTRS is one baked frame: centimetre translation, Euler degrees, scale.
type frameTRS [3][3]float64

type animationTrack struct {
	model  int64
	frames []frameTRS
}

func (s *scene) animation(name string, times []int64, tracks []animationTrack) {
	s.takes = append(s.takes, take{name, times[0], times[len(times)-1]})
	stackID, stack := s.obj("AnimationStack", name, "", "AnimStack")
	props := properties(stack)
	for _, entry := range []struct {
		key   string
		value int64
	}{{"LocalStart", times[0]}, {"LocalStop", times[len(times)-1]}, {"ReferenceStart", times[0]}, {"ReferenceStop", times[len(times)-1]}} {
		setProp(props, entry.key, "time", false, entry.value)
	}
	layerID, layer := s.obj("AnimationLayer", name, "", "AnimLayer")
	s.connect(layerID, stackID, "")
	props = properties(layer)
	// These are Blender's FbxAnimLayer template defaults, written directly
	// since this exporter does not omit values through shared templates.
	setProp(props, "Weight", "number", true, 100.)
	for _, key := range []string{"Mute", "Solo", "Lock"} {
		setProp(props, key, "bool", false, 0)
	}
	for _, key := range []string{"BlendMode", "RotationAccumulationMode", "ScaleAccumulationMode"} {
		setProp(props, key, "enum", false, 0)
	}
	for _, track := range tracks {
		// 'A' means animatable; Blender uses 'A+' for an actual animated
		// property. Preserve that distinction for SDK-based importers.
		for _, p := range s.modelProperties[track.model].children {
			switch string(p.props[0].str) {
			case "Lcl Translation", "Lcl Rotation", "Lcl Scaling":
				p.props[3] = sProp("A+")
			}
		}
		for propertyIndex, names := range [3][2]string{{"T", "Lcl Translation"}, {"R", "Lcl Rotation"}, {"S", "Lcl Scaling"}} {
			nodeID, curveNode := s.obj("AnimationCurveNode", names[0], "", "AnimCurveNode")
			props := properties(curveNode)
			setProp(props, "d", "compound", false)
			s.connect(nodeID, layerID, "")
			s.connect(nodeID, track.model, names[1])
			for channel, letter := range []string{"X", "Y", "Z"} {
				values := make([]float64, len(track.frames))
				for f := range track.frames {
					values[f] = track.frames[f][propertyIndex][channel]
				}
				setProp(props, "d|"+letter, "number", true, values[0])
				curveID, curve := s.obj("AnimationCurve", "", "", "AnimCurve")
				value(curve, "Default", dProp(values[0]))
				value(curve, "KeyVer", iProp(4008))
				value(curve, "KeyTime", longs(times))
				value(curve, "KeyValueFloat", floats(values))
				value(curve, "KeyAttrFlags", ints([]int64{4})) // Linear, sampled at the original FPS.
				value(curve, "KeyAttrDataFloat", floats([]float64{0, 0, 0, 0}))
				value(curve, "KeyAttrRefCount", ints([]int64{int64(len(times))}))
				s.connect(curveID, nodeID, "d|"+letter)
			}
		}
	}
}

var timeModes = map[float64]int{120: 1, 100: 2, 60: 3, 50: 4, 48: 5, 30: 6, 25: 10, 24: 11, 96: 15, 72: 16}

var (
	fileID       = []byte{0x28, 0xb3, 0x2a, 0xeb, 0xb6, 0x24, 0xcc, 0xc2, 0xbf, 0xc8, 0xb0, 0x2a, 0xa9, 0x2b, 0xfc, 0xf1}
	footerID     = []byte{0xfa, 0xbc, 0xab, 0x09, 0xd0, 0xc8, 0xd4, 0x66, 0xb1, 0x76, 0xfb, 0x83, 0x1c, 0xf7, 0x26, 0x7e}
	footerMagic  = []byte{0xf8, 0x5a, 0x8c, 0x6a, 0xde, 0xf5, 0xd9, 0x7e, 0xec, 0xe9, 0x0c, 0xe3, 0x75, 0x8f, 0x29, 0x0b}
	headerMagic  = []byte("Kaydara FBX Binary  \x00\x1a\x00")
	creationTime = "1970-01-01 10:00:00:000"
)

// encodeFile builds the complete file. The creation stamp is fixed (1970),
// so output depends only on the scene.
func (s *scene) encodeFile(axis string, fps float64, lastFrame int64, compress Compressor) ([]byte, error) {
	root := newNode("")
	header := root.add("FBXHeaderExtension")
	value(header, "FBXHeaderVersion", iProp(1003))
	value(header, "FBXVersion", iProp(version))
	value(header, "EncryptionType", iProp(0))
	stamp := header.add("CreationTimeStamp")
	value(stamp, "Version", iProp(1000))
	for _, entry := range []struct {
		key   string
		value int64
	}{{"Year", 1970}, {"Month", 1}, {"Day", 1}, {"Hour", 10}, {"Minute", 0}, {"Second", 0}, {"Millisecond", 0}} {
		value(stamp, entry.key, iProp(entry.value))
	}
	value(header, "Creator", sProp("Whitehound standalone FBX exporter"))
	value(root, "FileId", bProp('R', fileID))
	value(root, "CreationTime", sProp(creationTime))
	value(root, "Creator", sProp("Whitehound"))
	glob := root.add("GlobalSettings")
	value(glob, "Version", iProp(1000))
	p := properties(glob)
	axes := [3]int{1, 2, 1}
	if axis != "Y" {
		axes = [3]int{2, 1, -1}
	}
	for _, entry := range []struct {
		key   string
		value int
	}{{"UpAxis", axes[0]}, {"UpAxisSign", 1}, {"FrontAxis", axes[1]}, {"FrontAxisSign", axes[2]}, {"CoordAxis", 0},
		{"CoordAxisSign", 1}, {"OriginalUpAxis", axes[0]}, {"OriginalUpAxisSign", 1}} {
		setProp(p, entry.key, "int", false, entry.value)
	}
	setProp(p, "UnitScaleFactor", "double", false, 1.)
	setProp(p, "OriginalUnitScaleFactor", "double", false, 1.)
	mode, ok := timeModes[fps]
	if !ok {
		mode = 14
	}
	setProp(p, "TimeMode", "enum", false, mode)
	setProp(p, "CustomFrameRate", "double", false, fps)
	setProp(p, "TimeSpanStart", "time", false, int64(0))
	setProp(p, "TimeSpanStop", "time", false, int64(math.RoundToEven(float64(lastFrame)/fps*ktime)))
	docs := root.add("Documents")
	value(docs, "Count", iProp(1))
	doc := docs.add("Document", lProp(999), sProp("Whitehound"), sProp("Scene"))
	props := properties(doc)
	setProp(props, "SourceObject", "object", false)
	active := ""
	if len(s.takes) > 0 {
		active = s.takes[0].name
	}
	setProp(props, "ActiveAnimStackName", "string", false, active)
	value(doc, "RootNode", lProp(0))
	root.add("References")
	definitions := root.add("Definitions")
	value(definitions, "Version", iProp(100))
	// Counter(node.name for node in objects.children), then GlobalSettings.
	counts := ordered.New[int]()
	for _, child := range s.objects.children {
		n, _ := counts.Get(child.name)
		counts.Set(child.name, n+1)
	}
	counts.Set("GlobalSettings", 1)
	total := 0
	counts.Range(func(_ string, n int) bool { total += n; return true })
	value(definitions, "Count", iProp(int64(total)))
	counts.Range(func(name string, n int) bool {
		typ := definitions.add("ObjectType", sProp(name))
		value(typ, "Count", iProp(int64(n)))
		return true
	})
	root.children = append(root.children, s.objects, s.connections)
	takes := root.add("Takes")
	value(takes, "Current", sProp(active))
	for _, t := range s.takes {
		tk := takes.add("Take", sProp(t.name))
		value(tk, "FileName", sProp(t.name+".tak"))
		tk.add("LocalTime", lProp(t.start), lProp(t.stop))
		tk.add("ReferenceTime", lProp(t.start), lProp(t.stop))
	}
	if compress == nil {
		compress = ZlibBestSpeed
	}
	prepareArrays(root.children, compress)
	data := append([]byte(nil), headerMagic...)
	data = append(data, 0, 0, 0, 0)
	putUint32(data[23:], version)
	var err error
	for _, child := range root.children {
		if data, err = child.encode(data); err != nil {
			return nil, err
		}
	}
	data = append(data, make([]byte, 13)...)
	data = append(data, footerID...)
	data = append(data, make([]byte, 4)...)
	data = append(data, make([]byte, 16-len(data)%16)...)
	data = append(data, 0, 0, 0, 0)
	putUint32(data[len(data)-4:], version)
	data = append(data, make([]byte, 120)...)
	return append(data, footerMagic...), nil
}

func putUint32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// Options are export_fbx's keyword arguments. Python's defaults: UpAxis "Y",
// IncludeMesh and RootMotion true, RigID "auto" (an empty RigID means auto).
type Options struct {
	UpAxis      string
	IncludeMesh bool
	RootMotion  bool
	Materials   *Materials
	ClipName    string
	RigID       string
	// Compress replaces the zlib compressor (tests use it to reproduce
	// Python's exact zlib bytes); nil means ZlibBestSpeed.
	Compress Compressor
}

// DefaultOptions are Python's keyword defaults.
func DefaultOptions() Options {
	return Options{UpAxis: "Y", IncludeMesh: true, RootMotion: true, RigID: "auto"}
}

// Report is export_fbx's result.
type Report struct {
	Warnings            []string `json:"warnings"`
	MeshCount           int      `json:"meshCount"`
	BoneCount           int      `json:"boneCount"`
	HelperBoneCount     int      `json:"helperBoneCount"`
	ModelGroupBoneCount int      `json:"modelGroupBoneCount"`
	FrameCount          int64    `json:"frameCount"`
	FPS                 float64  `json:"fps"`
	MatchedBones        int      `json:"matchedBones"`
	RigID               string   `json:"rigId"`
	Unit                string   `json:"unit"`
	Scale               float64  `json:"scale"`
	UpAxis              string   `json:"upAxis"`
	Bytes               int64    `json:"bytes"`
}

type modelState struct {
	source    *formats.Model
	boneIDs   []int64
	boneWorld []exportpose.Mat4
	group     int64
	world     exportpose.Mat4
	hasGroup  bool
}

// ExportFBX is export_fbx(path, asset, clip, ...): export one selected
// animation and the entity's nested meshes/skeletons.
//
// Texture paths must already be staged next to the FBX by the caller. Source
// textures are never edited. Original frame zero is FBX time zero. User-facing
// callers should write into a transaction directory and publish on success.
// A nil clip is a one-frame rest pose at 30 FPS.
func ExportFBX(path string, asset *formats.Asset, clip *formats.Clip, opts Options) (*Report, error) {
	data, report, err := Encode(asset, clip, opts)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o666); err != nil {
		return nil, formats.PythonOSError(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	report.Bytes = info.Size()
	return report, nil
}

// Encode is ExportFBX without writing a file: the FBX bytes and the report
// (whose Bytes is the data length).
func Encode(asset *formats.Asset, clip *formats.Clip, opts Options) ([]byte, *Report, error) {
	upAxis := opts.UpAxis
	if upAxis != "Y" && upAxis != "Z" {
		return nil, nil, errors.New("FBX up axis must be Y or Z.")
	}
	models := asset.Models
	if len(models) == 0 {
		return nil, nil, errors.New("Load an entity or model before exporting FBX.")
	}
	if clip == nil {
		clip = &formats.Clip{ClipHeader: formats.ClipHeader{Name: "Animation", FPS: 30, LastFrame: 0}, Bones: []formats.ClipBone{}}
	}
	fps, lastFrame := clip.FPS, clip.LastFrame
	if math.IsNaN(fps) || math.IsInf(fps, 0) || !(0 < fps && fps <= 1000) || lastFrame < 0 {
		return nil, nil, errors.New("Invalid animation timing.")
	}
	names := map[string]bool{}
	for i := range clip.Bones {
		names[clip.Bones[i].Name] = true
	}
	matches := func(m *formats.Model) int {
		n := 0
		for i := range m.Bones {
			if names[m.Bones[i].Name] {
				n++
			}
		}
		return n
	}
	rigID := opts.RigID
	if rigID == "" {
		rigID = "auto"
	}
	var selected *formats.Model
	if rigID == "auto" {
		best, bestBones := -1, -1
		for _, m := range models {
			if len(m.Bones) == 0 {
				continue
			}
			if n := matches(m); n > best || (n == best && len(m.Bones) > bestBones) {
				selected, best, bestBones = m, n, len(m.Bones)
			}
		}
	} else {
		for _, m := range models {
			if m.ID == rigID {
				selected = m
				break
			}
		}
	}
	if selected == nil || len(selected.Bones) == 0 {
		return nil, nil, errors.New("The selected export rig has no skeleton.")
	}
	matched := matches(selected)
	if len(names) > 0 && matched == 0 {
		return nil, nil, errors.New("The selected animation has no matching bones on this rig.")
	}
	if (lastFrame+1)*int64(max(1, len(selected.Bones))) > 1_000_000 {
		return nil, nil, errors.New("FBX bake exceeds one million bone frames; choose a shorter clip.")
	}
	var warnings []string
	s := newScene()
	axisMatrix := exportpose.Identity4
	if upAxis == "Z" {
		axisMatrix[1] = [4]float64{0, 0, -1, 0}
		axisMatrix[2] = [4]float64{0, 1, 0, 0}
	}
	// Source names and wrappers match the working Cinema 4D entity importer.
	// FBX identifiers, rather than renamed bones, disambiguate attached rigs.
	hierarchy := asset.ExportHierarchy
	if len(hierarchy) == 0 {
		var err error
		if hierarchy, err = formats.BuildExportHierarchy(asset); err != nil {
			return nil, nil, err
		}
	}
	lookup := map[string]*formats.Model{}
	state := map[string]*modelState{}
	for _, m := range models {
		lookup[m.ID] = m
		state[m.ID] = &modelState{source: m, boneIDs: make([]int64, len(m.Bones)), boneWorld: make([]exportpose.Mat4, len(m.Bones))}
	}
	exported := map[string]int64{}
	worlds := map[string]exportpose.Mat4{}
	var pending []*formats.Node
	for i := range hierarchy {
		if opts.IncludeMesh || hierarchy[i].Type != "mesh" {
			pending = append(pending, &hierarchy[i])
		}
	}
	isExported := func(id string) bool { _, ok := exported[id]; return ok }
	for len(pending) > 0 {
		var ready, rest []*formats.Node
		for _, n := range pending {
			if n.ParentID == nil || *n.ParentID == "" || isExported(*n.ParentID) {
				ready = append(ready, n)
			} else {
				rest = append(rest, n)
			}
		}
		if len(ready) == 0 {
			return nil, nil, errors.New("Export hierarchy contains a missing parent or cycle.")
		}
		pending = rest
		for _, n := range ready {
			matrix := exportpose.FromColumnMajor(n.Matrix)
			for row := 0; row < 3; row++ {
				matrix[row][3] *= exportScale
			}
			if n.ParentID == nil {
				matrix = axisMatrix.Mul(&matrix)
			}
			world := matrix
			var parent int64
			if n.ParentID != nil && *n.ParentID != "" {
				parentWorld := worlds[*n.ParentID]
				world = parentWorld.Mul(&matrix)
				parent = exported[*n.ParentID]
			}
			subtype := "Null"
			switch n.Type {
			case "bone":
				subtype = "LimbNode"
			case "mesh":
				subtype = "Mesh"
			}
			inheritType := 1
			if n.Type == "bone" {
				model := lookup[n.ModelID]
				if model == nil || n.BoneIndex == nil || *n.BoneIndex < 0 || *n.BoneIndex >= len(model.Bones) {
					return nil, nil, errors.New("The export hierarchy references a missing joint.")
				}
				if model.Bones[*n.BoneIndex].Parent >= 0 {
					// Rrs removes only the immediate parent's local scale from
					// rotation/scale inheritance while its translation still uses
					// the full parent matrix. This is the game's joint convention.
					inheritType = 2
				}
			}
			ident, err := s.model(n.Name, subtype, parent, &matrix, inheritType)
			if err != nil {
				return nil, nil, err
			}
			exported[n.ID], worlds[n.ID] = ident, world
			if st := state[n.ModelID]; st != nil {
				switch n.Type {
				case "model":
					st.group, st.world, st.hasGroup = ident, world, true
				case "bone":
					st.boneIDs[*n.BoneIndex], st.boneWorld[*n.BoneIndex] = ident, world
				}
			}
		}
	}
	meshCount := 0
	if opts.IncludeMesh {
		for _, model := range models {
			st := state[model.ID]
			cache := map[string]int64{}
			var supplied *ordered.Map[*formats.Material]
			if opts.Materials != nil {
				supplied, _ = opts.Materials.Get(model.ID)
			}
			for meshIndex, mesh := range model.Meshes {
				key := mesh.MaterialKey
				if _, done := cache[key]; !done {
					var descriptor *formats.Material
					if supplied != nil {
						descriptor, _ = supplied.Get(key)
					}
					if descriptor == nil && model.Materials != nil {
						descriptor, _ = model.Materials.Get(key)
					}
					if descriptor != nil {
						if descriptor.Maps != nil {
							if _, ao := descriptor.Maps.Get("ao"); ao {
								warnings = append(warnings, model.Name+" / "+key+": separate AO map has no portable FBX Phong input and was omitted.")
							}
						}
						unbakedEmission := descriptor.EmissiveMode == "baseColorMask"
						for _, m := range descriptor.UnbakedMaps {
							unbakedEmission = unbakedEmission || m == "emissive"
						}
						if unbakedEmission {
							warnings = append(warnings, model.Name+" / "+key+": unbaked emission-mask shader was omitted from FBX.")
						}
					}
					cache[key] = s.material(descriptor, model.Name+"_"+key)
				}
				meshID, ok := exported[model.ID+":mesh:"+strconv.Itoa(meshIndex)]
				if !ok {
					return nil, nil, errors.New("'" + model.ID + ":mesh:" + strconv.Itoa(meshIndex) + "'")
				}
				geomID, err := s.geometry(mesh, meshID, cache[key])
				if err != nil {
					return nil, nil, err
				}
				if len(st.boneIDs) > 0 && len(mesh.SkinIndices) > 0 && len(mesh.SkinWeights) > 0 {
					if !st.hasGroup {
						return nil, nil, errors.New("'world'")
					}
					if err := s.skin(mesh, geomID, meshID, &st.world, st.boneIDs, st.boneWorld, st.group, &st.world); err != nil {
						return nil, nil, err
					}
				}
				meshCount++
			}
		}
	}
	st := state[selected.ID]
	evaluator, err := exportpose.NewEvaluator(selected.Bones, clip.Bones)
	if err != nil {
		return nil, nil, err
	}
	tracks := make([]animationTrack, len(st.boneIDs))
	for i, bid := range st.boneIDs {
		tracks[i] = animationTrack{model: bid, frames: make([]frameTRS, lastFrame+1)}
	}
	previous := make([]*[3]float64, len(st.boneIDs))
	pose := evaluator.NewPose()
	for frame := int64(0); frame <= lastFrame; frame++ {
		evaluator.EvaluateInto(pose, float64(frame), !opts.RootMotion)
		for i := range st.boneIDs {
			p, q, sc := pose.Positions[i], pose.Quaternions[i], pose.Scales[i]
			r := exportpose.EulerXYZ(exportpose.Rotation(q), previous[i])
			previous[i] = &r
			d := degrees(r)
			tracks[i].frames[frame] = frameTRS{{p[0] * exportScale, p[1] * exportScale, p[2] * exportScale},
				{d[0], d[1], d[2]}, sc}
		}
	}
	times := make([]int64, lastFrame+1)
	for frame := range times {
		times[frame] = int64(math.RoundToEven(float64(frame) / fps * ktime))
	}
	name := opts.ClipName
	if name == "" {
		name = clip.Name
	}
	s.animation(name, times, mergeTracks(tracks))
	data, err := s.encodeFile(upAxis, fps, lastFrame, opts.Compress)
	if err != nil {
		return nil, nil, err
	}
	if opts.IncludeMesh {
		warnings = append(warnings, "FBX materials use portable Phong connections; roughness, metallic, transparency and emission interpretation varies by importer.")
	}
	if warnings == nil {
		warnings = []string{}
	}
	bones := 0
	for _, m := range models {
		bones += len(m.Bones)
	}
	return data, &Report{Warnings: warnings, MeshCount: meshCount, BoneCount: bones, FrameCount: lastFrame + 1,
		FPS: fps, MatchedBones: matched, RigID: selected.ID, Unit: "centimetres", Scale: exportScale,
		UpAxis: upAxis, Bytes: int64(len(data))}, nil
}

// mergeTracks reproduces Python's {bid: frames} dict: a repeated bone id
// keeps its first position and its last frames.
func mergeTracks(tracks []animationTrack) []animationTrack {
	position := map[int64]int{}
	var out []animationTrack
	for _, t := range tracks {
		if i, ok := position[t.model]; ok {
			out[i] = t
			continue
		}
		position[t.model] = len(out)
		out = append(out, t)
	}
	return out
}
