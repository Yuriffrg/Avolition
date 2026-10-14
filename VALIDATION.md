# Avolition 0.8.22 validation

## Export Manager placement (2026-10-14)

The former Animation setup sliders button is now a full-width blue Export Manager button in the Animations inspector, after Clip details and before Preview objects. It opens the same export queue and settings; the dialog heading and setup status message use the new name.

- The existing export-queue integration passed with the real backend and persistent catalog: saved setups, output/settings, initially unchecked selections, sequential selected exports, restart persistence, failure recovery and removal remain intact. Native successes were simulated and no Cinema 4D jobs were published. Report: `work/preview-build/export-queue-ui-2V7T8E/`.
- Packaged desktop inspection confirmed the button is below the clip metadata and above Preview objects, uses the application primary blue, and opens Export Manager. Escape closes the dialog. Normal and minimum-window captures were visually checked with no renderer errors: `work/preview-build/export-manager-layout-JxhrIK/`.

## Avolition 0.8.21 validation (historical)

## Model browser sorting (2026-10-14)

Browse models now places a sort dropdown beside the entity/model type filter. Gameplay first is the default; most/fewest joints and largest/smallest source files sort numerically within each hero and skin. Unknown values come last in both directions and equal values use the existing Gameplay/name order. The choice survives closing/reopening the browser during the session. File-size metadata comes from the source file's stat, with no dependency traversal or mesh decoding; its tooltip explains that referenced assets are excluded.

- **473 Python tests passed**, including actual entity/model byte counts, stat failures retaining rows and bone counts, and scans without geometry decoding.
- **68 model-browser UI checks passed**, including 2 versus 10, zero and unknown values, ties, all five dropdown modes, numeric sorting across file types, preserved selection/filters/expanded groups and no extra preview/load requests when sorting.
- Source and packaged Windows integration passed with real synthetic binary assets, the production parser, material decoder and renderer. File-size order matches filesystem stats; sorting and browsing preserve the active scene, animation, camera and materials. Reports: `work/preview-build/model-browser-integration-T2DeYv/` and `work/preview-build/model-browser-integration-7oUYqE/`.
- Visual inspection passed at 1500 × 940 and the 1060 × 700 minimum. The dropdown, count, preview and Load controls fit. Minimum-size capture: `work/preview-build/model-browser-integration-T2DeYv/model-browser-sort-1060x700.png`.
- The prior desktop Heroes folder is no longer available, so this release's end-to-end checks used synthetic assets. Historical real-asset count and preview checks below still document 0.8.20; they were not rerun for 0.8.21. No native exports were requested.

## Avolition 0.8.20 validation (historical)

## Bone counts and toolbar cleanup (2026-10-14)

Browse models displays each scanned entity/model's bone count beside its name. Entity counts include repeated attachment instances and model-free wrappers contribute zero; standalone models show their own skeleton size. Missing, ambiguous, cyclic or unreadable references produce an unavailable count with a per-row explanation, never a misleading partial total. Scanning reads model headers and caches entity references without decoding geometry, materials or textures. The Open entity or model file toolbar button and its frontend event handlers were removed.

- **472 Python tests passed**, including nine count tests covering loaded-preview parity, shared/repeated attachments, missing/corrupt references, cycles, hierarchy limits, zero-bone wrappers, bounded header reads, caching and rescans.
- The model-browser renderer's **46 checks passed**, including plural/singular/zero/unavailable counts before preview completion, accessibility labels, source-path and error tooltips, search, and existing preview lifecycle behavior.
- Source and native desktop integration verify the button is absent and scanned counts are visible before creating the browser viewport. Existing Load, failed-load recovery, texture rendering and animation-state isolation checks pass.
- Real Heroes scan: **173 count-bearing entries, 675 clips, no unavailable counts**, with 107 model-header reads and 66 entity reads. The isolated count pass took **0.150 seconds**; the complete scan took **2.59 seconds** on the test machine. Totals matched full parsing: Widowmaker Gameplay1P **105**, Gameplay3P **210**, Widow's Kiss **20**; Mercy Gameplay1P **138**, Gameplay3P **415**. Report: `work/preview-build/real-bone-counts-xjqpc7tz/report.json`.
- Real-source browser checks and visual inspection passed at 1500 × 940 and 1060 × 700. Names, bone counts and type badges remain readable; the preview and Load controls fit. All 4,350 source file stats and sampled entity hashes were unchanged. Captures/report: `work/preview-build/real-model-browser-Tbe24b/`.

## Avolition 0.8.19 validation (historical)

## Model browser (2026-10-14)

Browse models adds a separate, lazy 3D preview alongside a searchable hero/skin tree. Actual Gameplay1P and Gameplay3P entity filenames sort first in both the quick picker and browser. Asset scanning stays metadata-only; only the selected candidate is parsed. Rapid selection is debounced and coalesced into one in-flight request, and stale results cannot replace a newer selection. Preview texture decoding is limited to 512 pixels per edge. Explicit Load into Animations uses the ordinary full-quality loader.

The browser has a separate texture owner and never registers editable naming assets or persists catalog changes. Replacing/closing its preview preserves active Animation and Map texture registrations and caches. A backend generation guard prevents an in-flight preview from re-registering textures after release. Modal ownership isolates camera shortcuts and rendering; closing releases preview meshes and materials, while retaining one reusable GPU context.

- **463 Python tests passed**, including preview/name/texture isolation, close/replacement races and priority ordering.
- **38 focused browser checks passed** for hero/skin grouping, filters, keyboard previews, coalesced requests, close/reopen races, malformed assets, material errors and WebGL fallback. These checks use deterministic backend and viewport fixtures.
- Viewer lifecycle checks passed **41 unit and 18 GPU assertions**, including three actual canvases, modal shortcut ownership, inactive render suppression, delayed texture cancellation, reopen and idempotent disposal. Existing map (84), navigation (11) and material (98) checks passed.
- Full-app integration passed with real synthetic binary assets, the actual parser, material decoder and renderer, in both source/browser mode and the packaged native desktop using preload/stdio APIs. Browsing, failed preview, framing and Cancel preserve the current entity, assignment, frame, camera and material UUIDs. Only explicit Load replaces the scene. A failed explicit Load stays in the browser and can recover by choosing another item. Isolated test profiles were used; no native export was requested.
- Real-source QA scanned **4,350 files** from the user's Mercy/Widowmaker library and verified Gameplay1P/3P priority and hero/skin context. Widowmaker Gameplay3P rendered **7 objects, 9 meshes, 210 bones and 9 textured meshes**; the separate Widow's Kiss entity rendered **4 objects, 3 meshes, 20 bones and 3 textured meshes**. Mercy Gameplay1P and Gameplay3P also rendered. Existing source material approximation notes remain available. Captures at 1500 × 940 and 1060 × 700 were visually inspected; preview and Load controls fit. Source inventory and sampled entity hashes were unchanged. Report: `work/preview-build/real-model-browser-ZkOGQW/report.json`.

## Avolition 0.8.18 validation (historical)

## Full-map export and framing (2026-10-14)

The reported error came from the desktop's 180-second RPC timeout. Its serial backend continued processing while the renderer discarded the eventual response and allowed another attempt. Map export now starts a tracked background job; short status requests report loading, material baking counts, native processing and publication. Both synchronous and asynchronous entry points share a single-job guard. Names, paths and export options are captured before work starts, and transient status failures retain the running job. Cinema's native map processing budget is 30 minutes, with a separate 150-second pickup wait. Compact JSON retains exact numeric values while avoiding unnecessary transfer whitespace.

Bridge 0.7.5 reuses identical complete material descriptors, preserving every object's binding, and verifies the unique saved material count. Static maps evaluate once before save and once after reload instead of five evaluations. Native geometry remains editable independent mesh objects; all scene verification remains enabled.

The Maps-only Include animations checkbox was removed and map extraction explicitly disables animation extraction. Pressing Frame now clears pending damped control movement before installing the framed view. Actual camera roll was not reproduced; real-control checks retain world Y up and a horizontal camera-right vector during free orbit, including underside views.

