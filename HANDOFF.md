# Avolition 0.8.35 and C4D Bridge 0.7.14

Built from the 0.8.34 release baseline. Export Manager calls individual file export Cinema export. Stager rig creation uses its Motion System target as the root-motion object automatically. The Actor override control has been removed. Stager remains independently developed and packaged.

Animations, Maps and Browse models share surface-pivot navigation adapted from Stager 0.22.0. Left/middle/right drag orbit, pan and dolly, with Alt and held 1/2/3 shortcuts. Wheel dolly uses the pointer's surface and empty space uses the previous pivot depth. Viewport navigation tips are removed.

The bridge explicitly creates a linear Motion Clip Timing spline. Cinema's default Bezier identity introduces small sub-frame time shifts, which caused the cinematic1 60 FPS export to fail pose validation. The original validation tolerances, source keys and interpolation are retained. Both standalone exports and newly staged sources use the corrected clip constructor. Install the bundled Bridge 0.7.14 to receive this native fix.
# Avolition 0.8.34 and C4D Bridge 0.7.13

Animation setup and Map export have permanent panels. Each workflow has a separate Library source row, with Scene objects under a disclosure. Export Manager opens on Stager rig and provides individual exports under Manual files. The separate Stager 0.19.1 release supplies the registration repair API for broken links in an existing project.

# Avolition 0.8.33

Animations shares its library source field with the workflow header. The model selector opens Animation setup, a keyboard-accessible popover with the current target, clip assignments, nickname and queue action. Library filters, clip details and viewport settings use native disclosures. The object hierarchy and transport remain visible. The playback row adapts to the width of the preview panel.

# Avolition 0.8.32

The Gameplay3P library filter now considers the selected model, all identical copies of a clip, its joint coverage and the original 5 cm frame-zero head/camera check. It uses measured bindings and retains the saved hideOriginPoses preference. A positive match from the body's own model folder protects partial clips from the cross-model coverage rule. The rule can also recognize transferred motion that drives the torso and both legs.

Toolchain uses a single Load heroes button for its existing catalog job. That job reads heroes and unlocks, then switching hero or category updates assets locally. The Windows executable, window icon and favicon use the supplied artwork. C4D Bridge remains at 0.7.12.

# Avolition 0.8.31 and C4D Bridge 0.7.12

Gameplay3P filtering uses the loaded root body's Head (Camera fallback) in model space at frame zero. The library reads the first pose of its ancestor chain alongside existing batched bone matching. The 5 cm filter applies to browsing. Both backends persist the default-on preference.

Actor setup export accepts different track sets. In the Go combined-set sampler, Actor insertion retries a sheared local transform using frame-zero world scales and the original evaluated joint positions and rotations. A rig import warning identifies the approximation. The bridge writes skin influences for a new Actor using 65,535 integer weight units per point. Registered rigs keep their existing Weight tags on re-import.

The exporter owns its desktop styling, library, export payload and C4D bridge. Stager 0.16.0 continues to own the viewer, bindings and take recording. The released registration API is the only integration entry point used to register the rig. Each project is packaged independently.

# Handoff for Avolition 0.8.30 and C4D Bridge 0.7.11

The Electron host and Go core add `stager_rig_destination(ids)` and
`create_stager_rig(request)`. The first resolves saved entries and reviews the
active document and cameras through the local mailbox. The second addresses
that same bridge instance and document. It samples each entry with the
combined-export evaluator and sends one entity, one texture set and separate
native action payloads. The legacy Python reference intentionally exposes
these two API names with a desktop-core-required error. File export remains
available in both implementations.

# Handoff: 0.8.29, Gameplay1P clavicles start at zero Y

## 0.8.29 follow-up

After Gameplay1P hierarchy cleanup and before Motion Source creation, each
clavicle's local Y curve is offset by its own frame-zero value. Every Y key,
its time and its change relative to the first frame are retained. X/Z and
rotation curves are unchanged. Static rigs start at Y=0 without added tracks.
This intentionally lowers the arms and their attached weapon; Joints and the
original skin bind state receive no compensating height offset.

