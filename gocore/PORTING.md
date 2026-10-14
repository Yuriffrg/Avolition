# Porting rules (Python → Go)

The Python modules in `owpreview/` and `app.py` are the specification. The Go
core must behave identically from the desktop shell's point of view: same API
methods, same JSON reply shapes and keys, same error messages, same files
written. The Cinema 4D plugin (`c4d-plugin/`, plus `owpreview/c4d_bridge.py`'s
`BridgeService`, `c4d_scene.py`, `c4d_octane.py`, which are bundled into it)
stays Python. Helpers that the backend imports from those files are ported.

## Layout

One Go package per Python module under `gocore/internal/`, named after it
without underscores:

| Python | Go package |
|---|---|
| `formats.py` (+ `_Reader`) | `formats` |
| `bone_counts.py` | `bonecounts` |
| `bone_names.py` | `bonenames` |
| `clip_bindings.py` | `clipbindings` |
| `duplicates.py` | `duplicates` |
| `library_scan.py` | `libraryscan` |
| `object_names.py` | `objectnames` |
| `textures.py` | `textures` |
| `materials.py` | `materials` |
| `export_textures.py` | `exporttextures` |
| `map_formats.py` | `mapformats` |
| `map_export.py` | `mapexport` |
| `map_jobs.py` | `mapjobs` |
| `export_pose.py` | `exportpose` |
| `c4d_export.py` | `c4dexport` |
| `export_bundle.py` | `exportbundle` |
| `fbx.py` | `fbx` |
| `c4d_bridge.py` (`BridgeClient`, helpers) | `c4dbridge` |
| `datatool.py` | `datatool` |
| `toolchain_runtime.py` | `toolchain` |
| `workflow_paths.py` | `workflowpaths` |
| `animation_evidence_formats.py` | `evidence` |
| `animation_analysis.py` | `analysis` |
| `app.py` (`BrowserAPI`) | `api` |

## Rules

1. **Behaviour parity first.** Port logic faithfully, including validation
   order, limits, warnings text and error messages (the UI shows them and the
   Python tests assert them). Where Python raises, return an `error` whose
   `Error()` is exactly `str(exception)`. Keep comments that explain *why*.
2. **Types.** Use typed structs with `json:"..."` tags matching the Python dict
   keys exactly (camelCase as in Python). Keep Python's key order in struct
   field order. Use `omitempty` only where Python omits the key. For dynamic
   keys whose order is visible, use `internal/ordered.Map`.
3. **Numbers.** Data read as float32 from files stays `float32` in Go (smaller
   JSON, same value once the UI puts it in a Float32Array). Math that Python
   does in float64 (or numpy float64) stays float64. Never emit NaN/Inf.
4. **Performance.** Read files with one `os.ReadFile` and decode with
   `encoding/binary.LittleEndian` on slices (no per-value reflection).
   Preallocate slices. Parallelise independent file work (scans, texture
   decoding, hashing) with a bounded worker pool (`runtime.NumCPU()`), keeping
   output order deterministic and identical to Python's.
5. **Tests.** Port the module's Python tests from `tests/test_<module>.py` to
   Go (`*_test.go`), and add oracle tests: build the same input, run the
   Python function through `internal/oracle`, and compare with
   `internal/jsoncmp` (`jsoncmp.Default` tolerates float32 rounding). Fixture
   generators in the Python tests (`struct.pack` helpers) should be ported to
   Go test helpers; `tests/demo-assets` and `tests/material-assets` are real
   binary fixtures to use as well.
6. **Dependencies.** Standard library, `golang.org/x/sys`, `golang.org/x/image`.
   Anything else needs a strong reason, stated in the package doc comment.
   Go 1.24; run Go with `GOTOOLCHAIN=local`. Never run `go mod tidy` in a shared
   checkout.
7. **Platform.** The product ships for Windows x64. Code must build with
   `GOOS=windows GOARCH=amd64` and on Linux (for tests). Use build-tagged
   files for Windows-only syscalls, mirroring how the Python uses `ctypes`.
8. **Concurrency.** Python classes guarded by `threading.Lock` become Go types
   with `sync.Mutex`; background threads become goroutines. Tests must pass
   with `-race`.
