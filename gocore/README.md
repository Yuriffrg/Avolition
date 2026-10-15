# Avolition Go core

`owio-core` in Go: the complete Avolition backend since 0.8.23. It replaced the
Python core (`app.py` + `owpreview/`) behind the same `--stdio` protocol, so the
desktop shell and renderer keep working unchanged with either core.

## How it works

`internal/api` is the Go port of `app.py`'s `BrowserAPI`: every desktop API
method, `catalog.json` (unknown keys and key order preserved, written
atomically in `json.dumps(indent=2, ensure_ascii=False)` form), the export
list, object names, entity previews and texture ownership, plus the `--stdio`
transport, the developer `--serve` host and `--self-test`.

```
Electron (desktop/main.cjs)
   │  --stdio JSON lines (same protocol as app.py)
   ▼
Go owio-core ── internal/api (BrowserAPI) ── formats, materials, textures, …
```

- Requests are validated exactly like `run_stdio` in `app.py` (same method
  list, id rules, argument-count `TypeError` texts and `json.loads` error
  messages) and answered serially, in order. Long-running work (Toolchain,
  DataTool, analysis, map exports) runs in background goroutines polled by the
  status methods, as in Python.
- Mesh and clip replies are encoded with `formats.AppendAssetJSON` /
  `AppendClipJSON` straight into the output buffer.
- Bulk arrays can travel as binary (below).

## Binary arrays

Large replies are mostly numbers: a 640k-vertex character is 67 MB of JSON
that Electron must parse and the renderer must copy into typed arrays. A host
that can decode binary frames asks for them per request, so every pairing of
old and new host and core keeps working:

- stdio: the request object carries `"binary": true`. `app.py` ignores unknown
  request keys and keeps answering in JSON; an old host never sends the key.
- `--serve`: the injected bridge sends `X-Avolition-Binary: 1`. The Python
  server injects its own JSON-only bridge.

Only replies with bulk arrays become frames (`load_entity`, `preview_entity`,
`load_map`, `load_animation`, `load_texture`); everything else, errors
included, stays a JSON line. A frame is the usual reply object plus a `blobs`
list of `[type, byteLength]`, as one JSON line padded with spaces to a multiple
of 8 bytes, followed by each blob's little-endian bytes zero-padded to a
multiple of 8 (over HTTP the same without `id`, with Content-Type
`application/x-avolition-frame`). In the result, `{"$blob":N}` stands for blob
N as a typed array (`f32`, `f64`, `u32`, `u16`, `u8`):

| Field | JSON | Binary |
|---|---|---|
| positions, normals, uvs, uvs2 | float32 numbers | `Float32Array` |
| indices | integers | `Uint16Array` when every index fits, else `Uint32Array` |
| skinIndices | integers | `Uint16Array` |
| skinWeights | float64 numbers | `Float32Array` (the precision the renderer keeps) |
| clip track frames / values | integers / float64 numbers | `Uint32Array` / `Float64Array` |
| load_texture `dataUri` | base64 PNG data URI | `png`: the PNG bytes as `Uint8Array` |

`desktop/frames.cjs` decodes frames from the core's stdout and gives every
array its own exact-size `ArrayBuffer`, so Electron's IPC and context bridge
copy only that array; the `--serve` bridge creates zero-copy views of the
response buffer. Non-finite numbers fail a binary reply exactly like a JSON
one. Measure with `bench/transport_stdio.cjs` (core and host parsing),
`bench/transport_electron/main.cjs` (through Electron IPC and the context
bridge) and `bench/transport_renderer.cjs` (`--serve` in headless Chromium, to
the first rendered frame).

## Compressed textures

A renderer whose WebGL context can sample GPU block formats gets preview
textures as ready-to-upload mip chains instead of PNGs: the browser decodes
nothing, uploads 4–8× fewer bytes and keeps 4–8× less video memory (no
GPU-generated mips either). Negotiation, per host and only for binary
replies, so every old/new host and core pairing keeps working:

