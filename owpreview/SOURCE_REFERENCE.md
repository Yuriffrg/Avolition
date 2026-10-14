# Material source reference

Whitehound's preview follows the Overtools Blender importer's material semantics. It is a basic PBR preview, not an exact Overwatch game shader renderer.

Primary upstream sources:

- [OWLib material writer](https://github.com/overtools/OWLib/blob/develop/DataTool/SaveLogic/Model.cs): `.owmat` version 3.0, material/look discriminator, uint64 mesh-key bindings, relative texture paths and uint32 semantic IDs.
- [OWLib model writer](https://github.com/overtools/OWLib/blob/develop/TankLib/ExportFormats/OverwatchModel.cs): file UVs already contain `1-game_v`.
- [Blender shader metadata](https://github.com/overtools/io_scene_owm/blob/main/shader_metadata.py): texture semantic IDs and shader variants.
- [Blender material builder](https://github.com/overtools/io_scene_owm/blob/main/importer/blender/BLMaterial.py): model-look binding, CHANNEL_PACKED alpha, texture color spaces, shader inputs and AO/UV exceptions.
- [Blender node library](https://github.com/overtools/io_scene_owm/blob/main/library.blend), inspected with Blender 5.1.2. SHA256: `6154443444447bbc9b8bb65ea36aefa23ade66ab4e149901dd001a2af741048b`.

## Verified packed-channel math

`OWM: Unpack Blue Channel` decodes a stereographic normal. Source RGB values are linear, normalized to 0..1:

```
f = 1 + 31.875 * B
x = (2*R - 1) / f
y = (2*G - 1) / f
q = 2 / (1 + x*x + y*y)
n = normalize([q*x, q*y, q-1])
RGB = 0.5*n + 0.5
```

There is no green inversion. Use file UVs unchanged. Normal images, PBR data, alpha and AO are linear; base-color images are sRGB.

`OWM: Unpack PBR V2`, with its default zero cloth/material input:

```
h = R > 0.525 ? 1 : 0
metalness = clamp(2*h*R - 1, 0, 1)
specular = clamp(2*(1-h)*R, 0, 1)
roughness = 1 - G
```

Blue is unused by this unpacker. Cloth red mask M additionally creates sheen `M*R`, subtracting half that sheen from metalness and specular. Whitehound's basic subset omits those cloth-specific adjustments.

The unpacker is present in Basic, Glass, Decal through Basic, OW1 Skin, Cloth, OW2 Detail and Refractive groups. OW2 Skin (shader 167, or shader 43 with static input `4081294361`) instead has zero metalness and a detail-driven roughness graph. Hair and eye shaders also have special handling; a generic packed-metalness interpretation must not be applied indiscriminately.

## Emission and transparency

Generic emission texture `3166598269` and skin emission `1523270506` are linear scalar masks, converted from RGB with Blender luminance weights `(0.21263909, 0.71516913, 0.07219274)`. They must tint the albedo, not create white emission. Glass emission `1493127177` is actual sRGB color.

With linear albedo C, AO A, emission mask E and scalar strength S:

- Basic: `C*A*E*S`, default S=16.
- OW2 Detail/eye: `C*E*S`, default S=16.
- OW2 Skin: `C*E*S`, default S=32.
- Decal: Basic formula, default S=128.
- Glass: separate emission color times S, default S=0.

Static float inputs `3250491852` and `2446772623` override emission strength. Cloth, OW1 skin and hair have additional emission operations; omitting their unsupported operations is preferable to silently interpreting masks as colors.

Generic albedo alpha is **AO**, not transparency, for shaders 217 and 44. Hair and glass albedo semantic IDs explicitly use image alpha. General AO `3335614873` uses the second UV set; hair AO `3761386704` uses the first. Decal alpha is `max(red,1-opacity)` with static opacity `565460110`. Blender uses HASHED blending, so any fixed alpha-test cutoff in the preview is an approximation.

## Preview limits

The supported subset is per-mesh material binding, base color, decoded tangent-space normals, packed roughness/metalness, explicit alpha/AO and supported emissive masks/colors. Missing or unsupported maps retain usable shaded geometry. Skin subsurface, layered/detail shaders, cloth sheen, strand anisotropy, exact glass refraction and shader-specific specular controls require fuller shaders and are not represented faithfully by MeshStandardMaterial. Three AO changes indirect lighting whereas Blender Basic multiplies albedo; that distinction remains an approximation.

The metadata identifies basic UV scale `1883253226`, but the upstream material builder has a guard which appears to skip several intended UV selectors. Applying that known scale is intended metadata behavior, not a claim of matching this upstream bug. Do not blindly multiply the unrelated default scale `2166182138`.

Development evidence is stored outside the application in `work/animation-research/material_nodes.json`, `material_probe.json`, their reproducer scripts and `MATERIAL_RESEARCH.md`. Constant-node Blender renders confirmed normal `(0.75,0.25,0.02)` becomes `(0.757354736,0.242645234,0.842836857)` and PBR `(0.6,0.2,0)` becomes `(metalness=0.200000048,specular=0,roughness=0.800000012)` before 8-bit preview quantization.
