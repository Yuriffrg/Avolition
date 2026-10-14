package formats

import (
	"encoding/binary"
	"strconv"

	"avolition/gocore/internal/pypath"
)

func ptr[T any](value T) *T { return &value }

// LoadModel reads one .owmdl (Python's _load_model). Non-fatal problems are
// appended to *warnings, as Python appends to the shared list. The returned
// model has no id/attachment fields yet; LoadAsset fills them.
func LoadModel(path string, warnings *[]string) (*Model, error) {
	r, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	head, err := r.Unpack("HH")
	if err != nil {
		return nil, err
	}
	major, minor := uint16(head.Uint(0)), uint16(head.Uint(1))
	if err := CheckVersion(r, major, minor, "model"); err != nil {
		return nil, err
	}
	material, err := r.String()
	if err != nil {
		return nil, err
	}
	name, err := r.String()
	if err != nil {
		return nil, err
	}
	header, err := r.Take(14) // 'IHII'
	if err != nil {
		return nil, err
	}
	guid := binary.LittleEndian.Uint32(header)
	boneCount := int(binary.LittleEndian.Uint16(header[4:]))
	meshCount := binary.LittleEndian.Uint32(header[6:])
	socketCount := binary.LittleEndian.Uint32(header[10:])
	if err := r.Count(int64(meshCount), 16384, "mesh count"); err != nil {
		return nil, err
	}
	if err := r.Count(int64(socketCount), 65536, "socket count"); err != nil {
		return nil, err
	}

	bones := make([]Bone, 0, boneCount)
	boneNames := make(map[string]struct{}, boneCount)
	for index := 0; index < boneCount; index++ {
		boneName, err := r.String()
		if err != nil {
			return nil, err
		}
		parent16, err := r.I16()
		if err != nil {
			return nil, err
		}
		pos, err := r.Float64s(3)
		if err != nil {
			return nil, err
		}
		if _, err := r.Float64s(3); err != nil { // Exported scale is ignored like BLModel.
			return nil, err
		}
		rot, err := r.Float64s(3)
		if err != nil {
			return nil, err
		}
		parent := int(parent16)
		if !(-1 <= parent && parent < boneCount) || parent == index {
			return nil, r.Failf("invalid parent %d for bone %d", parent, index)
		}
		if _, duplicate := boneNames[boneName]; boneName == "" || duplicate {
			return nil, r.Fail("missing or duplicate bone name: " + PythonRepr(boneName))
		}
		boneNames[boneName] = struct{}{}
		bones = append(bones, Bone{
			Name: boneName, SourceName: boneName, Parent: parent,
			Pos:   [3]float64{pos[0], pos[1], pos[2]},
			Rot:   EulerXYZ([3]float64{rot[0], rot[1], rot[2]}),
			Scale: [3]float64{1, 1, 1},
		})
	}
	globals, err := BoneGlobals(bones, r)
	if err != nil {
		return nil, err
	}

	fileName := pypath.Name(path)
	meshes := make([]*Mesh, 0, meshCount)
	vertexTotal, faceTotal := int64(0), int64(0)
	for meshIndex := 0; meshIndex < int(meshCount); meshIndex++ {
		mesh, err := readMesh(r, meshIndex, boneCount, &vertexTotal, &faceTotal, fileName, warnings)
		if err != nil {
			return nil, err
		}
		meshes = append(meshes, mesh)
	}

	sockets := make([]Socket, 0, socketCount)
	lookup := make(map[string]int, len(bones))
	for index := range bones {
		lookup[bones[index].Name] = index
	}
	for i := 0; i < int(socketCount); i++ {
		socketName, err := r.String()
		if err != nil {
			return nil, err
		}
		boneName, err := r.String()
		if err != nil {
			return nil, err
		}
		position, err := r.Float64s(3)
		if err != nil {
			return nil, err
		}
		rotation, err := r.Float64s(4)
		if err != nil {
			return nil, err
		}
		matrix := Compose([3]float64{position[0], position[1], position[2]},
			NormalizeQuaternion([4]float64{rotation[0], rotation[1], rotation[2], rotation[3]}))
		// OWLib writes hardpoints in model space, not parent-bone local space.
		// Convert once so child entities continue following animated bones.
		// The exporter writes bone_0000 when the hardpoint has no bone GUID.
		// Treat that null reference as model-space even if a root bone happens
		// to carry the same name.
		var socketBone *string
		if index, ok := lookup[boneName]; ok && boneName != "bone_0000" {
			inverse := InverseRigid(&globals[index])
			matrix = Multiply(&inverse, &matrix)
			socketBone = ptr(boneName)
		}
		sockets = append(sockets, Socket{Name: socketName, Bone: socketBone, Matrix: matrix})
	}
	if r.Remaining() > 0 {
		*warnings = append(*warnings, fileName+": ignored "+Commas(r.Remaining())+
			" trailing bytes from model version "+strconv.Itoa(int(major))+"."+strconv.Itoa(int(minor))+".")
	}
	if name == "" {
		name = pypath.Stem(path)
	}
	return &Model{
		Name: name, Path: path, GUID: strconv.FormatUint(uint64(guid), 10),
		Bones: bones, Meshes: meshes, Sockets: sockets, MaterialPath: ptr(material),
	}, nil
}

