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

### Migration phase (until every module is ported)

`api.Missing(api.Default())` lists modules that are not ported yet. While it is
not empty, `owio-core` keeps the Python core as the owner of all state and
answers only `api.Stateless` methods in Go (ping, dialogs, `load_animation`,
`read_clip_bindings`, `c4d_status`); everything else is forwarded to Python.
Splitting stateful methods between two processes would let them overwrite each
other's `catalog.json` and lose loaded-asset/texture registrations.

`AVOLITION_GO_API=1` forces the Go API for every mode (methods whose modules
are missing return an error). `bench/python-shim` uses it so the renderer
tests can run against the Go core, and `bench/difftest.py` compares the two
cores reply by reply.

When `api.Missing` is empty the Python core is no longer started at all.

The Python core (hybrid phase only) is found in this order:

1. `AVOLITION_PYTHON_CORE`: a frozen executable or a `.py` file.
2. `python-core/owio-core(.exe)` beside the Go binary (the packaged layout).
3. `app.py` in the binary's folder or up to three parent folders (a source
   checkout).

A `.py` core runs with `AVOLITION_PYTHON` (default `python` on Windows,
`python3` elsewhere). On Windows it runs hidden, in a kill-on-close job object.

## Develop

```sh
cd gocore
go test ./...                       # includes an end-to-end run against app.py + tests/demo-assets
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

This places the Go core at `resources/backend/owio-core.exe` and the frozen
Python core at `resources/backend/python-core/`. Without `-GoCore`, the build
is unchanged.

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
