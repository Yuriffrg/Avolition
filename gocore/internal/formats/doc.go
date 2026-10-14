// Package formats reads DataTool's OW2 model, entity and animation preview
// data. It is the Go port of owpreview/formats.py (SPDX-License-Identifier:
// GPL-3.0-or-later).
//
// Format layouts are based on the supplied OWLib/TankLib exporters and the
// io_scene_owm readers shipped with Overwatch Tools. All output transforms
// use raw OW Y-up coordinates, quaternions in XYZW order and column-major
// matrices for Three.js.
//
// The exporter rotates mesh vertices +90 degrees around X but leaves bones and
// animation tracks in the original coordinates. Undo only that mesh
// conversion. Bone Euler rotations are Blender XYZ (Rz @ Ry @ Rx); rest scale
// is one, matching the successfully tested Blender/C4D armature importer. Clip
// tracks are returned as exported; the playback evaluator applies Blender's
// parent-local child poses and rest-basis root-position behavior.
//
// # Python → Go map
//
//	_Reader                     Reader (ReadFile, NewReader, NewStreamReader)
//	_Reader.take/unpack/string  Take, Unpack (+ U8…U64, I16, I32, F32), String
//	_Reader.values('f'|'H'|'I') Float32s / Float64s, Uint16s, Uint32s
//	_Reader.count/fail          Count, Fail / Failf
//	_version                    CheckVersion
//	_path                       CheckPath
//	_matrix/_multiply           Compose / Multiply
//	_inverse_rigid/IDENTITY     InverseRigid / Identity
//	_euler_xyz                  EulerXYZ
//	_normalize_quaternion       NormalizeQuaternion
//	_bone_globals/_skin         BoneGlobals / Skin
//	_to_y_up                    fused into mesh decoding
//	_load_model                 LoadModel
//	_Resolver                   Resolver (NewResolver, Find, CleanReference)
//	_read_entity                ReadEntity
//	load_asset                  LoadAsset / LoadAssetWith
//	build_export_hierarchy      BuildExportHierarchy
//	read_clip_header            ReadClipHeader
//	read_clip_binding_metadata  ReadClipBindingMetadata
//	load_clip                   LoadClip
//	FormatError                 *FormatError (OSError family: *OSError)
//
// # JSON
//
// json.Marshal of Asset, Clip, ClipHeader and ClipBindingMetadata produces
// the Python dicts key for key. Python None is a nil pointer (JSON null);
// keys Python adds only conditionally are omitted while unset. Mesh vertex
// data keeps the file's float32 values ([]float32); quantities Python
// computes in float64 (bones, matrices, skin weights, clip values) are
// float64. AppendAssetJSON and AppendClipJSON encode the same objects without
// reflecting over the bulk numeric arrays; prefer them for API replies.
//
// Keys added after load_asset by other layers already exist on the structs
// and stay absent until set: Model.MaterialLook/Materials (materials),
// Model.MeshNames (object names), Model.MeshPrototype/MapInstanceID/
// MapCategory, Mesh.SourceMeshIndex, Node.SourceMeshIndex,
// Asset.MeshPrototypes/Map (map formats), Asset.ObjectNames (API),
// Material.UnbakedMaps and MaterialMap.Path/RelativePath/Source (texture
// export). A layer needing further keys should embed the struct in its own
// type (JSON promotes embedded fields).
//
// # Materials
//
// load_asset constructs materials.MaterialResolver. Because the materials
// package imports this one, it registers a MaterialResolverFactory with
// RegisterMaterialResolver (or callers pass one in LoadOptions). Until then
// LoadAsset uses NeutralMaterials: no materials, no textures, and the same
// textureRoot rule. Package materials registers itself from init, so any
// binary that should produce load_asset's real materials must import it
// (a blank import is enough; mapformats imports it). Material/MaterialMap/Texture are the shared descriptor
// types the materials package fills.
//
// # Errors
//
// Errors are *FormatError (Python FormatError/ValueError) or *OSError
// (FileNotFoundError and other OSErrors), and Error() equals Python's
// str(exception), so tests and the UI see identical messages.
//
// Paths follow pathlib semantics through package pypath; returned paths are
// str(Path.resolve()).
package formats