- Background API checks cover responsive serial transport/settings during a held export, duplicate prevention, captured names/options, failure recovery, result ownership and nonjoining shutdown. Renderer checks cover the corresponding running/completed/failed flows and transient status failures (**38 checks**).
- Camera verification passed **84 map checks, 676 viewer checks and 11 navigation checks**, including repeated updates after framing and loading another map mid-motion.
- Both Toolchain renderer workflows passed after the Maps-only option removal. These checks use mocked extraction APIs.
- The complete Esperança / D53 / Night - Push map took **272.36 seconds** to parse, prepare and bake, exceeding the former desktop deadline before native processing. Its transfer is **260,250,038 bytes** with compact JSON, down from 281,078,555 bytes, within the unchanged 256 MiB limit. It contains **3,193 textures**, **18,761 placed meshes** and **6,080,542 polygons**. **18,056 material bindings** resolve to **1,598 distinct descriptors**. Preparation report: `work/preview-build/avolition-v0818-full-map/prepared.json`.
- The user closed Cinema 4D before bridge 0.7.5 was installed. The previous 0.7.4 folder was moved outside plugin search paths into `work/preview-build/bridge-backups/Avolition-C4D-Bridge-0.7.4-before-0.7.5`. All seven installed files matched the prepared companion. A fresh live heartbeat confirmed bridge 0.7.5, Cinema 4D 2026 and Octane after reopening.
- **455 Python tests passed**, including native wait beyond the previous deadline, bounded pickup failure, background transport responsiveness, material reuse and saved-scene verification.
- **The complete real Esperança map saved and reopened successfully through live Cinema 4D 2026 / Octane and bridge 0.7.5.** Native processing took **46.15 seconds** after submission. The **1,050,014,866-byte** scene verified **18,761 meshes, 8,906 joints, 1,842 skins, 1,598 Octane materials and zero animation tracks**, with Y up and centimetre units. Report: `work/preview-build/avolition-v0818-full-map/verified-native-report.json`; scene and textures: `work/preview-build/avolition-v0818-full-map/verified-export/`. The completed test job was archived outside the live mailbox. No source assets or open editor documents were modified.
- Live QA used the production parser, normal preparation and baker, then copied a hash-verified complete payload and texture directory to the authorized bridge mailbox with the request last. The sandbox's external atomic-rename restriction prevents running the production client file-publish step directly in that mailbox; client handoff/publication is covered by automated transaction tests. Source omissions and material approximation notes remain available in Map notes.
- The built **0.8.18 Windows app passed native Maps integration** with real renderer/backend IPC. It exercised the new start/status APIs and recovered from a missing bridge in an isolated profile, publishing no files. Shared-library separation, naming, viewport geometry, paths, restart persistence and close-save recovery also passed without renderer errors. Report: `work/preview-build/maps-ui-rocalg/report.json`.

## Avolition 0.8.17 validation (historical)

## Extraction option cleanup (2026-10-14)

Both Toolchain extraction tabs omit Include sounds and explicitly submit `includeSound: false`. All levels of detail retains its unchecked default and displays "Unchecked exports the highest available detail" beneath the checkbox, also connected with `aria-describedby`.

- Existing Toolchain renderer and Maps extraction-tab integration checks passed with mocked APIs. The Maps expectation was updated for sound extraction being off; tab switching, independent choices, captured selections and shared export lifecycle still pass.
- The rendered Toolchain layout was inspected at 1440 × 940; the three checkboxes align and the complete note appears below All levels of detail. Capture: `work/preview-build/toolchain-ui-eUVcDu/toolchain.png`.

## Avolition 0.8.16 validation (historical)

## Automatic normals for native export (2026-10-14)

The user's Esperança / D53 / Night - Push map contained six zero-length normals in four mesh prototypes, reused by 115 objects. They are valid automatic-normal requests in Blender, but the C4D export preparation previously rejected them. Export preparation now retains and normalizes valid authored directions, reconstructs zero directions from corner-angle-weighted incident triangle normals, and preserves vertex splits and topology. Nonfinite and malformed data remain errors. Degenerate-only or unused vertices receive a finite placeholder; exactly cancelling contributions fall back to the largest incident face. These cases are reported in export warnings. Compact maps perform this work once per prototype. Map, single-animation and animation-set payloads propagate these warnings independently of bridge responses.

- **444 Python tests passed**, including 13 new targeted regressions. Coverage includes thin triangles, unequal corner angles, real opposing-face topology, split vertices, degenerate/unused vertices, cancellation, source immutability, native corner reflection, prototype sharing and warning propagation.
- The full real Esperança map passed payload preparation: **739 prototypes, 1,427 unique meshes and 1,147,281 vertex normals checked**. All six zeros were reconstructed and every valid authored direction retained. Source file hashes, positions, indices, UVs, skin data, placements and the hierarchy were unchanged. Report: `work/preview-build/avolition-v0816-esperanca-check/prepared.json`.
- A factory-startup Blender 5.1.2 probe using the installed Overwatch importer confirmed the generated directions for all six affected vertices (minimum direction dot product > 0.9999994). In particular, angle weighting matches Blender's -Y result for model 11770; area weighting would point +Y. Report: `work/preview-build/esperanca-blender-normal-probe.json`.

- **Live Cinema 4D 2026 / Octane save passed with the existing Avolition C4D Bridge 0.7.4.** A subset containing all four affected real map models saved and reopened successfully with **9 meshes and 9 Octane materials**, no animation tracks, and centimetre units. The verified file is 517,641 bytes. Report: `work/preview-build/avolition-v0816-esperanca-check/verified-native-report.json`. The completed test job was archived out of the live mailbox, and no open editor document was changed. As in 0.8.14, the QA driver copied a prepared request into the authorized mailbox; the production client filesystem path is covered by automated tests.
- The freshly built **0.8.16 Windows executable smoke test passed** native IPC, WebGL, animation playback, textured fixture meshes, hierarchy visibility, unchecked export queue entries, no implicit exports and clean shutdown with persisted settings. Report: `work/preview-build/avolition-v0816-staged-smoke.json`.

Full-map validation above covers payload preparation and normal data. The live native save covers the affected-model subset, not a complete 18,761-mesh Esperança save or a rendered shading comparison.

## Avolition 0.8.15 validation (historical)

## Character scans exclude map dependencies (2026-10-14)

Animation scans recognize immediate `.owmap` files as map-export markers, then prune neighboring Models, Entities, Effects and other standard map resource branches before descending. Same-directory map assets are omitted. An ancestor check covers direct scans inside those resource branches. Classification does not depend on the map's display name or a folder named Maps, does not parse map geometry and preserves unrelated character sibling folders. This requires DataTool's exported resource layout and a neighboring map marker; detached or arbitrarily rearranged dependencies cannot reliably be classified this way.

The Library source help reflects the filter. The redundant Open Output Folder button beside Export from Overwatch and its event/state handlers are removed. Output Directory Browse/Use Library and the separate Maps and animation-export output shortcuts remain.

- **431 Python tests passed.** Eight new scan regressions cover shared libraries, traversal pruning, direct scans at multiple map-folder depths, renamed exports, mixed case, unrelated character siblings, non-map folders named Maps and unreadable ancestor metadata.
- A read-only scan of the user's real library retained **91 character model/entity files and 340 clips**, with **zero map assets** in Animations and no warnings. Source files were not modified. Report: `work/preview-build/animation-map-filter-bdctdxel/report.json`.
- Toolchain source UI and Toolchain Maps UI regressions passed after the button removal; the source UI uses mocked backend actions. Report: `work/preview-build/toolchain-ui-dtlslZ/report.json`.

- **Packaged 0.8.15 Maps/Animations integration passed** against a shared synthetic library through native IPC: four character assets and one character clip appeared in Animations, both map variants remained in Maps, and no map dependencies leaked into the character list. Source-folder browsing, scene loading, renaming, output validation, recovery and restart persistence also passed. No renderer errors occurred. Report: `work/preview-build/maps-ui-0pUBvJ/report.json`. The Toolchain screenshot was inspected to confirm the removed header shortcut and retained directory controls.

## Avolition 0.8.14 validation (historical)


## Bridge compatibility, official name and Maps header (2026-10-14)

The installed Cinema 4D 2026 companion was identified as bridge 0.7.2 with Octane available but no map-instance capability. A running application alone therefore did not satisfy map-export requirements. The map capability check stays enforced, now reports the detected version, and directs users to the bundled Avolition C4D Bridge 0.7.4. Job dispatch repeats the capability check after texture baking. When multiple live Cinema instances exist, a map-capable Octane instance takes priority over a more recent older bridge heartbeat.

The companion folder, Python entry and registered plugin name now use Avolition C4D Bridge. Plugin ID, internal module package and the existing mailbox are retained for compatibility. Heartbeats include version, official name and loaded plugin directory. Bundle tests reject mixed legacy/new entries rather than distributing duplicate plugins.

- **423 Python tests passed**, including bridge identity/registration, malformed heartbeat handling, multiple-instance selection, queue aliases, legacy-map rejection before baking, native payload and transaction regression coverage. These automated native-save tests use mocked Cinema APIs.
- The packaged **0.8.14 Maps integration test passed** with native IPC, real parser and renderer, synthetic OWLib files, source-folder picking/scanning, hierarchy renaming, visibility, failed-load recovery, output validation and state persistence over two restarts. Only native dialogs and shell opening were intercepted. The Map scene header and Open .owmap button are absent, and source scanning plus variant selection remain functional. Report: `work/preview-build/maps-ui-UW4lRD/report.json`; wide and compact captures were generated and the wide layout inspected.
- The tested 0.7.4 companion was installed into the Cinema 4D 2026 plugins directory after the user closed Cinema. Every installed file hash matched the bundle. The original 0.7.2 folder was moved into `work/preview-build/bridge-backups/Whitehound-C4D-Bridge-0.7.2-before-0.7.4`, outside all Cinema plugin search paths. Other plugins were not modified.

