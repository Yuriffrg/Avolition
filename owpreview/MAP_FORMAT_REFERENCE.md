# OWMAP reader and static scene contract

The parser follows actual DataTool records. Primary sources inspected:

- [OWLib Map.cs](https://github.com/overtools/OWLib/blob/develop/DataTool/SaveLogic/Map.cs),
  also available locally at `work/owlib/OWLib-develop/DataTool/SaveLogic/Map.cs`.
- [io_scene_owm ImportMap.py](https://github.com/overtools/io_scene_owm/blob/main/importer/ImportMap.py).
- [io_scene_owm BLMap.py](https://github.com/overtools/io_scene_owm/blob/main/importer/blender/BLMap.py).
- The matching `OWMapReader.py` and `MapTypes.py` in the supplied Overwatch Maps
  importer (`work/map-plugin/Overwatch Maps/owm_maps_c4d`). Its legacy 2.0 light
  layout was checked separately from the current 2.1 writer.

These links identify mutable upstream branches; no commit hash or game-build
compatibility is claimed from them.

## Binary layout

Supported versions are **2.0 and 2.1**. Numeric fields are little endian;
strings use BinaryWriter's 7-bit UTF-8 byte length. The header contains two ushort
version fields, map name, then uint object/detail/light counts. Objects contain
a model path followed by material-look groups with placement records. Details
contain a model/entity path, look path and one placement. Placements store
translation XYZ, scale XYZ and quaternion XYZW. Lights have version-specific
records; sound positions and paths follow. Unknown versions and corrupt sections
fail explicitly.

Map.cs writes references relative to the map's containing directory. Keep each
`.owmap` beside its exported `Models` and `Entities` folders. Absolute, drive-
qualified, parent-traversing and symlink-escaping map references are rejected.

## Adapter

`load_map(path, root_path=None, compact=True, limits=None)` returns the existing
asset shape plus `map` statistics and `meshPrototypes`. It delegates model/entity,
joint, mesh and base-material reading to `formats.load_asset`. Explicit look
overrides use the current material reader and recursively affect an entity's
base model and child models, as `BLMaterial.bindEntityLook` does. Slots absent
from a partial override retain their source binding.
Missing dependencies are reported while available geometry remains usable. A
map with no loadable meshes produces an actionable error.

Placements have stable IDs based on record index. Parent IDs, actual attachment
bones and socket transforms remain intact. Column-major matrices preserve TRS
in raw OW Y-up metres, including signed/nonuniform scale. Map placement and
socket transforms are applied to native export hierarchy nodes too, so the map
export follows the preview. Established animation attachment behavior is unchanged.

`meshPrototypes[ID].meshes` holds one geometry copy per source model/submesh set; placed model
records have `meshPrototype` and individual materials. This avoids duplicating
large arrays in preview and bridge JSON. The renderer shares BufferGeometry.
`expand_map_asset` makes shallow wrappers for existing geometry consumers; numeric
arrays remain shared. `meshNames` overrides affect only an individual placement.

The installed Blender 5.1 `io_scene_owm` importer was inspected on 2026-10-13.
Its `SettingTypes.removeCollision` defaults to true and `BLMap.init` removes
meshes whose assigned material GUID is in `shader_metadata.CollisionMaterials`.
The map adapter follows that exact allowlist. It does not infer collision from
shader number, mesh name, color, or missing texture. Different looks can remove
different submeshes, so filtered geometry has a distinct shared prototype.
Parent transforms remain for child attachments. Kept geometry and graph mesh
nodes retain `sourceMeshIndex` so existing exported-name overrides keep targeting
the same source submesh after filtering. Graph `meshIndex` remains the compact
array index. Collision-only material and texture descriptors are dropped.

No geometry is silently truncated. Defaults cap file size at 128 MiB, placements
and model nodes at 100,000, placed meshes at 200,000, hierarchy nodes at 500,000,
and unique vertices at 10 million. Existing per-model limits also apply. These
bounds reject oversized inputs; they do not guarantee interactive performance
at the maximum sizes on every machine.

## Static C4D export

`map_export.export_map(asset, output_folder, scene_name=None, bake_textures=True,
bridge=None)` takes the loaded/renamed asset and uses `export_bundle`, the current
material baker, and `BridgeClient`. Shared appearances and identical material
descriptors bake once per export, with no persistent pixel cache. The bridge
receives compact geometry and no animation tracks. Existing native conversion
provides centimetres/Z reflection, normals, UVs, skin bindings and editable
Octane materials. Geometry becomes separate native Cinema mesh objects.

Bridge **0.7.3** advertises `mapInstances: true`. Export checks this capability
before creating output, so an old bridge cannot silently save an empty map.
Older bridges still work for established animation exports. Normal bridge size
limits and detached-document verification apply. Transactional publication never
overwrites a differing scene and does not publish a failed native save.

## Limitations and verification

Lights and sounds are read/counted but not previewed or exported; maps containing
them show warnings. Map.cs does not serialize effect placements, so effects,
gameplay logic and environmental shading are not reconstructed. Preview uses
the existing viewport lighting and entity material support. Zero scale can be
previewed but native export rejects it; invalid quaternions/nonfinite placements
fail parsing.

`test_map_formats.py` uses synthetic OWLib bytes with real model/material readers
for repeated placements, looks, mirrored/nonuniform TRS, entity attachments,
versions, missing assets and bounds/path checks. `test_map_export.py` uses real
staging/publication with a mocked bridge and the existing fake Cinema API.
Those checks do not prove a game extraction or native Cinema save. A local
King's Row (Winter), Evening - Hybrid export became available for read-only
material coverage checks on 2026-10-13. The baseline parser bound 7,512 of 7,576
meshes, including 7,137 with base-color maps and 3,415 registered texture
operations. This ruled out widespread missing material-key associations for
that map; the viewport's former 128-texture budget was a separate bottleneck.
With Blender's collision-material exclusion, 1,129 engine hit-test meshes are
omitted; 6,447 visible meshes remain, with 6,383 bindings, 6,010 base-color maps,
and 3,344 texture operations. The 148 omitted placements reference existing but
empty sources: five unique models contain no meshes/bones, and nineteen unique
entities contain no model or children. The 64 unbound mesh slots are absent
from the referenced model looks; no arbitrary replacement material is assigned.
`tools/audit_map_materials.py` reproduces coverage diagnostics without modifying
source files. Layered game shaders remain common-PBR approximations. A native
map-to-Cinema save remains separately unverified by these parser tests.

`tools/audit_map_texture_bake.py` sampled the richest material descriptor for
each of the thirteen shader IDs present in that map. The real bake produced
59 PNGs (2,994,641 bytes) at their source resolutions, ranging from 4×4 to
1024×1024. SHA-256 checks of all 63 involved source files (map, materials,
textures) matched before and after. This exercised base color, packed normals,
roughness/metalness, alpha and the common maps of layered materials. It does
not claim reconstruction of secondary shader-36/37 blend layers. AO that uses
a different UV set/repeat remains separate; four sampled materials retain an
explicit unresolved emission-mask warning, which native Octane export skips
rather than treating the mask as final emission color. Results were written
under `work/preview-build/map-native-texture-review`, outside the game export.