The existing hierarchy/skin checks run before the intentional height shift.
The offset stage verifies the expected per-arm translation and skin-weighted
deformation. Non-Gameplay1P exports, nickname-based combined clips, no-scale
tracks, exact source FPS and 24 FPS static maps retain their existing behavior.
App 0.8.29 requires the povHeightZero capability from bridge 0.7.10 for
Gameplay1P exports so an older bridge cannot silently omit this step.

Bridge 0.7.10 is installed with all nine files verified. The previous 0.7.9 copy
is backed up outside plugin search paths. After restarting Cinema 4D, a real
saved Widowmaker arms/weapon setup and independent-rig fixture exported through
the installed bridge successfully. See VALIDATION.md for the native key-by-key
comparison and automated check results.

The earlier release notes below are historical.

## 0.8.28 follow-up

Animation documents retain their source FPS (combined setups use the highest
assigned FPS while preserving each clip's duration). Frame keys, document ranges,
Motion Sources and Motion Clips now use rational frame/FPS times throughout.
Cinema's seconds-only BaseTime constructor rounded frame 49 at 30 FPS to 48.99;
the new code keeps it exactly 49 F, with a 50-frame inclusive clip length. Static
maps retain 24 FPS and the necessary 0–1 F project/preview range.

Gameplay1P identity comes from the original source filename, and hero names come
from the Heroes/<hero> source directory. User display renames cannot trigger the
cleanup. The top entity null takes the hero name when available. Before creating
the Motion Source, the native exporter bakes inherited clavicle motion, promotes
Clavicle_L/R below a skeleton null named Joints, and removes unused joints and
empty helper nulls. Following the user's choice, meshes weighted entirely to the
camera branch are omitted along with their now-unused joints. Meshes with mixed
camera/arm weights and necessary attachments remain intact with a warning.

Cleanup verifies every retained object's world motion at every exported frame,
the original skin weights/bind state, and deformed mesh points at start/mid/end.
It never recalculates binding at an animated pose. The combined native Motion
Source/Clip still uses the exact nickname, on Layer 0, with no scale tracks.
Other entity types retain their original hierarchy.

App 0.8.28 requires bridge 0.7.9 capabilities exactFrameTimes for Motion Clip
exports and optimizedGameplay1P for the special rig cleanup. c4d_pov.py is a
stdlib-only native companion module and must be included in bridge bundles.
Native Cinema 4D 2026/Octane tests passed real Widowmaker single/combined exports,
30/60 FPS exact frame boundaries, independent rigs and a static map fixture.
Bridge 0.7.9 is installed and hash-verified, with 0.7.8 backed up outside plugin
search paths. After restart, the installed bridge exported a real persisted
Widowmaker arms/weapon setup and passed the independent-rig native fixture.
See VALIDATION.md for evidence. The user confirmed the removed Widowmaker cards
are HUD effects; the verified scene has no joints outside the clavicle branches.

The earlier release notes below are historical.

## 0.8.27 follow-up

Animation nickname is now a separate saved value (`motionClipName`) from the
generated scene filename. Typing `Reload` creates `Gameplay1P_Reload.c4d`, but
both the Motion Source and Motion Clip are named exactly `Reload`. The input
starts/resets blank. Names retain casing, Unicode and underscores; an explicitly
typed entity prefix remains part of a new nickname. Legacy queue entries without
the new field remove only their exact known entity prefix once. Later annotation
edits do not change saved setup names.

One combined setup now creates one source, one Motion System tag and one clip on
`Layer 0`. Nested arms/weapon rigs use the outer Skeleton null. Independent rigs
share a `Joints` parent; grouping preserves world/bind transforms, and verification
compares all original object poses before/after grouping and native conversion.
Connector hierarchy is retained; unrelated meshes and unassigned rig branches
are omitted from the motion source. Scale tracks remain absent.

App 0.8.27 requires the `combinedMotionClips` capability from bridge 0.7.8. That
bridge is installed, hash-verified and tested after restarting Cinema 4D. Real
Widowmaker arms/weapon exports and independent-rig fixtures have one `Reload`
source/clip after save/reload. The previous release notes below are historical.

## 0.8.26 follow-up

Motion System tags now belong to the animated model's Skeleton null, identified
from the export hierarchy rather than its display name. Each animated rig gets
its own Motion Source and Clip. Sources contain only their own skeleton and
joints, excluding attached entities and other rigs. A single rig keeps the export
name; multiple rigs add their skeleton names to distinguish the clips.

Animation exports create only position/rotation keys. Scale remains at each
joint's first animation-frame value, with no scale CTracks in either the scene
or Motion Sources. Authored scale changes produce a warning because they are
intentionally omitted. Native save/reload checks reject scale tracks and verify
the nested Motion System tag owners, source links and sampled poses.

The app is 0.8.26 and the bundled bridge is 0.7.7. The new
`skeletonMotionClips` capability prevents an older bridge from silently producing
the previous layout. Bridge 0.7.7 is installed with a verified backup of 0.7.6;
after restarting Cinema 4D, the packaged app exported an animation and map through
it successfully. Real Widowmaker arms-only and arms/weapon exports passed native
checks; see `VALIDATION.md`. The previous release notes below are historical.

## 0.8.25 follow-up

- Animations has a **POV cam** viewport option, off by default. It follows an
  available Camera joint, including its animation and parent transforms; selecting
  an attached weapon retains the owning arms camera. Turning it off restores the
  orbit view. Entities without a Camera joint and map views retain normal navigation.
  This is a preview option and does not add an exported camera.
- In **For selected object** sorting, equally relevant bone matches favor clips
  matching another assigned object's duration within one frame. An assigned parent
  rig is preferred as the reference for an attached weapon. Source and bone relevance
  remain ahead of duration, and explicit alternative sorts remain unchanged.
- Static map documents use **24 FPS**, current time 0 F, and project/preview ranges
  **0–1 F**. Cinema 4D turns a 0–0 F range into −1–0 F, so the one-frame range is
  necessary to keep both minima at zero. Animation exports retain their clip FPS.
- Animated exports create a native Motion Clip and Motion Source named after the
  export. Position, Rotation and Parameter tracks are included and removed from the
  original hierarchy; Scale and PLA are excluded and retained. Expressions are not
  baked and pictures are not created. Save/reload verification checks the source,
  clip links, timing and sampled joint poses against the original animation.

These exports require bundled **Avolition C4D Bridge 0.7.6**. Both Go and Python
clients check its capabilities. The installed bridge was updated to 0.7.6 after
Cinema 4D closed, with all eight bundled files hash-verified and the previous
plugin backed up outside the plugin search path. After restart, the packaged Go
app exported an animation and map successfully through the installed plugin.
The larger native checks used an isolated temporary 0.7.6 service in Cinema 4D
2026 with Octane. All checks operated on detached documents.

Real Widowmaker animation and full Colosseo map exports passed native save/reload
checks. See `VALIDATION.md` and
`../preview-build/avolition-v0825-qa/real-native-export-report.json`. The existing
Windows external-Python-service map oracle race remains separate from these
successful native exports. The Go backend migration notes below are historical.

## 0.8.24 PNG export compatibility

Windows follow-up: see `VALIDATION.md` for native build, renderer, real-map, game-extraction and differential results. Version 0.8.24 fixes publishing Go-baked PNGs over equivalent Python-baked textures. The actual Colosseo map now exports successfully through live C4D/Octane, with all existing output files preserved. The broader Windows Go suite still has unresolved compatibility-test failures. The Linux results below are not a Windows release sign-off.

This release replaces the Python backend of 0.8.22 with a Go backend. The
interface, saved settings, export list, file formats and Cinema 4D bridge are
unchanged. This file explains what changed, how it was checked, how fast it is,
and what is still open. Coding agents: also read `AGENTS.md`.

## What changed

| Area | Before (0.8.22) | Now (0.8.23) |
|---|---|---|
| Backend `owio-core.exe` | `app.py` + `owpreview/` frozen with PyInstaller (CPython, NumPy, Pillow) | Go, `gocore/` (one ~9.9 MB executable, no Python runtime) |
| Large replies | JSON text (meshes as number arrays, textures as base64 data URIs) | Binary frames → typed arrays in the renderer (JSON still supported) |
| Texture requests | answered one at a time | consecutive `load_texture` calls decode in parallel |
| Preview textures | PNG, decoded by the browser | BC7 (or BC1/BC3) GPU-compressed with mips when the GPU supports it, else PNG |
| Map drawing | one draw call per mesh per placement | repeated props batched into instanced draws (`web/map-batching.js`) |
| Build | `build.ps1` (PyInstaller) | `build.ps1` builds Go by default; `-PythonCore` for the old backend |
| Cinema 4D companion | Python | Python, unchanged (runs inside Cinema 4D) |

`app.py` and `owpreview/*.py` stay in the repository as the **reference
implementation**: Go tests call them to compare outputs, and
`gocore/bench/difftest.py` replays identical requests through both cores. They
are not shipped. `c4d_bridge.py` (`BridgeService`), `c4d_scene.py` and
`c4d_octane.py` are still live code inside the companion plugin.

Files outside `gocore/` that changed:

- `desktop/main.cjs`, `desktop/frames.cjs` (new), `desktop/frames.test.cjs`
  (new): binary frame decoding in the host. `AVOLITION_BINARY_ARRAYS=0` forces
  JSON.
- `web/viewer.js`, `web/materials.js`: accept typed arrays and PNG bytes
  (still accept plain arrays and data URIs).
  `web/viewer.js` also asks for the high-performance GPU and compiles each new
  material's shader in the background before first use (avoids frame stalls,
  which ANGLE on Windows makes long); `web/materials.js` keeps up to one
  texture request per CPU core in flight (4–8, was 4).