- **Live Cinema 4D 2026 / Octane save passed with the installed Avolition C4D Bridge 0.7.4.** A synthetic parsed map with signed/nonuniform placements and renamed object/mesh nodes produced a 137,798-byte native scene containing 5 meshes, 2 joints, 3 Octane materials and one skin. The bridge reopened it and verified structure, materials and transforms. No animation tracks were created; units remained centimetres. Report: `work/preview-build/avolition-v074-native-service-check/report.json`.
- This live check used a prepared payload and texture directory copied into the authorized queue with the request published last. The execution sandbox blocked Python's atomic rename inside the external mailbox, despite granted access, so the live run establishes the unmodified native service/save path rather than the complete production client's file-write path. Client transaction coverage is provided by the Python suite. No production filesystem logic was weakened for this test. The completed test job was archived out of the live mailbox.
- The staged executable smoke test passed native IPC, WebGL, animation playback, eight textured fixture meshes, hierarchy visibility, unchecked export queue entries, no implicit exports and clean shutdown with persisted settings. Report: `work/preview-build/avolition-v0814-staged-smoke.json`.

These checks do not claim a full-size King's Row native export or an Octane render comparison. Earlier map material approximation limits still apply.

## Avolition 0.8.13 validation (historical)


## Map material coverage, navigation and variant hierarchy (2026-10-13)

Map preview textures now share a scene-wide budget with adaptive image dimensions, replacing the first-128-texture cutoff. Repeated material descriptions reuse GPU materials. Map export baking deduplicates repeated appearances and keeps source texture resolution. Collision filtering follows the installed Blender importer's exact `CollisionMaterials` GUID list and default removal behavior; it does not classify objects by shader number, color or name. Filtered mesh references preserve source mesh indices so saved object names remain attached to the intended submesh.

Map geometry retains its raw metre scale. Map-specific zoom limits, clipping and grid spacing support moving close to individual details; the native exporter retains its established metre-to-centimetre conversion. Static previews stop rendering once the controls settle, while camera movement and scene changes request new frames.

Large-map framing uses a guarded placement-distribution estimate only when distant or oversized backdrops more than double the bounds of the retained main content. It requires at least 100 visible placements and keeps at least 85% in the framing estimate; small, linear and separated-district fixtures retain appropriate bounds. No geometry is removed by framing. Full bounds still control far clipping, and Shift+F/Shift-click always frames the complete visible scene. King's Row's framing extent changes from roughly 1,386 to 310 source units.

The map library groups the exported map name, build/variant folders and `.owmap` entries without loading map geometry. Natural sorting, counts, search expansion, selected-variant retention and keyboard navigation follow the compact Preview objects style. Windows and Unix paths, Unicode names, direct map files and equal variant names in different folders are covered.

### Verified

- **413 Python tests passed**, including collision filtering, shared map appearance baking, texture decoding/cache behavior, naming and existing export transaction regressions. Native scene-save successes in these tests use a mocked bridge or fake Cinema API.
- **64 map viewport checks, 6 real WebGL checks and 676 existing viewer checks passed**, including guarded backdrop framing, elevated map overview, close navigation, preserved transforms, idle rendering and reactivation after camera changes. Character viewing angles remain unchanged.
- Material and viewer GPU checks passed. The adaptive-budget regression loads all **800 texture views** across 400 synthetic materials, retains bounded preview pixels and reuses equivalent material descriptors; this is a controlled resource test, not an FPS benchmark.
- `test_map_library.mjs` and `test_map_library_ui.cjs` passed. The renderer test checks real DOM/CSS, folder search, selected variants, click and keyboard expansion, and compact row layout with zero map geometry requests. Screenshot inspected: `work/preview-build/map-library-ui-bnFxRg/map-library.png`.
- Read-only parser/material investigation of **King's Row (Winter) → 6B1 → Evening - Hybrid** found 7,576 source mesh placements. The Blender collision list removes 1,129 engine hit-test meshes, leaving 6,447 visible meshes; 6,383 have source material bindings and 6,010 have base-color maps. The 64 unbound slots are absent from their referenced looks. Details and reproducible audit tools are recorded in `owpreview/MAP_FORMAT_REFERENCE.md`.
- A real source-resolution baking audit sampled the richest descriptor for each of the map's 13 shader IDs: **59 PNGs**, from 4×4 to 1024×1024, with all 63 involved source file hashes unchanged. This covers common material channels, not every layered game shader. Report: `work/preview-build/map-native-texture-review/report.json`.
- The packaged **0.8.13 real-map renderer** passed against King's Row (Winter), Evening - Hybrid: **6,150 / 6,150** meshes with available UV-compatible texture maps received textures; **27,924 / 27,924** expected material-role applications succeeded; no preview-budget warnings occurred. It used 3,255 texture views, 800 shared geometry objects and 826 material objects across 6,447 retained meshes. There were 898 map-role references without the required UV channel and no missing texture-file failures; those UV-incompatible references are excluded from the valid-role denominator. All 6,331 source file paths, sizes and modification times and the map SHA-256 stayed unchanged. Report: `work/preview-build/real-map-ui-hVEVyq/report.json`.
- The native **0.8.12 baseline** loaded the same source with 1,626 textured meshes out of 7,576 total, 25,833 missing valid material-role applications, and 3,182 budget-fallback occurrences. The different total mesh count includes engine collision surfaces removed in 0.8.13. Locally observed load times were 37.5 seconds for the baseline and 48.3 seconds for 0.8.13 with whole-map texture coverage; these are individual runs, not performance guarantees. Draw calls in the recorded scene fell from 7,752 to 6,623. Reports: `real-map-ui-JvooOr/report.json` and `real-map-ui-hVEVyq/report.json` beneath `work/preview-build`.
- **48 lazy object-tree renderer checks passed**, covering deferred children, expansion, keyboard order/focus, visibility, naming and scene reset. Packaged synthetic Maps regression also passed native file picking, real backend validation, output-folder routing, repeated geometry, visibility, renaming, failure recovery and settings/tab continuity across restarts. Only native dialogs/shell actions are intercepted. Report: `work/preview-build/maps-ui-Ad9jB9/report.json`.
- The final elevated camera and focused framing were visually checked with direct GPU overview and close-up captures from the native app, independent of the hidden window compositor. King's Row still loaded all 6,150 valid textured mesh placements, in 48.3 seconds, with unchanged source inventory and hash. Report and captures: `work/preview-build/real-map-ui-LI3XeB/`. The overview includes backdrop geometry; specialized backdrop/effect shading remains approximate.
- The staged 0.8.13 executable smoke test passed native IPC, WebGL, animation playback, all eight fixture mesh textures, hierarchy visibility, unchecked export queue entries, no implicit exports and close/save behavior. Report: `work/preview-build/avolition-v0813-staged-smoke.json`.

### Limits

No successful live Cinema 4D/Octane scene save or render is established by these tests. The baked images and mocked scene saves validate separate portions of the export path. Game lights, sounds, effects and gameplay logic remain omitted. Secondary layered shaders, exact refraction and subsurface effects remain approximations. Missing bindings stay explicit, and unresolved emission masks are not treated as finished emission color. No new live game extraction is claimed. Hidden-window frame timing is not treated as an FPS benchmark; compositor throttling can distort it. Performance improvements do not guarantee a particular frame rate on large maps or different graphics hardware. Hidden Electron compositor screenshots can retain an old loading-overlay frame, so they do not by themselves establish completed visual output; the real-map harness additionally supports generation-checked, synchronous GPU-buffer captures after an explicit QA render.

## Avolition 0.8.12 validation (historical)

## Maps, shared styling and editable export names (2026-10-13)

The crossed-out header artwork, empty-viewport logo and standalone-version note are removed. The Windows name/icon and upper-right version remain. Panels and controls use the compact gray Preview objects palette, with opaque dark dropdowns.

Toolchain now has independent Animations and Maps choices using its existing process, cancellation and log lifecycle. Map extraction resolves real catalog IDs to DataTool `extract-maps` queries. Official OWLib source, documentation and released executable help were checked; no Overwatch installation was available for a live extraction.

The top-level Maps workspace has its own source folder, searchable library, viewport, object hierarchy, output directory and C4D export action. It reads OWMAP 2.0/2.1, retains per-instance material looks and placements, and reuses geometry across repeated objects. The bridge transports compact geometry and creates native map objects. Maps require bundled bridge 0.7.3; older bridges fail before creating files. Animation exports remain compatible with 0.7.2.