- The renderer (`web/materials.js`) checks its WebGL extensions:
  `EXT_texture_compression_bptc` gives `bc7`; `WEBGL_compressed_texture_s3tc`
  together with `WEBGL_compressed_texture_s3tc_srgb` gives `bc3` and `bc1`.
  It calls `window.pywebview.setTextureFormats([...])` once (before its first
  texture request) when that function exists. The `load_texture(texture_id,
  max_edge)` signature does not change.
- Electron: `desktop/preload.cjs` forwards the list over IPC
  (`whitehound:texture-formats`); `desktop/main.cjs` keeps it and adds
  `"textureFormats": [...]` to each binary `load_texture` request.
  `AVOLITION_COMPRESSED_TEXTURES=0` (or `AVOLITION_BINARY_ARRAYS=0`) keeps
  PNGs.
- `--serve`: the injected bridge defines `setTextureFormats` and sends header
  `X-Avolition-Texture-Formats: bc7,bc3,bc1` with binary `load_texture`
  requests. The Python core's test server injects a bridge without the
  function, so the renderer keeps asking for PNGs there.
- The core honours the list only for binary replies (`"binary": true` /
  `X-Avolition-Binary: 1`); JSON replies, and requests without the key, are
  byte-identical to before. `app.py` ignores the unknown key.

The core (`textures.Store.LoadCompressed`) picks BC7 when accepted, else BC1
for previews whose alpha is all 255 and BC3 when alpha matters; with none of
those, or a preview side that is not a multiple of 4, it answers with the
usual PNG. The preview (same decode, channel processing and Lanczos resize as
the PNG) is flipped bottom to top, because WebGL cannot flip compressed
uploads (the renderer sets `flipY = false` on them, so UVs stay as before),
then halved with a 2×2 box filter down to 1×1 like `generateMipmap` (in
linear light for sRGB colour maps: `rgb`/`rgba` channels of `srgb` sources;
the chain stops early at a level whose side is not 1, 2 or a multiple of 4).
Each level is encoded by `internal/bcenc`, and results are cached per
(id, max_edge, accepted formats) like the PNGs. The binary reply is

```
{"format":"bc7","mips":[{"width":2048,"height":2048,"data":{"$blob":0}},…,{"width":1,"height":1,"data":{"$blob":11}}],"width":2048,"height":2048[,"warning":…]}
```

with each level's blocks as a `Uint8Array` (blob type `u8`). The renderer
builds a `THREE.CompressedTexture` with these mipmaps; the texture budget
counts level-0 pixels as for PNGs.

