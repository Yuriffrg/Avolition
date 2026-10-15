# Handoff: 0.8.23, the Go backend

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
  but compress differently, so their bytes differ. Re-exporting with Go into a
  folder that already holds Python-baked textures of the same name produces
  `_2` names instead of reusing them.
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