Scene/model/mesh names support double-click or F2 editing, Unicode, validation, Enter/blur saving, Escape cancellation and explicit save failures. Single-click target selection remains immediate. Source paths, joint names, binding identifiers and geometry are unchanged. Each new animation queue entry captures its names; subsequent edits do not alter earlier entries. Maps apply their current saved names. Re-extracted assets ignore aliases for objects that no longer exist.

### Verified

- **405 Python tests and 959 JavaScript checks passed.** Includes map binary parsing, transforms, shared geometry, material overrides, bounds, source-path validation, naming, settings rollback, real staging/publication and queue snapshots. Successful native writes use a mocked bridge/fake Cinema API.
- Source Maps renderer tests use actual synthetic OWLib bytes, real parsers and real settings: two independent scenes, shared GPU geometry, static-object selection, saved names, hierarchy visibility, failed-load cleanup and recovery, output-path save failures, and preserving a custom Maps root during Toolchain refresh. Only the Toolchain completion notification is mocked in the refresh test. Report: `maps-ui-tNtRNY/report.json` under `work/preview-build`.
- Source queue, texture and hierarchy regressions passed. Pending name saves are awaited before queueing; snapshots stay immutable after later edits. Reports: `export-queue-ui-OTyx8b/report.json`, `texture-ui-xswPbf/report.json`, `objects-tree-ui-GxmSa9/report.json`. Inline rename tests cover double-click/F2, failure recovery and independent trees.
- Real read-only Widowmaker checks passed for Gameplay1P and Gameplay3P: 6/9 textured meshes, actual attachments, all bone parents, immediate target selection, partial 85/93, 18/19 and 108/112 matches, independent visibility, and shared playback with zero pose-comparison error. All 2,731 source file paths/sizes/timestamps and 95 source hashes stayed unchanged. Report: `real-objects-tree-ui-ad6zts/report.json`.
- Packaged Electron tests passed for the new Maps renderer and tree, real native backend IPC, animation playback/queue, close/save recovery, Toolchain paths/tab continuity and window isolation. Reports: `maps-ui-IA7P0p/report.json`, `objects-tree-ui-Yvwf2O/report.json`, `desktop-regression-93efrt/report.json`, `toolchain-desktop-HTLImo/report.json`.
- Native Maps file-picker filtering, chosen-file loading, cancel preservation, validated output-folder opening, and both workspaces/subtabs/settings/names across two restarts passed. Only Electron dialogs and shell opening were intercepted; backend operations were real.
- Map and Toolchain layouts were visually inspected at normal and compact window sizes. Dark dropdown styles were checked; page capture does not prove the appearance of a separate OS-native popup surface.

### Limits

No real exported map, live game extraction, or successful live Cinema 4D/Octane save was available. The map workflow is verified with source-derived binary fixtures and mocked native saves. Maps currently preview/export geometry and common materials; game lighting, sounds, effects, gameplay logic and exact environmental shading are not reconstructed. Missing references and omitted light/sound records are reported. Very large maps can hit explicit parser, memory or 256 MB bridge-payload limits. Native transform restrictions are documented in `owpreview/MAP_FORMAT_REFERENCE.md`.

QA uses isolated profiles and hidden windows. The restricted QA launcher uses `--no-sandbox`; production context isolation/sandbox preferences stay enabled. No installed application, plugin, source export or existing release archive was overwritten.

## Avolition 0.8.11 validation (historical)

## Branding, dark dropdowns and permanent object hierarchy (2026-10-13)

The application is now Avolition. The supplied NAS `Avolition.tif` was read and converted without redesigning its artwork into PNG and Windows ICO assets. Visible application titles, installer messages, executable metadata and packaging use Avolition. Existing Whitehound catalogs migrate once into `%LOCALAPPDATA%/Avolition`; an existing Avolition catalog wins. Explicit profiles remain isolated. The installed Whitehound C4D Bridge, its queue and internal desktop protocol retain their compatibility names.

Preview objects occupies the right inspector beneath clip details. Compact square expanders, hierarchy lines and object icons show actual entity attachments, submeshes and skeleton parents. Models with no meshes remain represented. Clicking a model selects its animation target; independent visibility controls only change the preview. Expansion survives tab switches and refreshes of the same entity. Arrow keys navigate/collapse/expand, Enter selects a model, and Space changes mesh visibility without scrubbing the timeline.

The joined sorting control previously supplied a translucent background to the native popup. Selects, options and optgroups now have explicit opaque dark backgrounds and readable text, while the system supplies selection highlighting.

### Changed implementation

- `web/object-menu.js`, `web/viewer.js`: actual hierarchy metadata, permanent tree, expansion, focus, keyboard and visibility behavior.
- `web/index.html`, `web/styles.css`, `web/app.js`, `web/toolchain.js`: right inspector layout, dropdown colors, scene naming and Avolition branding.
- `web/assets/avolition-logo.png`, `web/assets/avolition.ico`: supplied artwork in runtime formats.
- `app.py`, `desktop/main.cjs`, `desktop/preload.cjs`, `desktop/package.json`: branding, migration and native smoke verification.
- `owpreview/datatool.py`, `owpreview/toolchain_runtime.py`, `owpreview/c4d_octane.py`: current product labels while preserving bridge compatibility.
- `build.ps1`, `tools/package_desktop.py`, README, notices and regression tests: Avolition 0.8.11 distribution and coverage.

### Verified

- **356 Python tests** and **959 JavaScript checks** passed. The new tree tests exercise malformed/cyclic parents and actual bone nesting. Migration tests cover preservation of existing catalogs, legacy priority, explicit profiles, queue/custom-path retention and invalid metadata.
- New hierarchy renderer tests pass with the real parser in source and in the packaged Windows executable: five-model fixture with attachments, a skeleton-only object, bone parents, keyboard navigation, independent visibility, model selection, tab continuity and decoded branding. Layout fits 1440x940 and 1060x700 without horizontal overflow. Skeleton and joint branches expand progressively. Reports: `objects-tree-ui-Opt8fk/report.json`, `objects-tree-ui-mwQlxl/report.json` under `work/preview-build`.
- Source preview regressions pass for eight textured meshes, polygon counts, individual/all visibility, reload reset, failed-load reset, animation assignment preservation, stale clip responses and per-object matching across different skeleton sizes. Reports: `object-ui-0EDhmi/report.json`, `texture-ui-H0LIKm/report.json`, `manual-objects-ui-8IKHBo/report.json`, `object-search-ui-WhAmSf/report.json`.
- Source queue regression passes for real catalog persistence, unchecked restoration, whole-entity snapshots, sequential checked jobs and failure retry. Two successful native saves are mocked; the disconnected-bridge failure is real. Report: `export-queue-ui-oHQb7h/report.json`.
- Packaged native regressions pass for IPC, pickers, playback, Unicode paths/names, close recovery, persisted queue entries, Toolchain directories/tab continuity and partial/mixed-rate animations. Reports: `desktop-regression-PHAXmJ/report.json`, `toolchain-desktop-u4gTnu/report.json`, `animation-set-desktop-Y44Fld/report.json`.
- Dark styles are checked for every select, option and optgroup in source and Electron. Page screenshots cannot reliably capture an OS-native popup, so the selected native popup surface is not claimed as visually inspected. Normal workspace screenshots were inspected.
- Real read-only Widowmaker renderer checks pass on the available flat DataTool export: Gameplay1P (6 meshes, 85/19/1-bone rigs) and Gameplay3P (9 meshes, 190/19/1-bone rigs), every model/joint parent, preserved attachment nodes, partial 85/93, 18/19 and 108/112 bindings, independent visibility and paired playback. The shared end pose matches the existing evaluator with zero numerical error. All 2,731 source file paths/sizes/timestamps and 95 source hashes are unchanged. All 6/9 meshes receive textures; existing unsupported shader approximations remain reported. Whole-workspace and object-panel screenshots were visually inspected. Report: `real-objects-tree-ui-qjwo8O/report.json`. No native save or API mocking occurs in this test.

### Limits

Successful live Cinema 4D saving/Octane rendering and game extraction were not performed. This release retains the existing exporters. Native tests use isolated profiles and a disconnected bridge; only the restricted QA launcher uses `--no-sandbox`, while production window isolation settings remain enabled.

## Whitehound 0.8.10 validation (historical)

## Named export list and streamlined library (2026-10-13)

The animation workspace now has a joined search/sort control, selected-object matching by default, and no track-count groups. Name A–Z is in More sorting options. Animation setup replaces immediate combined/nickname exports with Add to export and a persisted list. Each entry captures the full entity and its one or more assigned clips under entity_nickname. New/restored checkboxes start unchecked; only explicitly checked entries export C4D, sequentially, to the common directory in the export menu. The old nickname pane, raw-copy UI and top Clip copies row are removed.

### Changed implementation