- `web/map-batching.js` (new): instanced drawing for static maps (see
  "Batched map rendering").
- `desktop/preload.cjs`, `desktop/main.cjs`, `web/materials.js`: compressed
  texture negotiation (see "Compressed preview textures").
- `build.ps1`, `tools/package_desktop.py`: Go build by default, ship
  `frames.cjs`, no Python runtime or Python license files in a Go build.
- `licenses/GO-LICENSE.txt`, `GOLANG-X-IMAGE-LICENSE.txt`,
  `GOLANG-X-SYS-LICENSE.txt`, `THIRD_PARTY_NOTICES.md`: Go notices.
- Version 0.8.23 in `app.py`, `desktop/package.json`, `web/index.html`,
  `tools/package_desktop.py`, `gocore/internal/api/api.go` and the tests that
  check it.

## Protocol

Requests are still one JSON object per line. A host that adds
`"binary": true` (the new `desktop/main.cjs` does) gets `load_entity`,
`preview_entity`, `load_map`, `load_animation` and `load_texture` replies as a
**frame**: the usual reply JSON line plus a `blobs` list, padded to 8 bytes,
followed by the raw little-endian arrays (each padded to 8 bytes). In the
result, `{"$blob": N}` marks where array N goes. Everything else stays a JSON
line. The Python core ignores the key and answers in JSON; an old host never
sends it. Details: `gocore/README.md`.