func readMesh(r *Reader, meshIndex, boneCount int, vertexTotal, faceTotal *int64, fileName string, warnings *[]string) (*Mesh, error) {
	meshName, err := r.String()
	if err != nil {
		return nil, err
	}
	if meshName == "" {
		meshName = "Mesh " + strconv.Itoa(meshIndex+1)
	}
	header, err := r.Take(18) // 'QBIIB'
	if err != nil {
		return nil, err
	}
	materialKey := binary.LittleEndian.Uint64(header)
	uvCount := int(header[8])
	vertexCount := int(binary.LittleEndian.Uint32(header[9:]))
	faceCount := int(binary.LittleEndian.Uint32(header[13:]))
	slots := int(header[17])
	*vertexTotal += int64(vertexCount)
	*faceTotal += int64(faceCount)
	if err := r.Count(*vertexTotal, MaxVertices, "total model vertices"); err != nil {
		return nil, err
	}
	if err := r.Count(*faceTotal, MaxFaces, "total model triangles"); err != nil {
		return nil, err
	}
	if err := r.Count(int64(uvCount), 16, "UV channel count"); err != nil {
		return nil, err
	}
	if err := r.Count(int64(slots), 32, "skin influences per vertex"); err != nil {
		return nil, err
	}
	// Verify the entire fixed-sized mesh before allocating its arrays.
	meshBytes := vertexCount*(12+12+16+uvCount*8+slots*6+32) + faceCount*12
	if meshBytes > r.Remaining() {
		return nil, r.Fail("truncated mesh " + PythonRepr(meshName) + ": requires " + Commas(meshBytes) + " bytes")
	}
	positions, err := r.yUpFloat32s(vertexCount)
	if err != nil {
		return nil, err
	}
	normals, err := r.yUpFloat32s(vertexCount)
	if err != nil {
		return nil, err
	}
	if _, err := r.Take(vertexCount * 16); err != nil { // Three derives tangent space from positions/UVs.
		return nil, err
	}
	uvs, uvs2 := []float32{}, []float32{}
	if uvCount > 0 {
		if uvs, err = r.Float32s(vertexCount * 2); err != nil {
			return nil, err
		}
	}
	if uvCount > 1 {
		if uvs2, err = r.Float32s(vertexCount * 2); err != nil {
			return nil, err
		}
	}
	if _, err := r.Take(max(0, uvCount-2) * vertexCount * 8); err != nil {
		return nil, err
	}
	// OWLib's skeleton.Lookup is ushort[]; 0xffff is its missing-bone
	// sentinel. Values above 32767 are valid when the skeleton is large.
	indices, err := r.Uint16s(vertexCount * slots)
	if err != nil {
		return nil, err
	}
	weights, err := r.Float32s(vertexCount * slots)
	if err != nil {
		return nil, err
	}
	if _, err := r.Take(vertexCount * 32); err != nil { // Two game-specific vertex color channels.
		return nil, err
	}
	faces, err := r.Uint32s(faceCount * 3)
	if err != nil {
		return nil, err
	}
	for _, index := range faces {
		if int(index) >= vertexCount {
			return nil, r.Fail("triangle index outside vertex array in " + PythonRepr(meshName))
		}
	}
	skinIndices, skinWeights := []uint16{}, []float64{}
	if slots > 0 && boneCount > 0 {
		var orphans int
		skinIndices, skinWeights, orphans = Skin(indices, weights, boneCount, vertexCount, slots)
		if orphans > 0 {
			*warnings = append(*warnings, fileName+": "+meshName+" has "+strconv.Itoa(orphans)+
				" vertices without valid skin weights; attached to first joint.")
		}
	}
	return &Mesh{
		Name: meshName, MaterialKey: strconv.FormatUint(materialKey, 10),
		Positions: positions, Normals: normals, UVs: uvs, UVs2: uvs2,
		Indices: faces, SkinIndices: skinIndices, SkinWeights: skinWeights,
	}, nil
}