- `app.py`: persistent export queue APIs, full-entity snapshots, common destination, one-time default-sort migration and saved-entry validation.
- `web/app.js`, new `web/export-queue.js`: explicit Add and checked export actions, captured batch settings, per-entry results, local labels, queue restoration, keyboard focus and close-during-batch handling.
- `web/index.html`, `web/styles.css`: compact search/sort group, removed track/legacy export controls, relocated settings icon, native export dialog and list.
- `desktop/main.cjs`, `desktop/preload.cjs`: native API routing, output-folder opening and updated packaged smoke test.
- Packaging/version metadata, README, updated renderer/native regression harnesses and new queue API/UI tests. Old auto-copy and evidence-era UI harnesses are labeled historical in `tests/README.md`; they are not represented as current passing UI tests.

### Verified

- 352 Python tests, including 14 new queue API tests, and 940 JavaScript matching/preview/math checks. The queue tests exercise real parsing, scene payloads and publication with mocked native Cinema saving; a single assigned clip still retains the complete entity. Common output settings persist across scans/restarts. Failed catalog writes roll back, duplicate jobs reject, and malformed saved lists preserve existing data.
- Source renderer queue test: real backend save/remove and full restart; immutable single/multiple assignment snapshots; no export on name blur, Add or close; unchecked restoration; two sequential selected jobs with mocked successes, followed by a real disconnected bridge failure. Successful entries uncheck, failed entries remain selected. Space retains checkbox focus. Closing during a held batch returns promptly without leaving the renderer stuck in closing state. Report: `work/preview-build/export-queue-ui-lf6Rk4/report.json`.
- Real Widowmaker source/backend tests: body coverage 186/190, 108/190 and 190/190; gun overlap 18/19 versus 0/19 for equal-count clips; arms 85/93. Shared 46-frame preview and shorter-clip holding remain correct. Real queue snapshot has independent arms/gun assignments. Native save responses in the combined renderer test are mocked. Reports: `object-search-real-mQmBfV/report.json`, `animation-set-ui-ACaJbq/report.json` under `work/preview-build`.
- Manual-object source test: duplicate filtering, independent poses, visibility, stale loads, one/two-object queued snapshots, tab continuity and compact layout. Report: `work/preview-build/manual-objects-ui-Vi5zqC/report.json`.
- Packaged Windows desktop: native IPC/pickers, Unicode names/destinations, full restart and unchecked queue persistence, malformed-name rejection, no implicit file export, unchanged legacy notes, explicit disconnected-Cinema error/retry, per-object preview and Toolchain continuity. Reports: `desktop-regression-GMDbHH/report.json`, `animation-set-desktop-qJFEZn/report.json`, `toolchain-desktop-XmgAgH/report.json` under `work/preview-build`.
- Packaged object-search test across full restart: synthetic 3-, 2-, and 4-bone characters, partial/zero/unavailable matches, preserved assignments, saved sort choices and no horizontal overflow at 1024×700. Report: `work/preview-build/object-search-ui-70ZPz5/report.json`.
- Export-dialog layout fits 1024×700 and 1440×940. C4D settings expand inside the dialog; Escape closes it. All native tests use isolated QA profiles. Production sandbox/context isolation remain enabled; the restricted QA launcher alone uses --no-sandbox.

### Limits

No successful live Cinema 4D save or Octane render was possible with the bridge disconnected. Successful native exports are mocked in automated tests; real native IPC failures are verified separately. No Overwatch installation is available for a new game-extraction check. The Toolchain workflow is otherwise preserved. Real source assets remained unchanged.

## Previous 0.8.9 validation (historical)

## Object-aware animation browsing (2026-10-13)

The library now reads skeletons from the loaded entity, defaults to selected-object sorting, shows actual bone coverage, and offers dynamic track-count groups. No character-specific joint counts or weapon-role guesses are used. Source-model priority is scoped to the same model folder and export path, with identical source copies retained as context. All clips remain accessible; neither overlap nor folder association guarantees the intended action.

- **338 Python tests passed**, including 11 lightweight binding-reader/cache tests and 8 new API/preference tests. Structural corruption, duplicates, bounds, replacement races, Unicode paths, bounded caching, invalid preferences and restart persistence are covered.
- **940 JavaScript checks passed**, including 51 matching/grouping checks. Arbitrary skeleton counts, exact names, partial clips, same-count mismatches, unknown metadata, hero/skin folder scope, duplicate aliases and data-derived groups are covered.
- All **246 real Widowmaker source clips** returned the same bone names as the full playback parser. The lightweight reader seeks over keyframe arrays rather than allocating them. Cold read measured 0.827 seconds and cached read 0.023 seconds on this machine; these are local observations, not performance guarantees. Numeric keyframe values remain validated by the playback parser.
- The real source renderer/backend test passed without mocked APIs: Gameplay3P coverage of 186/190 and 108/190, weapon matches of 18/19 and 0/19 for equal-count clips, and POV coverage of 85/85 from a 93-track clip. Count groups reflect search and duplicate visibility. Sorting/filtering preserve assignments and playback. Source hashes remained unchanged. Report: `work/preview-build/object-search-real-QIJoam/report.json`.
- Existing selected-object and combined-preview source tests passed. Their native C4D save responses remain mocked. Reports: `work/preview-build/manual-objects-ui-yTkLoz/report.json` and `work/preview-build/animation-set-ui-rLaunG/report.json`.
- The rebuilt 0.8.9 desktop passed existing native preview/persistence, Toolchain, and combined-animation IPC regressions. Reports: `work/preview-build/desktop-regression-rWpvaS/report.json`, `work/preview-build/toolchain-desktop-R3imlf/report.json`, and `work/preview-build/animation-set-desktop-jDFeP3/report.json`. No live Cinema save/render or new game extraction is claimed.
- The new search workflow passed with both the source backend and the packaged native IPC backend. Synthetic characters use 3-, 2-, and 4-bone rigs, including shared names, partial/extra tracks, duplicate files and damaged clips. Checks cover dynamic denominators, exact group counts, unknown versus zero matches, retained playback/assignments, persisted sort/duplicate choices, and a full native restart. At 1024×700 the packaged library list retains 152 pixels of height without horizontal overflow. Reports: `work/preview-build/object-search-ui-msNpfM/report.json` and `work/preview-build/object-search-ui-GDiZBO/report.json`.

## Previous 0.8.8 validation (historical)

## Combined object animations (2026-10-13)

Animations now shows only Library source and Clip copies; Overwatch Install and Output Directory appear only on Toolchain. Analysis controls and automatic analysis on scans are removed. The dormant analysis backend and research utility remain available for future work.

Animation setup keeps a separate clip per object and lists each assignment. The shared timeline spans the longest clip at the highest assigned FPS, holds shorter clips at their final pose, and remains usable when the selected object is unassigned. Export combined C4D captures the complete assignment set and preserves the loaded entity's attachment hierarchy. Nickname exports remain selected-object exports. Visibility and preview In-place do not change exported motion.

- **319 Python tests passed**, including 10 combined-payload and 7 combined-API tests. Synthetic scenes exercise different rates/durations, local joint transforms, partial matches, unassigned attached objects, texture baking, invalid inputs, naming collisions, root motion, source immutability and failure cleanup. API publication tests mock only the native Cinema save.
- **889 JavaScript checks passed**, including mixed-rate playback, shorter-clip holding and attached multi-rig poses. Dormant evidence utility tests remain in this count.
- Real source/backend renderer tests passed for independent assignments, pending loads, immutable export requests, duplicate prevention, shared timeline, selected-object nickname exports, tab state and compact layout. C4D replies in these renderer tests are mocked.
- The user's real Widowmaker Gameplay1P entity and example clips were parsed and previewed: arms matched **85/93**, gun **18/19**. The combined payload retains 105 bones and animates 104 joints across two rigs, over 46 frames at 30 FPS. The gun retains its attachment through the arms' `bone_004A`. These examples validate mechanics, not a semantic claim that the clips are the correct reload pair.
- Missing tracks are ignored as before. No world-position meaning is inferred for the unmatched gun track, and no synthetic replacement track is generated.
- Cinema 4D's bridge was disconnected. A live C4D save/render and visual attachment confirmation in Cinema remain unverified. No Overwatch installation is available for new extraction tests.
- The rebuilt **0.8.8 Windows desktop** passed existing native regressions, Toolchain layout/persistence checks, and a new two-rig combined-animation test through the actual preload and compiled backend. The combined test isolates an empty bridge queue and verifies the real disconnected-Cinema error, retry behavior, duplicate-assignment rejection and clean close; it does not simulate a successful native save. Production window isolation/sandbox settings remain enabled (the restricted QA launcher alone uses `--no-sandbox`).

Renderer reports: `work/preview-build/animation-set-ui-KSWgU4/report.json`, `work/preview-build/manual-objects-ui-Mljdl7/report.json`, and `work/preview-build/toolchain-ui-bfgksS/report.json`.