Texture loads may now be answered out of order (the host matches replies by
`id`, as it always did). Any other request waits for running texture loads
first, so state changes keep Python's strict order.

## How parity was checked

- **Module level:** every `owpreview` module has a Go package with the Python
  tests ported (603 Go test functions in total) plus oracle tests that run the
  Python function on the same input and compare results, warnings and error
  messages.
- **API level:** `gocore/bench/difftest.py` sends the same request sequences
  (reads, catalog changes, exports, map export jobs, errors) to both cores and
  compares every reply and the final `catalog.json`: **0 differences in 1,205
  replies** (demo-assets, material-assets, and the 545 MB benchmark library),
  identical `catalog.json`.
- **Renderer:** the 13 browser tests that pass on the Python core in a Linux
  container also pass on the Go core: `test_export_queue_ui`, `test_maps_ui`,
  `test_object_search_ui`, `test_object_ui`, `test_objects_tree_ui`,
  `test_texture_ui`, `test_map_library_ui`, `test_map_export_ui`,
  `test_model_browser_ui`, `test_object_rename_ui`, `test_objects_lazy_ui`,
  `test_toolchain_maps_ui`, `test_toolchain_ui`.
- **Desktop:** Electron 44 ran headless (Linux, xvfb, software WebGL) with the
  new host against the Go core in binary mode, against the Python core, and
  with the old host against the new core; `tests/test_desktop.cjs` and
  `tests/test_toolchain_desktop.cjs` passed in the packaged layout.

