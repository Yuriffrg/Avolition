# Notes for coding agents

Read this first, then `HANDOFF.md` (what changed in 0.8.23 and why) and
`gocore/README.md` (how the Go core works).

## Architecture (since 0.8.23)

```
desktop/main.cjs (Electron)  ── spawns ──▶  owio-core --stdio   (Go, gocore/)
   │  JSON lines + binary frames            internal/api = the API (was app.py BrowserAPI)
   ▼
web/*.js (three.js renderer)   calls window.pywebview.api.<method>(...)
c4d-plugin/ + owpreview/c4d_bridge.py, c4d_scene.py, c4d_octane.py
   = Cinema 4D companion, still Python, runs inside Cinema 4D (file mailbox)
```

- **The backend is Go.** `gocore/cmd/owio-core` is the shipped `owio-core.exe`;
  it never starts Python. Each Python module in `owpreview/` has a Go package of
  the same name without underscores under `gocore/internal/` (table in
  `gocore/PORTING.md`); `app.py` is `gocore/internal/api`.
- **`app.py` and `owpreview/*.py` are kept as the reference implementation**,
  not as runtime code. Go oracle tests call them (`gocore/internal/oracle`) and
  `gocore/bench/difftest.py` replays identical requests through both cores.
  Exception: `c4d_bridge.py` (`BridgeService`), `c4d_scene.py` and
  `c4d_octane.py` are bundled into the Cinema 4D plugin and are live code.
- **Transport:** requests are one JSON object per line. When the host sends
  `"binary": true` (desktop/main.cjs does; the `--serve` bridge sends header
  `X-Avolition-Binary: 1`), large replies (`load_entity`, `preview_entity`,
  `load_map`, `load_animation`, `load_texture`) come back as a JSON line with
  `{"$blob": N}` placeholders followed by raw little-endian blobs; the host
  turns them into typed arrays (`desktop/frames.cjs`). Every other reply is a
  plain JSON line. The Python core ignores `binary` and answers in JSON, and
  the renderer accepts both arrays and typed arrays.

## Making a change

1. **Behaviour changes go in Go** (`gocore/internal/...`). If you change
   behaviour that `app.py`/`owpreview` also implement, either update the Python
   reference the same way (so oracle/difftest stay meaningful) or update the Go
   test expectations and note the intentional divergence in `HANDOFF.md`.
2. **New API method:** add it to `protocol.Methods`, implement it in
   `internal/api`, and add it to `API_METHODS` in `app.py` too (a test checks
   that the two lists match; remove that test if Python is ever deleted).
3. **Error messages and JSON keys are UI contract.** The renderer shows error
   strings and reads reply keys; keep them stable.
4. **UI/renderer changes** go in `web/`; keep both plain arrays and typed arrays
   working where reply data is consumed.
5. **Cinema 4D plugin changes** stay in Python (`c4d-plugin/`, `owpreview/c4d_*.py`);
   the mailbox protocol is shared with `gocore/internal/c4dbridge`.
6. Bump the version in all places listed in `HANDOFF.md` → "Version".

## Checks to run

```sh
cd gocore
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -short -race ./...          # needs python3 + numpy + Pillow for oracle tests (they skip otherwise)
GOOS=windows GOARCH=amd64 go build ./...
go build -trimpath -ldflags "-s -w" -o bin/owio-core ./cmd/owio-core
python3 bench/difftest.py bin/owio-core                # Python vs Go, every reply + catalog.json
cd ..
node --test tests/*.mjs desktop/frames.test.cjs
python -m unittest discover -s tests -p "test_*.py"    # the Python reference still passes
# Renderer tests against the Go core (they start "<python> app.py --serve"; the shim runs Go instead):
PLAYWRIGHT_MODULE=$(npm root -g)/playwright WHITEHOUND_TEST_PYTHON=$PWD/gocore/bench/python-shim node tests/test_texture_ui.cjs
```

Renderer tests that pass on both cores: `test_export_queue_ui`, `test_maps_ui`,
`test_object_search_ui`, `test_object_ui`, `test_objects_tree_ui`,
`test_texture_ui`, `test_map_library_ui`, `test_map_export_ui`,
`test_model_browser_ui`, `test_object_rename_ui`, `test_objects_lazy_ui`,
`test_toolchain_maps_ui`, `test_toolchain_ui`. Tests write scratch files to
`../preview-build` (create it first).

## Build

`build.ps1` (Windows) builds the Go core by default and packages it with
Electron; Go must be on PATH. `-PythonCore` builds the legacy PyInstaller
backend instead.

## Conventions

- Go 1.24, `GOTOOLCHAIN=local`; dependencies: stdlib, `golang.org/x/sys`,
  `golang.org/x/image` only. Never run `go mod tidy` blindly in shared work.
- Windows-only code lives in `_windows.go` files with non-Windows fallbacks so
  tests run on Linux.
- Keep output deterministic: parallel work (worker pools sized by
  `runtime.NumCPU()`) must produce Python's ordering.
- Commit messages: imperative summary line, body explaining why.