Native desktop reports: `work/preview-build/desktop-regression-bjv0sW/report.json`, `work/preview-build/toolchain-desktop-wbbk4b/report.json`, and `work/preview-build/animation-set-desktop-FUWJgy/report.json`.

## Previous 0.8.7 validation (historical)

## Local animation evidence

The new resolver reads assets without renaming them, assigning animations, changing preview state or writing nicknames. It separates resource identity, content equivalence, occurrences, model associations and usage contexts. Original source names and unavailable GUID/build fields remain unknown.

- **302 Python tests pass**, including 15 bounded evidence-format tests and 31 resolver/cache/API tests. Fixtures cover engine-distinct full GUIDs with identical short filenames, conflicting references, duplicate contents with distinct contexts, partial-body matches, ambiguous candidates, unavailable metadata, asynchronous jobs and byte/parser/provenance cache invalidation. Rescans refresh cached evidence; overlapping roots retain separate details; managed copies are excluded while existing destination originals remain visible. These synthetic cases do not claim new game extractions.
- **859 JavaScript checks pass**, including 13 new occurrence-filter tests. Mocked evidence UI tests verify missing values, literal GUID strings, stale-cache clearing, escaped evidence, filters and lifecycle.
- The real source backend and renderer preserve independent object assignments, frame, canvas, model objects and nicknames while analyzing/filtering. Existing raw-copy/C4D request behavior remains intact; native C4D responses in that suite are mocked. Report: `work/preview-build/manual-objects-ui-F9vsLs/report.json`.
- **Real Widowmaker export:** 246 occurrences, 245 scoped assets, 52 models, 40 entities, 16 companions, no parse errors. Recovered 16 explicit animation/model associations; 91 assets have fully compatible candidate skeletons, including 87 with additional skeleton suggestions. No original names or particular named action contexts were recovered. Source/game build remains unknown. Existing profile nickname coverage was unavailable to the separate research audit and is recorded as null.
- Real export renderer QA loaded Gameplay1P and clip `00000000033C`, checked its separate 42/42 model association, and preserved existing preview state. All 2,731 source file paths/sizes/timestamps and selected source hashes stayed unchanged; no export calls occurred. Report: `work/preview-build/v087-real-evidence-qrpUr8/report.json`.
- Packaged native regression passes on v0.8.7: picker IPC, Unicode paths, preview, per-object assignments, duplicate defaults, raw-copy integrity, close recovery and persistence. Separate Toolchain desktop regression preserves equal directory widths, tabs, defaults and existing workflow behavior. Reports: `desktop-regression-X8J4kl/report.json` and `toolchain-desktop-wamMaz/report.json` beneath `work/preview-build`. Only the QA launcher uses `--no-sandbox`; production window sandbox/context isolation remain enabled.
- Native **animation analysis** passes against the frozen backend and real Widowmaker export: all four IPC capabilities, 245 assets/246 occurrences, source-name/GUID unknowns, 42/42 explicit model match for clip 033C, unchanged playback/assignments/nickname, and cache validation/restoration after a real app restart. A valid cache keeps its hash and timestamp; an unrelated root cannot read its detail. All 2,731 source files remain unchanged and no exports are created. Report: `work/preview-build/analysis-desktop-7y80OK/report.json`.
- Official OWLib source was checked at the installed package's commit. All 769 located source documents matched SHA-256 checksums in the installed PDBs. See `owpreview/ANIMATION_SOURCE_REFERENCE.md` for pinned sources and limitations.

New implementation: `owpreview/animation_analysis.py`, `animation_evidence_formats.py`, `web/animation-analysis.js`, `tools/analyze_library.py`. Integration edits: `app.py`, desktop API allowlists, existing web layout/styles/app hooks, version/package metadata. Preview evaluator, export pipeline and nickname semantics are unchanged. Cache resides under the existing application data directory, never the export root.

No Overwatch installation is present, so new named-unlock extraction and live gameplay reference traversal were not tested. No new native Cinema 4D/Octane render was performed. The complete real inventory and investigation report are delivered separately as `animation-index.json` and `animation-analysis.md`.

## Whitehound 0.8.6 validation (historical)

## Toolchain integration

Implemented on the active 0.8.5 fork, preserving its manual per-object animation workflow. No 0.9.0 pairing/perspective restriction was reintroduced.

- **256 Python tests pass**, including 20 installer/release tests, 16 DataTool tests, and nine workflow/API integration tests. Installer tests mock GitHub/download responses to cover digest failures, unsafe archives, incomplete dependencies, same-tag asset replacement, offline metadata, cancellation and failed atomic activation retaining the old runtime. DataTool tests use synthetic JSON catalogs and harmless real Python child processes, including Windows process-tree cancellation and immediate parent exit. These are not game extraction tests.
- **846 JavaScript checks pass** across pose, viewer mathematics, materials, navigation and duplicate-library modules. Source browser regression verifies real fixture parsing, preview deformation, copy integrity, nickname sorting, removed FBX controls, preview-only In-place and persistence, with zero uncaught errors.
- Renderer Toolchain tests use **mocked backend release/job APIs**, exercising equal directory widths, tab/header alignment, persistent controls, UTC metadata separation, literal selection transport, inline errors, cancelled/failed/completed states, escaped logs and one library refresh per completed job.
- Real source backend + Three.js fixture tests retain the same canvas, viewport, model objects, rig/clip assignments, nickname/filter/sort and paused frame across tabs. Playback continues while the Animations tab is hidden; tab keyboard navigation does not step the clip. Existing selected-object raw exports remain byte-identical. C4D responses in this suite are simulated.
- The packaged Windows desktop regression passes native picker IPC, Unicode paths, load/playback/scrubbing, per-object assignments, hide-duplicate defaults, raw-copy saving, close-error recovery, restart persistence and sandbox/context isolation. The production app keeps Electron sandboxing enabled; the test host alone uses `--no-sandbox` for launch.
- The additional native Toolchain suite passes three app launches: executable-adjacent library/toolchain defaults, native Unicode Browse through actual IPC, inline invalid-game feedback, Open Output routing, equal-width directory controls, preview continuity, custom directory/tab restart persistence and the portable Use Library marker. Only native dialog selections and folder opening are intercepted; release/job APIs are not mocked in this suite. Reports: `work/preview-build/desktop-regression-URI9yF/report.json` and `toolchain-desktop-5cFcRW/report.json`.
- **Real upstream installation:** the manager downloaded the exact `toolchain-release.zip` from the latest-release API response, verified GitHub's SHA-256, extracted all 305 archive entries, validated the executable and declared runtime dependencies, and restored installed metadata offline. The tested release was `v2.24.1.0+1149`; neither its version nor dates are hardcoded in application code. Installed and available metadata remain separate.
- **Real DataTool execution:** released executable help ran successfully, including through the suspended/contained Windows worker. Main and mode help plus matching `df522f0c` source confirmed the extraction/listing modes, query categories, PNG/owanimclip outputs, `--flatten`, skip flags and read-only `--online=false`. Help was correctly not treated as a completed export.

There is no Overwatch installation on this machine (confirmed by the user), so real hero/unlock catalog loading and end-to-end game extraction remain unverified. No claim is made that a model or animation was extracted from the game. Native Cinema 4D/Octane behavior is unchanged and was not revalidated in this task.

References inspected: `Screenshot 2026-10-04 192823` (3ds Max horizontal tabs), `192838`/`192838markup` (current animation layout and top path controls), `193240` (upper-left tabs aligned with Local Preview), and `213104` (portable `library` and `toolchain` folders). The named `HOW_TO_USE_A_COMMAND_LINE` tutorial was not available in the supplied files or discovered references; official OWLib README, released help and matching source were used instead. The screenshots do not define additional extraction categories, so this release exposes the verified model/gameplay, emote, victory-pose and highlight-intro workflows only.

Changed implementation files: `app.py`; `desktop/main.cjs`, `preload.cjs`, `package.json`; `web/index.html`, `styles.css`, `app.js`, `toolchain.js`; new `owpreview/workflow_paths.py`, `toolchain_runtime.py`, `datatool.py`; packaging, documentation and targeted tests. Library/output resolution is shared through `WorkflowPaths`; the old nickname/C4D destination and existing preview/import code are retained.

## Whitehound 0.8.5 validation (historical)

## Manual object animation fork

The source was extracted from the delivered Whitehound 0.8.4 ZIP into `work/whitehound-v084-fork`. Version 0.9.0's pairing analyzer, saved pairs, UI and perspective restriction are absent. The existing 0.9.0 source and release remain separate.

Hide duplicates defaults ON and groups byte-identical source clips using streaming SHA-256, with cheap header/size candidate pruning and a bounded file-stat cache. Source paths and annotations remain intact. Tests keep different-content files with the same filename/size visible and cover failed reads, changing files, cache invalidation, hidden-alias searches and representative selection.

