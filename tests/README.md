# Avolition regression tests

`test_object_hierarchy.mjs` checks source model and skeleton relationships. `test_objects_tree_ui.cjs` exercises the permanent right inspector, keyboard controls, independent visibility, branding and dark select styles with real binary fixtures; set `WHITEHOUND_EXE` to the Avolition executable to run it through native IPC. The retained test environment variable names are compatibility aliases.

Run commands from the project root. Python and Node.js must be available. Browser
tests additionally need Playwright and its Chromium runtime. When Playwright is
not on Node's normal module path, set `PLAYWRIGHT_MODULE` to its package directory;
set `PLAYWRIGHT_BROWSERS_PATH` if its browsers are stored elsewhere.
`WHITEHOUND_TEST_PYTHON` can select the Python executable used by JavaScript tests.
Scratch profiles, generated fixtures, screenshots and reports go under the
sibling `preview-build` folder, independently of the user's profile.

## Unit and backend regressions

```powershell
python -m unittest discover -s tests -p 'test_*.py'
node --test tests/*.mjs
```

The Python suite includes mocked native bridge and Toolchain/process scenarios.
A passing mock test does not establish that extraction or native Cinema 4D
export ran on this machine. `VALIDATION.md` records the checks actually run for
each release and their limitations.

## Current renderer workflows

```powershell
node tests/test_export_queue_ui.cjs
node tests/test_object_search_ui.cjs
node tests/test_manual_objects_ui.cjs
node tests/test_maps_ui.cjs
node tests/test_map_library_ui.cjs
```

`test_export_queue_ui.cjs` uses generated asset files with the real renderer,
parsers, API, and catalog persistence. It restarts the backend to check saved
snapshots and unchecked restoration, verifies that only selected entries run
sequentially, and checks removal and honest disconnected-bridge failures. The
two successful native-export responses are explicitly simulated; it does not
create Cinema 4D documents.

`test_object_search_ui.cjs` checks skeleton coverage, unknown metadata, sorting,
search, duplicate hiding and assignment retention using generated binary assets.
It also supports `WHITEHOUND_EXE` for the same checks through native IPC.

These additional tests require the existing read-only Widowmaker export. Set
`WHITEHOUND_REAL_ASSET_ROOT` to the skin export root containing `Entities` and
`Models` when using a different location:

```powershell
node tests/test_object_search_real.cjs
node tests/test_animation_set_ui.cjs
```

The real-object test checks the observed 190-, 85- and 19-bone rig coverage.
The animation-set test checks the actual separate arms and gun clips, their
shared timeline and an explicit queued setup. Its native C4D success response is
simulated; source animation files stay unchanged.

`test_map_library_ui.cjs` exercises the expandable map/build/variant browser in
an isolated renderer, without requesting map geometry. `test_map_library.mjs`
checks path grouping, natural sorting and search. `test_maps_ui.cjs` uses real
synthetic map binaries, parsers and API calls for Maps workspace integration.

`test_real_map_ui.cjs` requires an existing DataTool map export. By default it
reads `Maps/King's Row (Winter)/6B1/Evening - Hybrid.owmap` beneath the local
Avolition library. Set `AVOLITION_REAL_MAP_ROOT` and `AVOLITION_REAL_MAP_FILE`
to use another appropriate map. It scans/selects through the UI, records material
coverage, geometry/GPU resources, navigation bounds, load timing and idle/orbit
render timing, and checks source inventory/hash integrity. It never extracts
from the game or requests C4D export. Timing is reported without a hardware FPS
threshold. `WHITEHOUND_EXE` selects a packaged build; `AVOLITION_MAP_BASELINE=1`
records the old 0.8.12 behavior without requiring the new map-tree, collision,
texture-budget or navigation assertions. Real-map source mode requires local
loopback access for its test backend.

## Packaged desktop regressions

Point `WHITEHOUND_EXE` at the build under test; tests must use a matching build,
not a previously installed release with different UI/API contracts.

```powershell
$env:WHITEHOUND_EXE = 'C:\path\to\Avolition\Avolition.exe'
node tests/test_desktop.cjs
node tests/test_animation_set_desktop.cjs
node tests/test_toolchain_desktop.cjs
node tests/test_object_search_ui.cjs
```

Desktop tests launch an isolated hidden QA window with a temporary profile.
Their harness uses `--no-sandbox` for automation; the production window's
renderer preferences are separately checked. Native file dialogs or folder
opening may be intercepted as documented in each file. Animation-set tests use
an isolated disconnected Cinema bridge and do not claim native export success.
Toolchain desktop tests exercise paths, tabs, settings and native IPC without
claiming a game extraction.

## Historical UI harnesses

Do not run every `.cjs` file as though they all describe the current product.
The following harnesses intentionally preserve assertions for interfaces that
were removed; they are not current release regression commands:

| Historical files | Original interface | Current replacements |
| --- | --- | --- |
| `test_browser.cjs`, `test_c4d_ui.cjs`, `test_close.cjs` | v0.8.9-and-earlier nickname-triggered exports, raw-clip checkboxes and old output controls | `test_export_queue_ui.cjs`, `test_desktop.cjs` |
| `test_animation_analysis_ui.cjs`, `test_analysis_desktop.cjs` | v0.8.7-era Analyze Library and Animation evidence UI | `test_object_search_ui.cjs`, `test_object_search_real.cjs` |

Historical assertions remain intact and will fail against incompatible current
markup. No passing result is manufactured by skipping or disabling their tests.
Use the corresponding old source/build if reproducing those historical checks.