### Known, intentional differences

- Numbers: Go writes float32 data in shortest form (`0.3` instead of
  `0.30000001192092896`) and `1` instead of `1.0`; values are identical once
  stored in a Float32Array. Some reply key orders differ; `catalog.json` is
  byte-identical.
- PNG files (texture previews and baked `tex/*.png`) decode to identical pixels
  but compress differently, so their bytes differ. Since 0.8.24, bundle publication
  reuses existing metadata-free PNGs when their headers and decoded pixels match
  exactly. This fixes map exports into pre-Go output folders while preserving
  their original bytes and timestamps. Different pixels, precision, metadata or
  corrupt files still fail safely; scene and clip comparison remains byte-exact.
  Direct texture baking still allocates `_2` names on differing bytes.
- JPEG textures decode within 3 levels of Pillow (Go's IDCT differs);
  JPEG-compressed TIFFs are not supported. All other formats are bit-identical
  to Pillow 12.3.
- FBX: identical content, but a few skin matrix values differ in the last bits
  (NumPy/LAPACK inversion) and zlib streams differ.
- Some messages that quote an operating-system error use Go's wording.
- A few malformed inputs that crashed the Python core now return an error
  message instead.

## Performance

Synthetic library from `gocore/bench/make_library.py` (545 MB, no game data),
Linux container with 4 shared CPU cores. "Go (binary)" is what the desktop app
uses; "Go (JSON)" is the same core answering in JSON. Times are from request
to the last reply byte received; memory is peak resident memory of the
backend process tree. Averages of two runs each.

| Step | Python 0.8.22 | Go (JSON) | Go (binary) | Speed-up |
|---|---:|---:|---:|---:|
| Startup + bootstrap | 127 ms | 4.6 ms | 6.6 ms | ~20× |
| Scan library (721 clips, 49 entities) | 215 ms | 72 ms | 82 ms | ~2.6× |
| Load heavy character (640k vertices, 200 bones) | 5,660 ms | 1,433 ms | 242 ms | **~23×** |
| Load 5 textures, 2048 px | 3,054 ms | 621 ms | 382 ms | ~8× |
| Load animation (200 bones × 240 frames) | 87 ms | 16 ms | 4.4 ms | ~20× |
| Load 30 animations (120 bones) | 575 ms | 150 ms | 35 ms | ~16× |
| Load 35k-vertex character ×10 | 353 ms | 62 ms | 14 ms | ~25× |
| Load map (300 props, 3,600 placements) | 3,574 ms | 594 ms | 315 ms | **~11×** |
| Load 80 map textures, 512 px | 4,136 ms | 1,112 ms | 861 ms | ~4.8× |
| Reply size, heavy character | 139 MB | 64 MB | 46 MB | 3× smaller |
| Peak backend memory, whole session | 930 MB | 278 MB | ~200 MB | **~4.6× less** |

The Go numbers above exclude renderer parsing; the binary format also removes
most of the work on the Electron side:

Time from request until the sandboxed renderer holds the reply's typed
arrays, through the real Electron 44 host path (`desktop/frames.cjs` → IPC →
context bridge), `gocore/bench/transport_electron`, medians of 3:

| Reply | Python 0.8.22 (JSON) | Go (JSON) | Go (binary, shipped) | Speed-up |
|---|---:|---:|---:|---:|
| Heavy character (640k vertices) | 7,549 ms, 146 MB | 3,029 ms, 67 MB | **403 ms**, 49 MB | **~19×** |
| Animation (200 bones × 240 frames) | 113 ms, 1.4 MB | 22 ms, 1.1 MB | **9 ms**, 0.9 MB | ~13× |
| Map (3,600 placements) | 4,394 ms, 72 MB | 1,437 ms, 36 MB | **433 ms**, 27 MB | **~10×** |

Other measurements:

| Task | Python | Go | Speed-up |
|---|---:|---:|---:|
| C4D export payload, heavy character | 7.1 s | 0.06 s | ~100× |
| FBX export, 200k vertices | 5.8 s | 0.47 s | ~12× |
| Bake 2048² textures, heavy character | 6.8 s | 1.1 s | ~6× |
| Map export with texture baking | 52.5 s | 7.2 s | ~7× |
| Animation analysis, 721 clips | 14.2 s | 1.7 s | ~8× |
| difftest request sequence on the benchmark library | 141 s | 28 s | ~5× |
| Shipped backend size | PyInstaller folder (CPython + NumPy + Pillow) | 9.9 MB single exe | |

The container's 4 cores are shared and barely scale; the Go core parallelises
file reading, texture decoding/encoding and baking, so a desktop CPU with more
cores should gain more than shown here.

## Compressed preview textures

Preview textures can now travel as GPU block-compressed mip chains (BC7, or
BC1/BC3 without BPTC) instead of PNGs: no image decoding in the browser, no
GPU mip generation, and 4–8× less upload and video memory. It is negotiated
(renderer → `window.pywebview.setTextureFormats` → host adds
`"textureFormats"` to binary `load_texture` requests), so the Python core,
JSON replies and old hosts are unchanged: `difftest.py` still reports 0
differences. Design, formats and reply shape: `gocore/README.md`
"Compressed textures". Files: `gocore/internal/bcenc` (new encoder),
`gocore/internal/textures/compressed.go`, `api` (`stdio.go`, `server.go`,
`frames.go`, `dispatch.go`, `wire.go`), `protocol`, `desktop/main.cjs`,
`desktop/preload.cjs`, `web/materials.js`, one line in `web/viewer.js`
(passes the renderer to `MaterialLibrary`), tests
`tests/test_compressed_textures.mjs`, `tests/test_compressed_texture_ui.cjs`,
benchmark `gocore/bench/texture_renderer.cjs`.

Measured with `gocore/bench/texture_renderer.cjs` (headless Chromium,
SwiftShader, the shared 4-core container under load from other jobs, cold
core caches, medians of 3):

| Textures | Mode | Last reply | Textures built | Upload + finish | Total | Received | GPU memory |
|---|---|---:|---:|---:|---:|---:|---:|
| Big character, 5 × 2048² | PNG | 1,043 ms | 1,057 ms | 942 ms | 1,999 ms | 22.9 MB | 111.8 MB |
| Big character, 5 × 2048² | BC7 | 2,128 ms | 2,129 ms | 56 ms | 2,185 ms | 28.0 MB | 28.0 MB |
| Bench map, 80 × 512² | PNG | 1,836 ms | 1,841 ms | 1,909 ms | 3,680 ms | 35.0 MB | 111.8 MB |
| Bench map, 80 × 512² | BC7 | 2,695 ms | 2,696 ms | 41 ms | 2,737 ms | 28.0 MB | 28.0 MB |

The browser side (decode, upload, mip generation) drops from ~1–2 s to
~50 ms and video memory 4×; the core spends that time encoding instead
(BC7 costs ~1.2–1.9× a PNG encode per 2048² texture, ~0.25–0.4 s on this
loaded machine) until its cache holds the result. The synthetic library's
textures are smooth and compress well as PNG, so compressed replies carry
more bytes here; real textures are usually the other way round. On a real
GPU the upload saving is smaller than SwiftShader's, the decode saving the
same.

Intentional differences from the PNG path: compressed previews are lossy
(see the PSNR numbers in the README section); mips are box-filtered by the
core (in linear light for sRGB colour maps, as GPUs do); BC7 may decode the
alpha of opaque previews as 254 so that black and white stay exact.

Checked in Chromium with SwiftShader, which exposes BPTC and S3TC:
`tests/test_compressed_texture_ui.cjs` renders every fixture map through the
PNG and the BC7 and BC1 paths and compares them at full size and minified
(orientation, colour space, mips). Not checked on a real GPU/ANGLE-D3D11:
upload speed, driver acceptance of non-power-of-two compressed sizes, and
visual quality on real Overwatch textures.

## Batched map rendering

Static map scenes (`PreviewViewport._staticScene`) no longer draw one mesh per
placement. `web/map-batching.js` draws placements that share geometry and the
material currently shown as one `THREE.InstancedMesh`; wire overlays batch the
same way. The per-placement meshes stay in the scene graph as the source of
truth (object tree, visibility, renaming, framing, texture loading); their own
draw is switched off with an empty layer mask. `sync()` (on invalidated
frames) follows material/visibility changes, `cull()` (every rendered frame)
keeps per-instance frustum culling. Mirrored placements batch separately with a
mirrored batch matrix (same front-face flip as before); transparent and skinned
meshes still draw themselves; animating a map dissolves the batches.
Characters are untouched. Test: `tests/test_map_batching.mjs`.

`gocore/bench/render_map.cjs` on the benchmark map (3,600 placements, 300
props, textured), headless Chromium with SwiftShader, so draw calls are exact
and times only indicative (rasterising 11M triangles in software takes ~24 s a
frame and hides the GPU-side gain):

| | before | after |
|---|---:|---:|
| Draw calls, overview | 3,601 | 301 |
| Draw calls, close view | 64 | 57 |
| Triangles (overview / close) | 10.95M / 192k | 10.95M / 192k |
| `renderer.render()` CPU time per orbit frame | 34.8 ms | 14.4 ms |
| JS heap after textures | 31.3 MB | 33.4 MB |

Screenshots from the same camera: close view identical (max 2/255); overview
0.004% of pixels differ (max 33/255, instance order at depth ties).

## If something breaks

Start with the log: `%LOCALAPPDATA%\Avolition\desktop\avolition.log`. The
host writes the core's stderr there (`core: ...` lines), plus renderer console
messages. The fastest way to isolate a problem is to switch one change off at a
time:

| Switch | Effect |
|---|---|
| `AVOLITION_BINARY_ARRAYS=0` | host asks for plain JSON replies (no binary frames) |
| `AVOLITION_COMPRESSED_TEXTURES=0` | PNG textures instead of BC7/BC1/BC3 |
| `AVOLITION_BACKEND_PATH=<path>` | run a different `owio-core.exe`, e.g. a `build.ps1 -PythonCore` build of 0.8.22's backend, to see whether a bug is the Go port or something else |

Likely failure points, why, and where to look:

- **"The local importer stopped" at launch / blank window.** The Go core
  exited. Run `resources\backend\owio-core.exe --self-test report.json` in a
  terminal to see the error. Code: `gocore/cmd/owio-core/main.go`,
  `gocore/internal/api/api.go` (`New`: catalog loading and migration).
- **Saved labels, export list or settings missing after updating.**
  `catalog.json` reading/migration (`gocore/internal/api/api.go`, tests in
  `migration_test.go`). The file format is unchanged; compare with what 0.8.22
  wrote.
- **A model/map/clip fails to load, or loads differently than in 0.8.22.**
  Parsers in `gocore/internal/formats`, `materials`, `mapformats`. Error texts
  match Python's, so search the message in `owpreview/*.py` to find the
  matching Go function. To confirm a port bug: run
  `python bench/difftest.py <owio-core.exe> <export folder>` from `gocore/`
  (it needs Python with NumPy and Pillow); it lists every reply that differs
  from the Python core.
- **Paths: "file not found", wrong library root, UNC paths, odd drive-letter
  case, symlinks/junctions.** Python's `pathlib` behaviour is emulated in
  `gocore/internal/pycompat` and `pypath`. These were only tested on Linux;
  Windows path resolution is the most likely place for differences.
- **Mesh loads but looks garbled, or the window freezes on large assets.**
  Binary frames: Go writer `gocore/internal/api/frames.go` +
  `formats/binary.go`, host reader `desktop/frames.cjs`, renderer use in
  `web/viewer.js`. Try `AVOLITION_BINARY_ARRAYS=0`; if that fixes it, the bug
  is in the frame path.
- **Textures black, wrong colours, upside down or missing.** Try
  `AVOLITION_COMPRESSED_TEXTURES=0`. If PNG works, look at
  `gocore/internal/bcenc` (encoder, row flip, mips), the format choice in
  `web/materials.js`, and driver support (non-power-of-two compressed sizes are
  the least tested case). If PNG is also wrong: `gocore/internal/textures`
  (decoders and channel maths; JPEG is the one format not bit-identical to
  0.8.22).
- **Textures load slowly on the user's PC.** The BC7 encode runs on the CPU in
  the core; on a weak CPU PNG may be faster. Measure with the switch above.
- **Map objects missing, flickering, wrong texture or visibility toggles not
  working on maps (characters fine).** Map batching: `web/map-batching.js`
  (`MapBatches.sync`/`cull`), hooked into `web/viewer.js` (`loadAsset`,
  `_renderFrame`, `_clear`). To rule it out, skip the `new MapBatches(...)`
  block in `viewer.js` `loadAsset` (maps then draw one mesh per placement as
  in 0.8.22). Unit test: `node tests/test_map_batching.mjs`.
- **Wrong texture on a mesh after quickly switching assets.** Texture loads
  now run in parallel (`concurrent` in `gocore/internal/api/stdio.go`) and
  replies can arrive out of order; the host matches them by id. Check the
  texture store's register/release handling in `gocore/internal/textures`.
- **Toolchain / DataTool: extraction won't start, Cancel leaves processes
  running, or "access denied".** Windows job objects and suspended process
  start in `gocore/internal/datatool/*_windows.go`. This code was compiled for
  Windows but never run.
- **Cinema 4D export fails or the scene is wrong.** The Go side writes the
  same payload JSON into the same mailbox (`gocore/internal/c4dbridge`,
  `c4dexport`, `mapexport`); the companion plugin is unchanged Python. Compare
  a payload from the Go core with one from a `-PythonCore` build.
- **Baked textures get `_2` names in an existing export folder.** Expected:
  Go's PNG bytes differ from Pillow's even when pixels are identical, so the
  publisher treats them as different files.
- **Build fails.** `build.ps1` now needs Go 1.24 on PATH (it calls
  `go build` in `gocore/`); Python is still needed for
  `tools/package_desktop.py`. `-PythonCore` builds the old backend.

## Not verified here (needs a Windows machine)

Everything was built and run on Linux; the Windows build compiles but was not
run. Before releasing, please:

1. Run `build.ps1` on Windows (Go 1.24 on PATH) and open the app.
2. Load a real exported character, a map and textures; scrub animations.
3. Run a Toolchain/DataTool extraction (job-object process handling was only
   compile-checked for Windows) and Cancel one.
4. Export to Cinema 4D with the companion (only simulated here; the companion
   code itself is unchanged).
5. Check `%LOCALAPPDATA%\Avolition\catalog.json` from 0.8.22 loads unchanged.
6. Open a large real map: check it looks the same as in 0.8.22 (batching),
   that hiding objects in Preview objects works, and compare texture load time
   with `AVOLITION_COMPRESSED_TEXTURES=0` (BC7 encoding speed on real CPUs and
   compressed uploads on real GPUs were not measured).

No real Overwatch assets, Windows, Cinema 4D or GPU were available; all asset
checks used synthetic fixtures.

## Ideas not done

- Raw RGBA texture transfer (~25% faster in Chromium than PNG blobs, but 3×
  the bytes); grayscale PNGs for single-channel maps.
- Removing `app.py`/`owpreview` (except the companion files) once the Go core
  has shipped: drop the oracle tests and difftest with them.
- Port `tools/package_desktop.py` to Go (it is the last Python needed to
  build; it uses PyInstaller only to stamp the icon and version resource).
