package formats

import (
	"encoding/json"

	"avolition/gocore/internal/ordered"
)

// Limits shared with the other parsers (Python module constants).
const (
	MaxFileBytes   = 512 * 1024 * 1024
	MaxVertices    = 2_000_000
	MaxFaces       = 4_000_000
	MaxModels      = 512
	MaxEntityDepth = 48
)

// Nullable is a JSON value that can be absent (omitted with `omitzero`),
// null, or set — for Python keys that are added conditionally and may hold
// None, such as a model's materialLook.
type Nullable[T any] struct {
	// Set reports whether the key exists.
	Set bool
	// Value is nil for JSON null.
	Value *T
}

// Some returns a present, non-null value.
func Some[T any](value T) Nullable[T] { return Nullable[T]{Set: true, Value: &value} }

// Null returns a present null.
func Null[T any]() Nullable[T] { return Nullable[T]{Set: true} }

// IsZero makes `omitzero` omit an absent value.
func (n Nullable[T]) IsZero() bool { return !n.Set }

func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*n.Value)
}

func (n *Nullable[T]) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = nil
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	n.Value = &value
	return nil
}

// Bone is one skeleton joint. Pos is the exported rest translation, Rot the
// XYZW rest rotation and Scale is always one (exported scale is ignored like
// BLModel). Name and SourceName are the raw exported name.
type Bone struct {
	Name       string     `json:"name"`
	SourceName string     `json:"sourceName"`
	Parent     int        `json:"parent"`
	Pos        [3]float64 `json:"pos"`
	Rot        [4]float64 `json:"rot"`
	Scale      [3]float64 `json:"scale"`
}

// Mesh is one submesh in raw Y-up coordinates. Vertex data keeps the file's
// float32 precision; SkinWeights are computed in float64 like Python.
// SkinIndices/SkinWeights hold four influences per vertex, or are empty.
type Mesh struct {
	Name        string    `json:"name"`
	MaterialKey string    `json:"materialKey"`
	Positions   []float32 `json:"positions"`
	Normals     []float32 `json:"normals"`
	UVs         []float32 `json:"uvs"`
	UVs2        []float32 `json:"uvs2"`
	Indices     []uint32  `json:"indices"`
	SkinIndices []uint16  `json:"skinIndices"`
	SkinWeights []float64 `json:"skinWeights"`

	// SourceMeshIndex is added by map loading (mapformats) and the object-name
	// layer: the mesh's index in the source model before collision meshes
	// were removed. Absent from load_asset output.
	SourceMeshIndex *int `json:"sourceMeshIndex,omitempty"`
}

// Socket is a hardpoint. Bone is the parent joint's raw name, or nil when the
// socket is in model space; Matrix is relative to that joint (or the model).
type Socket struct {
	Name   string  `json:"name"`
	Bone   *string `json:"bone"`
	Matrix Mat4    `json:"matrix"`
}

// Model is one entry of Asset.Models. Fields follow the Python dict built by
// _load_model and load_asset, in Python's key order. Pointer fields encode
// Python None as JSON null; fields documented as optional are absent from
// the JSON when nil/unset, exactly where Python omits the key.
type Model struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// GUID is the model header's uint32 GUID as decimal; absent for an
	// entity without a model.
	GUID    string   `json:"guid,omitempty"`
	Bones   []Bone   `json:"bones"`
	Meshes  []*Mesh  `json:"meshes,omitzero"` // nil (absent) only for compact map models
	Sockets []Socket `json:"sockets"`
	// MaterialPath is the model header's look reference ("" when none);
	// absent for an entity without a model.
	MaterialPath *string `json:"materialPath,omitempty"`
	// MaterialLook is set by the MaterialResolver (materials.for_model): the
	// effective look reference, or null.
	MaterialLook Nullable[string] `json:"materialLook,omitzero"`
	// Materials maps a mesh MaterialKey to its descriptor (nil value: an
	// explicitly cleared slot in partial map looks).
	Materials *ordered.Map[*Material] `json:"materials,omitempty"`

	ID               string  `json:"id"`
	ParentID         *string `json:"parentId"`
	AttachmentBone   *string `json:"attachmentBone"`
	AttachmentMatrix Mat4    `json:"attachmentMatrix"`

	// IsEntity and HasModel are nil only for metadata-free models that callers
	// construct directly; BuildExportHierarchy then treats both as true.
	IsEntity   *bool   `json:"isEntity,omitempty"`
	EntityName *string `json:"entityName"`
	EntityPath *string `json:"entityPath"`
	ModelGUID  *string `json:"modelGuid"`
	HasModel   *bool   `json:"hasModel,omitempty"`

	// Keys added by later layers, absent from load_asset output:
	// meshNames (object_names.apply_object_names), meshPrototype,
	// mapInstanceId and mapCategory (map_formats compact map models).
	MeshNames     *ordered.Map[string] `json:"meshNames,omitempty"`
	MeshPrototype string               `json:"meshPrototype,omitempty"`
	MapInstanceID string               `json:"mapInstanceId,omitempty"`
	MapCategory   string               `json:"mapCategory,omitempty"`
}

// EntityWrapper is model.get('isEntity', True).
func (m *Model) EntityWrapper() bool { return m.IsEntity == nil || *m.IsEntity }

// ModelPresent is model.get('hasModel', True).
func (m *Model) ModelPresent() bool { return m.HasModel == nil || *m.HasModel }

