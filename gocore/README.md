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

Results for 0.8.23 are in `../HANDOFF.md`.

## Build the portable app

```powershell
.\build.ps1 -ElectronArchive ...\electron-v44.4.3-win32-x64.zip
```

Go is the default backend: `resources/backend/owio-core.exe`, with no Python
runtime shipped. `-PythonCore` builds the legacy PyInstaller backend instead.
