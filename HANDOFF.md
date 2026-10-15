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

No real Overwatch assets, Windows, Cinema 4D or GPU were available; all asset
checks used synthetic fixtures.

## Ideas not done

- Raw RGBA texture transfer (~25% faster in Chromium than PNG blobs, but 3×
  the bytes); grayscale PNGs for single-channel maps.
- Removing `app.py`/`owpreview` (except the companion files) once the Go core
  has shipped: drop the oracle tests and difftest with them.
- Port `tools/package_desktop.py` to Go (it is the last Python needed to
  build; it uses PyInstaller only to stamp the icon and version resource).
