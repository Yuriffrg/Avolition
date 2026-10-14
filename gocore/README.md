# Avolition Go core

`owio-core` in Go. It replaces the Python core one method at a time, without
changing the desktop shell or the web UI.

## How it works

```
Electron (desktop/main.cjs)
   │  --stdio JSON lines (same protocol as app.py)
   ▼
Go owio-core ── ported methods: answered in Go
   │
   └─ every other method: forwarded byte for byte
      ▼
   Python core (app.py, or the PyInstaller build in backend/python-core)
```

- Requests are validated exactly like `run_stdio` in `app.py`: the same method
  list, id rules, argument checks and error messages.
- The Python core is started only when a request first needs it. Its replies
  are streamed back in chunks, so large mesh replies are never held in memory
  a second time.
- Go answers ported methods immediately, even while Python is still working on
  a long call. The desktop shell already matches replies by id.
- If the Python core stops on its own, the Go core exits too, so the desktop
  shell shows the same "local importer stopped" error as before.
- On Windows the Python core runs hidden, in a kill-on-close job object. If the
  Go core is terminated, Python and any DataTool process it started end with it.
- `--serve` and `--self-test` are passed straight to the Python core.

### Ported so far (phase 1)

`ping`, `close_app`, `browse_root`, `browse_output`, `browse_entity`,
`browse_map`, `browse_workflow_directory`.

Only stateless methods are ported in phase 1. `catalog.json` stays owned by
Python until every method that writes it has been ported. Otherwise the two
cores would overwrite each other's changes.

## Develop

```sh
cd gocore
go test ./...                       # includes an end-to-end run against app.py + tests/demo-assets
go build -o bin/owio-core ./cmd/owio-core
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