`internal/bcenc` is a fast encoder, not an optimal one: BC1 uses principal
axis endpoints with two least-squares refinements (stb_dxt's approach); BC3
adds an 8- or 6-value alpha block; BC7 uses mode 6 for every block, mode 5
for blocks whose alpha varies and mode 1 (two subsets) for opaque blocks with
an edge. Previews without alpha use `EncodeOpaque`, which lets BC7 decode
alpha as 254 where that keeps black and white exact. Blocks are encoded in
parallel rows. `go test ./internal/textures -run CompressedSpeed -v`
(`AVOLITION_BENCH_LIBRARY` adds library textures) reports encode time and
PSNR; `bench/texture_renderer.cjs` measures load-to-first-frame in Chromium.

Every module is ported: `owio-core` never starts Python. `app.py` and
`owpreview/` remain in the repository as the reference implementation used by
the oracle tests (`internal/oracle`) and `bench/difftest.py`, which runs the
same request sequences through both cores and compares every reply and the
final `catalog.json`. `bench/python-shim` lets the renderer tests
(`tests/*_ui.cjs`, which start `<python> app.py --serve`) run against the Go
core.

## Packages

One package per Python module (`PORTING.md` has the table), plus shared helpers:

| Package | Role |
|---|---|
| `api` | `BrowserAPI`: every API method, `catalog.json`, export list, stdio loop, binary frames, `--serve`, `--self-test` |
| `formats`, `materials`, `mapformats` | model/entity/clip/material/map readers and fast JSON / binary encoders |
| `textures`, `exporttextures` | image decoders (PNG, DDS BC1–7, TGA, TIFF, JPEG, BMP, WebP), Pillow-exact channel maths and Lanczos resize, PNG encoder, baking |
| `bcenc` | BC1/BC3/BC7 block encoder and mip chains for compressed preview textures (Go only, no Python counterpart) |
| `c4dexport`, `exportpose`, `fbx`, `exportbundle`, `mapexport`, `mapjobs`, `c4dbridge` | export payloads, pose maths, FBX writer, publishing, the Cinema 4D mailbox client |
| `bonecounts`, `clipbindings`, `duplicates`, `libraryscan`, `objectnames`, `bonenames`, `evidence`, `analysis` | library scan helpers and animation analysis |
| `datatool`, `toolchain`, `workflowpaths` | DataTool jobs (Windows job objects), Toolchain download/install, folders |
| `pycompat`, `pyjson`, `pypath`, `ordered` | Python-compatible strings, `json.dumps`/`json.loads`, `pathlib` and ordered dicts |
| `oracle`, `jsoncmp`, `formats/formatstest`, `mapformats/maptest`, `exporttest` | test helpers: call the Python reference, compare JSON, build fixtures |
| `protocol` | request validation identical to `run_stdio` |

`cmd/owio-core` is the backend executable; `cmd/analyze-library` replaces
`tools/analyze_library.py`.

## Develop

```sh
cd gocore
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -short -race ./...   # unit, oracle (need python3 + numpy + Pillow; skip otherwise) and end-to-end tests
GOOS=windows GOARCH=amd64 go build ./...
go build -trimpath -ldflags "-s -w" -o bin/owio-core ./cmd/owio-core
python3 bench/difftest.py $PWD/bin/owio-core   # Python vs Go: every reply and catalog.json
# Renderer tests against the Go core (--serve); the shim runs the Go binary when a test starts app.py:
PLAYWRIGHT_MODULE=$(npm root -g)/playwright WHITEHOUND_TEST_PYTHON=$PWD/bench/python-shim node ../tests/test_texture_ui.cjs
```

Run the desktop shell from source against the Go core:

```powershell
cd gocore; go build -o bin\owio-core.exe .\cmd\owio-core
$env:AVOLITION_BACKEND_PATH = "$PWD\bin\owio-core.exe"
cd ..\desktop; npm install; npm start
```

## Benchmarks

`bench/make_library.py OUT` generates a 545 MB synthetic library (no game data):
24 skins of 35k-vertex characters with 30 clips each, one 640k-vertex
character with 2048² textures, and a 300-prop, 3,600-placement map. Set
`AVOLITION_BENCH_LIBRARY=OUT` so `difftest.py` and the gated Go tests use it.

- `bench/bench.py [--binary] [--label L] [--json F] LIBRARY -- CORE...`: time,
  reply size and peak memory (core plus children) per API step over stdio.
  Python core: `-- python3 -u app.py`; Go: `-- gocore/bin/owio-core`.
- `bench/transport_stdio.cjs`, `bench/transport_electron/main.cjs`,
  `bench/transport_renderer.cjs`: JSON versus binary frames through the host,
  Electron IPC and the renderer.
- `bench/texture_renderer.cjs CORE LIBRARY [REPS]`: PNG versus compressed
  preview textures, load to first rendered frame in headless Chromium.

Results for 0.8.23 are in `../HANDOFF.md`.

## Build the portable app

```powershell
.\build.ps1 -ElectronArchive ...\electron-v44.4.3-win32-x64.zip
```

Go is the default backend: `resources/backend/owio-core.exe`, with no Python
runtime shipped. `-PythonCore` builds the legacy PyInstaller backend instead.