Explicit per-model assignments retain other objects' clips while browsing. Each rig has its own evaluator and joint-name scope, sampled on the active object's timeline at its source frame rate. C4D and raw-copy requests capture the selected model; simultaneous saves of the same clip on different objects retain their own filename and scope. No perspective classification or automatic matching runs.

Verification:

- All **211 Python tests pass**, covering existing formats/materials/exports plus duplicate indexing, selected-asset projection and actual API output. Selected-object tests exercise real binary fixture parsing, PNG baking and raw-copy bytes; external C4D saving is mocked.
- **646 viewport**, **20 duplicate-library**, **98 material** and **11 navigation** JavaScript checks pass.
- The complete manual-object UI regression passes independent assignments and restoration, separate keyboard selection/visibility, duplicate hide/show, same-name differing contents, stale request cancellation, model-prefixed raw copies and captured C4D requests. Both 1440x940 and 1060x700 screenshots were inspected. Report: `work/preview-build/manual-objects-ui-VpfAAl/report.json`.
- Native **v0.8.5** passes duplicate hide/show (three unique clips from four files), explicit target selection, native pickers, playback/scrubbing, Unicode prefixed raw-copy saving, close-error recovery, restart persistence, removed FBX/pairing controls and unchanged viewport defaults, with zero page errors. Report: `work/preview-build/desktop-regression-sQRmOE/report.json`.
- Real Widowmaker Gameplay1P arms and Gameplay3P body/gun payload checks pass: selected-only meshes, rigs and texture references; original names, UVs and skinning unchanged; native rest-world placement error zero; checked animation-world matrix error at most 1.34e-15. Source hashes remain unchanged. Report: `work/preview-build/v085-object-export-qa/real-object-payload-report.json`.
- Packaged real Widowmaker UI checks pass two independent assignments, target restoration, no perspective restriction and default duplicate filtering (246 files become 241 visible clips). Source hashes remain unchanged and there are zero browser errors. Report: `work/preview-build/v085-real-objectsQA-8hqiqN/report.json`.
- Source texture UI passes real texture decoding, polygon counts, failed-load reset and compact layout with zero errors. Screenshots: `work/preview-build/texture-ui-e9ZvDH/`.

Attached objects export independently at their native rest placement, without baking their parent's assigned animation or including attached child objects. The same version-1 bridge payload and **C4D Bridge 0.7.2** remain in use. Actual Cinema 4D saving/rendering has not been validated here. The portable runtime uses Pillow 12.3.0; the complete license is included. Earlier sections below describe historical releases.

## Preview visibility and export controls

Version 0.8.4 removes the FBX checkbox and axis control. Every interface export snapshot disables FBX, and older saved enabled-FBX preferences are migrated while retaining raw-copy and C4D choices. The dormant FBX backend remains for future work; its obsolete UI-only regression was retired.

In-place and object visibility belong only to the preview. The Objects menu beside the polygon count toggles individual meshes or model groups, synchronizes wire overlays and skeleton helpers, retains visibility while switching clips, resets on entity replacement, and counts visible versus total polygons. Bones and model groups remain active so visible attached objects continue to animate. Frame Model ignores hidden meshes.

Verification:

- All **186 Python tests pass**. A root-motion regression captures actual C4D payload preparation through the API and checks that toggling preview lock yields identical sampled animation, while changing the export root-motion setting changes horizontal root motion. Native Cinema saving is mocked in this regression.
- **348 viewport**, **98 material** and **11 navigation** JavaScript checks pass. Coverage includes attached child transforms while a parent mesh is hidden, no mutation of source assets, clip/rig/frame/wireframe persistence, zero-mesh assets and visibility reset.
- The source browser regression passes saved-FBX migration and byte-identical animation copying with In-place checked. The C4D UI regression checks C4D-only, clip+C4D, clip-only and nickname-only behavior with mocked external-save replies. Hiding all meshes and enabling In-place still sends Include mesh and Keep root motion enabled, with no preview visibility or lock setting in the export request. Report: `work/preview-build/c4d-ui-Ot6tHa/report.json`.
- The Objects interface passes real pointer and keyboard interaction, visibility across clips, visible polygon counts, reset, and bounded 1440/1060 layouts. The 1060x700 screenshot was visually inspected. Report: `work/preview-build/object-ui-oPWrdk/report.json`.
- Texture UI regression passes loading, parent/child polygon counts, failed-load reset and compact layout with zero uncaught errors. Report: `work/preview-build/texture-ui-CVyMe8/report.json`.
- The rebuilt Windows executable passes native pickers, 14 independently toggleable demo meshes, animation switching, hide/show/all, 168 polygons, disabled-FBX migration, byte-identical raw copying with In-place on, Unicode nickname close-save/recovery and relaunch. There are zero page errors. Report: `work/preview-build/desktop-regression-7xxLvG/report.json`.

The shipping bridge remains 0.7.2; no plugin reinstall is required. No new native Cinema/Octane render was performed. Previous rendering limits below remain applicable. Historical FBX checks describe earlier releases rather than an exposed feature in this version.

## Viewport defaults, nickname sorting and C4D normals

Skeleton and In-place now start OFF in both the controls and viewer. Textures and Grid remain ON. Nicknamed first groups nonblank nicknames alphabetically ahead of unnamed clips, with deterministic filename/path ordering. Editing a nickname updates the list immediately. The source browser regression, 304 viewer-math checks and 11 navigation checks pass.

All **185 Python tests pass**, including three new C4D-normal regressions for normalization, native corner order, retained tags, invalid inputs and source immutability. Real Gameplay1P preparation checks all 18,415 normals and 25,448 polygons: unit-length error is at most 2.22e-16, and original shading directions, geometry, hierarchy, weights and UVs are unchanged. Normalization occurs before the bridge, so existing bridge **0.7.2** installations need no update.

This corrects a measured difference from Blender's custom-normal behavior. It does not claim a verified Cinema/Octane visual fix: native rendering remains unavailable in this environment. See `owpreview/NORMALS_REFERENCE.md` and `work/animation-research/normal-qa-v083/real-gameplay1p-normal-report.json` for the investigation and its limits. Earlier release checks below are historical baselines.

The rebuilt 0.8.3 executable passes fresh/relaunch viewport defaults, nickname ordering, playback/scrubbing, native picker, close-save recovery and persistence checks with zero uncaught errors. Report: `work/preview-build/desktop-regression-IIR4Wd/report.json`. The intentionally hidden QA window requires DOM click dispatch for scrolled controls; visible source-browser interaction was checked separately. Native checks use the restricted-host QA flag documented below.

## Doubled zoom sensitivity

Version 0.8.2 increases the shared zoom speed from 1.5 to 3, doubling sensitivity relative to 0.8.1. Horizontal Alt+right-drag, hold-2 drag and wheel zoom use this speed. The direction and horizontal-only drag behavior remain unchanged.

All 11 existing navigation checks pass, including the twofold sensitivity ratio, horizontal/vertical input, wheel/drag agreement and event-frequency independence. The rebuilt executable also passes native navigation, playback, scrubbing, polygon count, format defaults, close-save recovery and relaunch persistence with no uncaught page errors. Report: `work/preview-build/desktop-regression-2rwG7N/report.json`. Native checks use the restricted-host QA flag explained below. Export logic and the C4D bridge remain unchanged; earlier validation results are retained below as historical baselines.

## Favorites removal and viewport polygon count

The favorites filter, clip-details button, row stars and associated interface behavior are removed. Existing catalog metadata remains intact. A top-right viewport label counts loaded triangle faces, including child models, and resets when replacing an entity or after a failed load. Grid, skeleton and wire overlays do not contribute to the count.

Both live browser checks pass: search and sorting still work, legacy favorite metadata survives nickname edits without appearing in the UI, and the polygon label remains stable through playback, scrubbing and display toggles. Fixture totals of 96 faces, 24 faces across parent/child instances and zero faces match the label; failed replacement clears it and retry restores it. The 1060x700 layout was visually inspected (`work/preview-build/texture-ui-sD0Lkm/compact.png`). All 304 viewer-math checks pass.

The rebuilt **0.8.1 executable** passes native picker, playback/scrub, close-save recovery and relaunch-persistence checks, with no uncaught page errors and a verified 168-polygon demo. Report: `work/preview-build/desktop-regression-mJLRII/report.json`. Its separate real FBX-only export verifies 96 polygons, absent favorites, the unchanged format defaults, 14 textures and zero raw clip copies. Report: `work/preview-build/whitehound-v081-fbx-smoke.json`. Native checks use the restricted-host QA flag explained below. The backend and C4D bridge are unchanged in this release; Python-suite results below are the preceding release baseline.

## Independent output selection

The three format toggles default to **.owanimclip OFF, FBX OFF, C4D ON**. An old catalog without the explicit raw-copy flag migrates its three format choices once while retaining saved labels, folders, axis and other options. New choices persist across restarts.

