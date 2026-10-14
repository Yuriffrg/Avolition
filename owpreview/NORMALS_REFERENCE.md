# C4D normal export investigation (Whitehound 0.8.3)

The reported triangular shading disappears when the user removes Normal tags.
The correction in this release retains those tags and the authored directions,
normalizing their vectors before sending the scene to Cinema 4D.

## Compared approaches

- **Axis conversion and corner order:** the original Entities importer and
  Whitehound both reflect Z and reverse triangle winding. Normals follow the
  same corner order, including the repeated fourth corner of a C4D triangle.
  Real-source checks found no normals opposing the transformed face winding.
  No additional axis flip or winding change is justified.
- **Packed versus vector API:** Cinema's `NormalTag.Set` accepts four vectors.
  Multiplying them by 32000 is appropriate only for writing packed raw normal
  buffers, not this API. The exporter continues using vector normals.
- **Blender conversion:** the reference importer assigns custom vertex normals
  with `normals_split_custom_set_from_vertices`. A Blender 5.1.2 background
  probe with `(0.1, 0.2, 0.96)` confirmed the resulting corner normals have unit
  length. Whitehound previously sent the game's short vectors directly to C4D.
- **Recomputing Phong normals:** this changes authored shading directions and
  potentially hard edges. It was not made the default and tags are not deleted.
  Current Cinema supports Normal tags with topology-preserving deformation.

## Evidence and scope

All 18,415 source normals in the available Gameplay1P entity were finite and
nonzero but had lengths from 0.986388 to 0.998381. The normalized export has a
maximum unit-length error of 2.22e-16. Direction, topology, weights, hierarchy,
UVs and source asset data are unchanged. The audit covers 25,448 triangles.
Development report: `work/animation-research/normal-qa-v083/real-gameplay1p-normal-report.json`.

Normalization happens in `c4d_export.make_payload`, before the existing bridge
receives the asset. Bridge 0.7.2 therefore needs no reinstall. Shared source
data, viewport normals and FBX output are not changed by this C4D correction.
Missing normal arrays still use the existing Phong path. Invalid, zero or
nonfinite supplied directions fail explicitly instead of creating broken tags.

Regression checks cover non-axis-aligned vectors, normalization, reflected and
reordered corners, retained Normal tags, invalid arrays and asset immutability.
These are numerical and API-fixture checks, not an actual Cinema/Octane render.
Normalization fixes a measured Blender/C4D difference, but does not establish
that it fully explains the screenshot. Native visual confirmation is pending.

## Primary references

- [Maxon NormalTag vector API](https://developers.maxon.net/docs/py/2026_0_0/modules/c4d/C4DAtom/GeListNode/BaseList2D/BaseTag/VariableTag/NormalTag/index.html)
- [Maxon NormalTag setup example](https://developers.maxon.net/docs/cpp/2026_1_0/page_manual_normaltag.html)
- [Current Normal tag and deformation behavior](https://help.maxon.net/c4d/en-us/Content/html/TNORMAL.html)
- [Maxon SDK face-normal calculation](https://developers.maxon.net/forum/topic/4883/4791_polygon-position/22)