// Material is a materials.MaterialResolver descriptor (materials.py
// descriptor()). It is defined here because Model refers to it and the
// materials package imports this one. Keys after Maps are conditional.
type Material struct {
	Name        string                     `json:"name"`
	SourcePath  string                     `json:"sourcePath"`
	ShaderID    uint32                     `json:"shaderId"`
	BaseColor   [3]float64                 `json:"baseColor"`
	Roughness   float64                    `json:"roughness"`
	Metalness   float64                    `json:"metalness"`
	Emissive    [3]float64                 `json:"emissive"`
	Opacity     float64                    `json:"opacity"`
	AlphaMode   string                     `json:"alphaMode"`
	AlphaCutoff float64                    `json:"alphaCutoff"`
	DoubleSided bool                       `json:"doubleSided"`
	Maps        *ordered.Map[*MaterialMap] `json:"maps"`

	EmissiveIntensity *float64 `json:"emissiveIntensity,omitempty"`
	EmissiveMode      string   `json:"emissiveMode,omitempty"`
	EmissiveUsesAO    *bool    `json:"emissiveUsesAO,omitempty"`
	// UnbakedMaps is added by export_textures for maps that could not be baked.
	UnbakedMaps []string `json:"unbakedMaps,omitempty"`
}

// MaterialMap binds a material role to a texture (materials.py add_map).
// Path, RelativePath and Source are added by export_textures, whose baked
// maps have no textureId (registered maps always have a non-empty one).
type MaterialMap struct {
	TextureID    string     `json:"textureId,omitempty"`
	UVIndex      int        `json:"uvIndex"`
	ColorSpace   string     `json:"colorSpace"`
	Repeat       [2]float64 `json:"repeat"`
	Path         string     `json:"path,omitempty"`
	RelativePath string     `json:"relativePath,omitempty"`
	Source       string     `json:"source,omitempty"`
}

// Texture is a texture descriptor registered by the material resolver
// (materials.py _texture), keyed by texture id in Asset.Textures.
type Texture struct {
	Path            string `json:"path"`
	Channel         string `json:"channel"`
	Transform       string `json:"transform"`
	InputColorSpace string `json:"inputColorSpace"`
}

// Node is one export hierarchy node (build_export_hierarchy). BoneIndex and
// SourceName are present on bones, MeshIndex on meshes; SourceMeshIndex is
// added to mesh nodes by map loading.
type Node struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	ParentID *string `json:"parentId"`
	ModelID  string  `json:"modelId"`
	Matrix   Mat4    `json:"matrix"`

	BoneIndex       *int    `json:"boneIndex,omitempty"`
	SourceName      *string `json:"sourceName,omitempty"`
	MeshIndex       *int    `json:"meshIndex,omitempty"`
	SourceMeshIndex *int    `json:"sourceMeshIndex,omitempty"`
}

// MeshPrototype is shared geometry of compact map assets (map_formats).
type MeshPrototype struct {
	Meshes []*Mesh `json:"meshes"`
}

// Asset is load_asset's result. Models form a hierarchy by ID/ParentID.
// Later layers add keys that load_asset never sets; they are absent until set:
// objectNames (api: accepted saved names), meshPrototypes and map
// (map_formats.load_map; Map holds its summary object).
type Asset struct {
	Name            string                 `json:"name"`
	Path            string                 `json:"path"`
	Models          []*Model               `json:"models"`
	Textures        *ordered.Map[*Texture] `json:"textures"`
	TextureRoot     string                 `json:"textureRoot"`
	Warnings        []string               `json:"warnings"`
	ExportHierarchy []Node                 `json:"exportHierarchy"`

	MeshPrototypes *ordered.Map[*MeshPrototype] `json:"meshPrototypes,omitempty"`
	Map            any                          `json:"map,omitempty"`
	ObjectNames    *ordered.Map[string]         `json:"objectNames,omitempty"`
}

// EntityChild is one child reference of an .owentity.
type EntityChild struct {
	Reference  string `json:"reference"`
	Attachment string `json:"attachment"`
}

// Entity is _read_entity's result.
type Entity struct {
	Name         string        `json:"name"`
	Model        string        `json:"model"`
	Children     []EntityChild `json:"children"`
	Relative     string        `json:"relative"`
	MaterialLook string        `json:"materialLook"`
}

// ClipHeader is read_clip_header's result (the 16-byte header).
type ClipHeader struct {
	Name            string  `json:"name"`
	Path            string  `json:"path"`
	FPS             float64 `json:"fps"`
	DurationFrames  int64   `json:"durationFrames"`
	LastFrame       int64   `json:"lastFrame"`
	DurationSeconds float64 `json:"durationSeconds"`
	BoneCount       int     `json:"boneCount"`
	Size            int64   `json:"size"`
	Version         string  `json:"version"`
}

// Track is one sorted keyframe track. Values are flattened per frame: three
// components for positions and scales, four (unit XYZW) for rotations.
type Track struct {
	Frames []uint32  `json:"frames"`
	Values []float64 `json:"values"`
}

// ClipBone holds a bone's tracks; absent tracks are omitted like in Python.
type ClipBone struct {
	Name      string `json:"name"`
	Positions *Track `json:"positions,omitempty"`
	Rotations *Track `json:"rotations,omitempty"`
	Scales    *Track `json:"scales,omitempty"`
}

// Clip is load_clip's result.
type Clip struct {
	ClipHeader
	Bones    []ClipBone `json:"bones"`
	Warnings []string   `json:"warnings"`
}

// ClipBindingMetadata is read_clip_binding_metadata's result.
type ClipBindingMetadata struct {
	FirstPose []ClipBone `json:"firstPose,omitempty"`
	Path      string     `json:"path"`
	BoneNames []string   `json:"boneNames"`
	BoneCount int        `json:"boneCount"`
	Warnings  []string   `json:"warnings"`
}