All **182 Python tests pass**. New coverage exercises all eight format combinations, no filesystem/bridge work with all formats off, raw-copy export without an entity or Cinema, selected-only collision suffixes, unchanged unselected existing files, rollback and catalog migration. C4D file bytes in transaction tests are mocked; actual source parsing, PNG baking and FBX writing run normally.

The live interface tests confirm selected-only status paths, no duplicate or imaginary clip paths, persistence, five UI combinations, and nickname-only saving/closing with a blank output folder and all formats off. The controls fit at 1060x700. Reports: `work/preview-build/c4d-ui-oO0sdx/report.json` and `work/preview-build/fbx-ui-T0fCh4/report.json`.

The rebuilt **0.8.0 executable** passed the native picker, playback/scrub, explicit raw-copy close/recovery and relaunch-persistence checks, including the new defaults. Report: `work/preview-build/desktop-regression-ozDcbw/report.json`. A separate actual executable FBX-only export produced an FBX plus 14 textures and **zero .owanimclip copies**, then closed successfully. Report: `work/preview-build/whitehound-v08-fbx-smoke.json`. Native checks use the restricted-host QA flag explained below.

The package includes bridge **0.7.2**: exact mesh/joint validation from 0.7.1 and the working importer's `V = 1 - V` conversion on both UV sets. Eleven native-scene API fixture tests include the reported joint error and asymmetric/tiled UVs without mutating source data. Existing bridge 0.7.2 installations remain compatible.

## FBX scale, hierarchy and animation

The user identified `correct example.c4d` as the reference. Read-only examination of its embedded strings confirmed friendly joint names and identified the matching source character folder. The working Overwatch Entities importer is the transform/hierarchy reference. Source assets and Cinema project files were not modified or bundled.

The exporter now multiplies geometry, translations, bind matrices and animated translations by 100 and declares centimetres. It keeps entity/model/skeleton wrappers and bone attachments, uses the working plugin's friendly names, and omits artificial model/scale joints. Native FBX Rrs inheritance retains non-inherited joint scale. The 0.6.1 default-attribute and animation metadata fixes are retained.

- Autodesk's FBX SDK passed **700 synthetic comparisons** across Y/Z up, including nonuniform parent scales, nested component rigs, bind matrices, world poses and skinned vertices. Maximum error: **0.000048 cm**.
- The real Gameplay1P entity has **6 meshes, 18,415 vertices and 105 joints**. A 50-frame, 93-track clip passed **768 SDK comparisons**, including complete skinned meshes at five times. Maximum error: **0.0000063 cm**. All 105 exported joint names occur in the correct project audit.
- Native C4D payload preparation succeeded for **all 19 available 93-track clips**, with no unrepresentable local shear. This checks source parsing and sampled transforms, not native Cinema saving.
- Reports and reproducible development scripts are under `work/animation-research/fbx-qa-v070`, `fbx_v070_fixture.py` and `verify_fbx_v070_sdk.py`. The SDK and user-owned assets are development-only and are not shipped.

Blender 5.1's FBX importer fails on the preserved nested armature hierarchy and ignores FBX inheritance modes. The earlier exporter flattened hierarchy to work around that limitation. Version 0.7 prioritizes the requested original hierarchy; old Blender round-trip results from 0.6 do not validate this new layout. Autodesk's reader accepts and evaluates it. Direct Cinema 4D FBX import remains to be checked in the user's normal session.

## Native C4D and Octane

The optional companion runs on Cinema's main thread and builds an independent document. It creates native polygon meshes, joints, UV/normal tags, Weight tags, Skin deformers, per-source-frame PSR animation, centimetre units and Y up. It never inserts, activates, merges or saves the open editor document.

The initial Octane Universal material builder covers albedo, roughness, metallic, normal, alpha and supported emission, UV selection/repeats and relative `tex` references. **54 parameter IDs** agree with the locally installed Octane 1.8.1 and 1.9.5 resource headers. Eight material contract tests cover node types, links and settings. These are simulated API checks, not rendered comparisons.

Every native export saves and reopens its new scene before reporting success. It checks object hierarchy/order/names/types, polygon counts, joint links, material names/types/image paths, renderer selection, all baked key counts and sampled world transforms at first/middle/last frames. Nine scene contract tests cover successful and intentionally corrupted save/readback paths using an API fixture.

Queue tests exercise live/stale/offline discovery, texture transfer, isolated jobs and export failures. Shared publication keeps raw animation, FBX and C4D filenames aligned and prevents partial output if a requested format fails. Tests using fake native bytes are protocol/transaction checks only.

**Native runtime limitation:** installed c4dpy 2025/2026 exits with code 1 before executing scripts in this environment. Consequently no actual `.c4d` save, direct Cinema import or Octane render was verified here. The companion performs native save/readback checks during each user export, but visual material quality still needs real Cinema/Octane feedback. This is an initial integration, not a fully validated Octane shader reproduction.

Native PSR cannot represent sheared local transforms without extra hierarchy nodes. Such clips fail explicitly; FBX is no longer offered in the interface. Native sampling has a 250,000 bone-frame limit. The companion uses a private prototype plugin ID pending public registration.

## Application and desktop checks

All **170 Python regression tests pass**, covering source parsing, pose evaluation, friendly-name hierarchy, binary FBX, materials, texture baking, catalog/settings, queue protocol and transactional output. This includes successful C4D-only/combined API transactions with mocked native bytes and rollback after a simulated native failure.

The C4D interface test covers offline/connected/missing-Octane status, export selection and retry, persistence, captured options, and passive close without generating exports. The live FBX interface/backend test covers default track sorting (93/43/29), options, matching animation/FBX files, 14 texture PNGs and 19 unchanged source files. Both compact and larger layouts were inspected. C4D UI success replies in the interface-only test are mocked; the FBX UI test uses the real backend.

The rebuilt 0.7.0 Windows executable passed native picker IPC, two clip switches, scrubbing, failed-close recovery, focused Unicode nickname saving, relaunch persistence and no uncaught page errors. Report: `work/preview-build/desktop-regression-5aJEu6/report.json`.

Its separate FBX smoke passed with 8 textured meshes, two previewed clips, byte-identical OWANIMCLIP output, 14 PNGs, a 110,556-byte FBX and successful close-save. All 11 attributed mesh/joint nodes select attribute zero, and a named animation take exists. Report: `work/preview-build/whitehound-v07-fbx-smoke.json`.

Autodesk's SDK also inspected that executable's actual output: 8 meshes, 3 native skeleton nodes, 3 animated/moving joints, 192 vertices, 96 polygons and 121 keys per channel. Exact hierarchy and centimetre units agree with the source fixture, and every exported vertex equals its source coordinate multiplied by 100 with zero numeric error. Report: `work/preview-build/whitehound-v07-fbx-smoke-sdk.json`.

The release ZIP is integrity-checked, extracted into a new folder and tested again before delivery. The optional bridge folder is included with its three runtime modules and license. No Autodesk, Cinema 4D, Octane or Blender binaries are bundled.

Desktop checks use a **QA-only `--no-sandbox` flag** under the restricted development account because that account cannot start Chromium's normal GPU child. Production settings retain sandboxing, context isolation and disabled renderer Node integration. This flag is not a normal launch instruction; normal double-click startup outside that restricted account is not established by these checks.

## Retained preview scope

Prior regression baselines include 71 pose checks (including an actual Blender-sampled fixture), 304 viewer-math checks, 11 navigation checks and 98 material checks. GPU shader tests cover skinning, normal/PBR, opacity, secondary UVs, AO/emission and environment retention. These synthetic fixtures remain in source for repeat checks.

The preview and exports approximate common game materials. Complex skin/subsurface, cloth, hair, glass and shader-specific layers are omitted or simplified. Additive animation, constraints and game effects can require information outside one clip. See `owpreview/SOURCE_REFERENCE.md` for channel formulas and `owpreview/FBX_REFERENCE.md` for export conventions.

## Repeat checks

Run in the source folder with the development Python dependencies and Node.js:

```powershell
python -m unittest discover -s tests -p "test_*.py"
node tests/test_pose.mjs
node tests/test_viewer_math.mjs
node tests/test_navigation.mjs
node tests/test_materials.mjs
node tests/test_c4d_ui.cjs
node tests/test_desktop.cjs
```

Interface checks need Playwright and its Chromium test runtime. `PLAYWRIGHT_MODULE`, `PLAYWRIGHT_BROWSERS_PATH`, `WHITEHOUND_TEST_PYTHON` (or `OWIO_TEST_PYTHON` for older tests) and `WHITEHOUND_EXE` can select local development dependencies. They are not prerequisites for the portable application. Use isolated data directories and synthetic assets when rerunning failure fixtures.

For user validation, install the included bridge, restart Cinema with Octane, check the connection, load the reference entity/clip, commit a nickname with C4D checked and inspect the generated scene's hierarchy, scale, playback and materials. Keep the scene and its `tex` folder together.
