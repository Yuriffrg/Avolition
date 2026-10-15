# Avolition Go core

`owio-core` in Go. It replaces the Python core one method at a time, without
changing the desktop shell or the web UI.

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

## Develop

```sh
cd gocore
go test ./...                       # includes end-to-end stdio/--serve/--self-test runs of the binary
go build -o bin/owio-core ./cmd/owio-core
python3 bench/difftest.py bin/owio-core     # Python vs Go, every reply and catalog.json
# Renderer tests against the Go core (--serve):
PLAYWRIGHT_MODULE=$(npm root -g)/playwright WHITEHOUND_TEST_PYTHON=$PWD/bench/python-shim node ../tests/test_texture_ui.cjs
```

The Python core is found in this order:

1. `AVOLITION_PYTHON_CORE`: a frozen executable or a `.py` file.
2. `python-core/owio-core(.exe)` beside the Go binary (the packaged layout).
3. `app.py` in the binary's folder or up to three parent folders (a source
   checkout).

A `.py` core runs with `AVOLITION_PYTHON` (default `python` on Windows,
`python3` elsewhere).

To try it in the desktop shell from source:

```powershell
cd gocore; go build -o bin\owio-core.exe .\cmd\owio-core
$env:AVOLITION_BACKEND_PATH = "$PWD\bin\owio-core.exe"   # app.py is found two folders up
cd ..\desktop; npm start
```

## Build the portable app with the Go core

```powershell
.\build.ps1 -ElectronArchive ...\electron-v44.4.3-win32-x64.zip -GoCore
```

This places the Go core at `resources/backend/owio-core.exe`; no frozen Python
core is built or shipped (Python is used only by the packaging script).
Without `-GoCore`, the build is unchanged.

## Phase 1 measurements

Measured on Linux in the development container with `tests/demo-assets`, over
the real stdio protocol (best of three runs):

| | Python only | Go core → Python |
|---|---|---|
| First reply after launch (`ping`) | ~115 ms | ~2 ms |
| `bootstrap` ready | ~120 ms | ~120 ms (Python still starts) |
| `ping` round trip | ~80 µs | ~70 µs |
| `load_entity` (forwarded) | ~4.9 ms | ~5.1 ms (+ one pipe hop) |
| Memory | 32.5 MB | 6.5 MB Go + 32.5 MB Python |

Phase 1 is plumbing: until real work moves to Go, it adds ~6.5 MB and one pipe
hop. The gains come as the parsers, texture decoding and scanning are ported,
and Python ends up no longer being started at all.
