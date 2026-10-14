# Avolition 0.8.22 — Export Manager


Avolition, previously Whitehound, is a standalone Windows application for exporting and browsing Overwatch models, maps and animations. Load an entity or model once, click clips to preview them, and collect named setups in a persistent export list. Open exported maps in their own workspace and export static scenes to Cinema 4D.

Version 0.8.22 moves the export control to a blue **Export Manager** button between **Clip details** and **Preview objects** in the Animations sidebar. It opens the existing export list and settings. The sliders icon beside Animation setup is removed.

Version 0.8.21 adds a sort dropdown beside **Entities and models** in **Browse models**. Choose **Gameplay first**, **Most joints first**, **Fewest joints first**, **Largest files first** or **Smallest files first**. Sorting applies within each hero and skin, retains your selected preview, and stays selected when reopening the browser during the session. Joint counts include attachments. File size means the source `.owentity` or `.owmdl` file, excluding referenced models and textures; hover its Entity/Model badge for the exact byte count. Unavailable values sort last in both directions.

Version 0.8.20 shows a bone count beside each entity and model in **Browse models**, available after scanning without opening every 3D preview. Entity counts include their attached models, matching the preview's total. Unreadable or incomplete counts display a dash with an explanation on hover. The redundant Open entity or model file button beside Load is removed.

Version 0.8.19 adds **Browse models** beside the fast Entity / Model picker. Filter by hero and skin, search filenames or paths, and select a row to inspect it in an interactive 3D preview. Gameplay1P and Gameplay3P appear first. The browser shows model, mesh and bone counts to help identify separate animated attachments. Choose **Load into Animations** when ready; browsing leaves the current entity, assigned clips and object names intact. Only the selected asset is decoded, with reduced preview texture resolution. Loading into Animations uses the normal texture quality.

Version 0.8.18 runs map export as a background job with preparation, material baking and native-save progress. Long exports keep the interface responsive and retain their result instead of hitting the desktop's three-minute request timeout. Cinema has up to 30 minutes after starting a native map save; an unclaimed request still has a short, separate wait. Identical Octane materials are shared by the included Avolition C4D Bridge 0.7.5. Export saves a `.c4d` file and its `tex` folder to the chosen output directory; open that saved file in Cinema 4D.

Maps no longer offers Include animations in Toolchain. The Animations tab keeps that option. Framing a map clears remaining orbit/pan/zoom motion so the camera stays at the framed view, with Y up and free orbit preserved.

Version 0.8.17 removes Include sounds from both Toolchain export tabs. Sound extraction stays off. All levels of detail now includes the note: "Unchecked exports the highest available detail".

Version 0.8.16 fixes C4D export stopping on zero-length vertex normals, including Esperança maps. Like Blender, these normals are generated from the surrounding mesh geometry; valid authored directions, split vertices, topology and source files are preserved. The same correction applies to animation exports containing meshes. Existing Avolition C4D Bridge 0.7.4 installations need no update.

Version 0.8.15 keeps map dependencies out of the Animations library. Scans recognize exported `.owmap` folders and skip their Models, Entities and other asset branches, including when scanning directly inside them. Renamed map export folders are recognized; unrelated character folders remain available. The redundant Open Output Folder button beside Export from Overwatch is removed.

Version 0.8.14 included Avolition C4D Bridge 0.7.4. Map-export errors now report the connected bridge version, and Avolition prefers a map-capable Octane instance when multiple Cinema 4D sessions are open. The redundant Maps header and file-picker row have been removed; choose maps through Library source and the map tree.

Version 0.8.13 improved map material coverage, close-up navigation and variant browsing. The map library now groups maps and their exported variant folders in an expandable tree. Preview texture resolution adapts to the whole map instead of stopping after the first 128 textures. Engine collision surfaces are omitted using the Blender importer's exact material list. C4D texture baking retains source resolution.

**Maps**, **Animations** and **Toolchain** share a compact dark theme. **Preview objects** is a permanent hierarchy in both preview workspaces. Double-click a scene, model or mesh name to save a display name used by exports. Joint names remain unchanged.

## Find animations for the selected object

**For selected object** in the Animation library uses the loaded entity's actual objects and skeletons. It stays synchronized with **Choose object** above the preview. Counts are read from each model, with no per-character count table. Root Gameplay1P/Gameplay3P objects receive a descriptive arms/body prefix; attached objects retain their source names rather than being classified by their bone counts.

The default **For selected object** sort prioritizes clips from the selected model's animation folder, including identical copies elsewhere, then ranks actual joint-ID overlap and unmatched tracks. Folder context is scoped to the same export/hero/skin path, so another character with shared joint names does not receive that folder priority. This is a browsing aid: all clips remain available and no perspective or compatibility restriction is applied.

Each row shows a small coverage bar and **matched bones / selected skeleton bones**, separately from total clip tracks. For example, a 112-track clip matching 108 joints of a 190-bone rig shows **108 / 190 bones covered**. Partial animations remain usable. Coverage does not establish an animation's action, pose quality, or how many joints visibly move.

Search and sorting share one compact control. **For selected object** is the default; **Most tracks first** and **Nicknamed first** remain prominent. **Name A–Z**, size, duration and ascending-track sorts are under **More sorting options**. Track-count groups and their filter have been removed. On the first upgrade, an older Most tracks first preference moves to For selected object; subsequent choices and Hide duplicates persist.

Bone names are read automatically in small background batches after loading an object and cached in memory. Unavailable metadata is shown as unavailable, not a zero match. Filtering and sorting preserve assignments and playback.

## Combine arm and weapon animations

The **Animation setup** panel keeps one clip assigned to each object and exports the complete setup to Cinema 4D. For a first-person character such as Widowmaker:

1. Open **Animations**, scan the **Library source**, and load `Gameplay1P.owentity`.
2. Use **Choose object** to select the arms, then click the arm animation in the library.
3. Select the gun object and browse its animations. The arms keep their assigned clip while you try gun clips.
4. Check the assignment rows beneath the selector. Click an object's row button to change its clip, or **Clear** to remove only that assignment. Preview and scrub to compare the motions together.
5. Enter a nickname in the naming field below the assignments, then click **Add to export** (or press Enter). A bare `reload` becomes `Gameplay1P_reload`; a full `Gameplay1P_reload` name is accepted. This saves a snapshot of the entity path, name, assigned clip paths and current object names without exporting files.
6. Open **Export Manager** between Clip details and Preview objects. Choose the output folder, check the entries you want, then **Export selected**. Every new or restored entry starts unchecked. The list stays available even before loading a new entity. Keep Cinema 4D open with Octane and the Avolition bridge enabled for the actual export.

All assigned clips start at zero on a shared timeline that spans the longest clip and uses the highest assigned frame rate. Each source clip is sampled at its original rate; shorter clips hold their final pose. Switching the selected object does not change this shared duration. Assignments last while the entity is loaded; loading another entity starts a fresh setup.

The combined export preserves the loaded entity hierarchy, including attached objects, and applies each clip only to its chosen rig. Objects without an assigned clip keep their rest pose and follow their attachments. Every entry exports the whole loaded entity, even when only one animation was assigned. Include mesh, Bake textures and Keep root motion live in the export menu. Preview visibility and In-place do not change the exported scene.

You choose the pair by previewing it. Avolition does not infer action names, automatically pair clips, or guess the purpose of unmatched tracks. The displayed tracks count is exported **bone records**, not transform channels or total skeleton bones. An 85/93 or 18/19 match remains usable; the unmatched count alone does not identify a missing mesh or world-position track.

The experimental analysis controls and evidence inspector are removed from the interface. The read-only research utility remains in source: `python tools/analyze_library.py --root "C:\\Your export" --output "C:\\Research\\animation-index.json"`. Keep its output outside the export. See [source investigation](owpreview/ANIMATION_SOURCE_REFERENCE.md) for its evidence and limitations.

## Browse and export maps

1. Open the top-level **Maps** tab. Choose the common parent of your exported map files as its **Library source**, then **Scan folder**. The map source is separate from the animation library source.
2. Expand a map, then its exported build/variant folder, and click a `.owmap` variant to load it. For example, **King's Row (Winter) → 6B1 → Evening - Hybrid**. Names use natural alphabetical order. Search reveals matching variants across folders; keyboard arrows navigate and expand the tree, and Enter loads a variant. Keep referenced models, entities, materials and textures in DataTool's original folder structure.
3. Inspect the map using the same pan, zoom, orbit and frame controls as Animations. The right-side **Preview objects** tree shows the scene and its model/mesh hierarchy. Toggle visibility, or double-click scene, model and mesh names to rename them for export.
4. Set **Export name** and **Output directory**, choose **Bake textures**, then **Export C4D**. Keep Cinema 4D open with Octane and the bundled **Avolition C4D Bridge 0.7.4**. **Open output folder** opens the destination after export.

Maps retain their own library source, output directory and Bake textures preference across restarts. The default source is the existing application library; the default map output is its `Map exports` subfolder unless a common C4D output directory was already saved. Switching between Maps and Animations retains both loaded scenes and their selections. Export continues when switching tabs.

Repeated map geometry and matching materials are shared internally while each placement keeps its source transform, appearance and editable names. Large Preview objects trees create child rows when their parent is expanded. Static scenes stop submitting new render frames after the camera settles. Large maps still require many draw calls while navigating; performance depends on map size and graphics hardware. The map exporter preserves the placement hierarchy in the C4D scene. Visibility is a preview aid: the exported map includes its loaded geometry. A changed exported map can leave obsolete saved name keys; these are ignored when they no longer identify a loaded object.

Map geometry remains in its source metre units; native C4D export applies the existing metre-to-centimetre conversion. Navigation now permits approaching individual map details, with map-specific clipping distances and grid spacing. For large maps where distant backdrop objects dominate the bounds, the starting view and **F** focus on the main concentration of map objects. **Shift+F**, or Shift-click the frame button, includes all visible scenery. All geometry remains loaded and available for export. The viewport correction does not rescale source assets or multiply exported scenes again.

Map previews distribute a bounded texture budget across the whole scene, using smaller images when many texture maps are present. **Bake textures** uses the original source resolutions for Cinema 4D/Octane. Material channels follow the inspected Blender importer where supported: base color, tangent-space normals, roughness/metalness, alpha, AO and supported emission. Exact collision material GUIDs remove invisible engine hit-test meshes from preview and export; ordinary untextured geometry remains. Export baking reuses repeated material appearances.

This workflow supports static map geometry, model/entity attachments and common material maps. It does not recreate map lights, sounds, effects or game behavior. Layered game shaders remain common-PBR approximations. Unsupported or omitted content is reported in **Map notes**; missing source bindings are not replaced by guessed textures. Map parsing and baking also have read-only coverage against an exported King's Row (Winter) variant. Live game extraction and native Cinema 4D/Octane saving remain unverified in this environment. See [validation](VALIDATION.md) for completed checks.

The packaged renderer was checked against **King's Row (Winter), Evening - Hybrid**: all 6,150 meshes with available UV-compatible texture mappings received textures, with no preview-budget fallback. Source material bindings and UV channels still limit what can be displayed. This does not establish exact in-game lighting or every Blender shader effect.

## Export from Overwatch

The upper-left **Animations**, **Maps** and **Toolchain** tabs preserve the previews, selections and running jobs. **Overwatch Install** and the extraction **Output Directory** appear only on Toolchain. Animations shows its library, object assignments, timeline, visibility controls and C4D export list. Maps has a separate map library, preview and static C4D export controls.

1. Open **Toolchain** and set **Overwatch Install** to a local game installation. An exported asset folder is not a game installation.
2. **Output Directory** defaults to the existing portable `library` folder beside `Avolition.exe`. A configured `libraryDirectory`/`libraryPath` is respected. Custom output selections and the active tab are saved across restarts; **Use Library** restores the default. Source launches resolve these folders beside the source `app.py`, never the process working directory.
3. Press **Install**. Avolition downloads the exact `toolchain-release.zip` asset from the [official OWLib release](https://github.com/overtools/OWLib/releases), verifies its upstream digest when supplied, and extracts the entire runtime. Internet is required for installation/update checks; existing previews and installed Toolchain remain usable offline.
4. Use Toolchain's inner **Animations** tab and **Load heroes** for models and animations. Select a hero, category and asset from DataTool's real catalog. Models + gameplay animations uses skin extraction; emotes, victory poses and highlight intros use their respective unlock filters. Alternatively, use Toolchain's inner **Maps** tab and **Load maps**, then select a listed map variant or **All maps**. Map extraction uses DataTool's `list-maps` catalog and `extract-maps` mode, not a hero/unlock filter.
5. Configure the supported texture, animation, sound and LOD options, then **Export**. PNG and `.owanimclip` are requested explicitly, preserving DataTool's output structure. **Cancel** stops the process tree; partial output can remain. Logs expand below the export controls. Both inner workflows use the same job controls and keep their selections when switching.
6. Exports to the library refresh its indexes. Loaded previews retain their current content. Open **Animations** for supported `.owentity`/`.owmdl` files, or **Maps** for `.owmap` scenes. Each workspace's C4D destination is separate from DataTool's extraction destination.

The complete downloaded ZIP, dependencies, and immutable installations live beneath `toolchain` beside Avolition.exe. `toolchain/active.json` atomically selects the validated installation; a failed download/update keeps the prior copy. No update runs while DataTool is active. **Check for Updates** only checks; **Update** explicitly installs a different package, including a replacement asset under the same release tag. Use a writable portable app folder.

Installed version, release date (`release.published_at`), package last updated (`asset.updated_at`) and local installation time are stored separately and displayed in UTC. Asset update time is GitHub's package timestamp, not a guaranteed code-change date. Available-release metadata never overwrites the installed package's displayed metadata. An older manually unpacked runtime can be used, but unverifiable version/dates show Unknown.

Shared settings live in `%LOCALAPPDATA%\Avolition\catalog.json` under `workflow`; first-launch migration retains the existing scan root, annotations, export list and output settings. Toolchain reads the game installation with `--online=false`, disabling upstream game-file repair/download behavior. Output inside the game or Toolchain runtime is rejected. Avolition starts DataTool with an argument list and no shell, preserving spaces, Unicode and literal query pipes.

Command mappings were checked against the [official usage documentation](https://github.com/overtools/OWLib#how-to-use), released executable help, and matching release source. [MAP_SOURCE_REFERENCE.md](owpreview/MAP_SOURCE_REFERENCE.md) records the map catalog/query contract. The extraction backend is OWLib, not a reimplementation. Game extraction still depends on Toolchain supporting the installed game build. No Overwatch installation is available in this development environment, so real hero and map extraction have not been run here.

This version forks the shipped **0.8.4** source. It does not include 0.9.0's automatic pairing, saved-pair system or Gameplay1P/Gameplay3P restrictions. You choose which object receives each animation.

**Hide duplicates** starts ON beneath Animation library. Repeated byte-identical `.owanimclip` files across skins appear once. Uncheck it to see every source path. Different file contents remain separate even if filenames, track counts and sizes match. Original files and their existing nicknames are retained; nothing is deleted or merged. Search checks all source copies before grouping, so a nickname on another copy can still be found.

Use **Choose object** above the viewport, or select a model in the permanent **Preview objects** tree on the right. Clicking a library clip assigns it only to that object's rig. **Clear animation** clears only the selected assignment. Visibility checkboxes remain independent. See the combined-animation steps above to preview and export several assigned clips together.

The old right-side Nickname box, raw `.owanimclip` copy option and top Clip copies field are removed. Adding a setup saves a searchable nickname for the active clip and an independent named export entry. Existing annotations and previously exported files remain untouched. Browsing, blurring the name field, changing settings and closing do not implicitly export.

The mesh visibility controls introduced in 0.8.4 now live in **Preview objects**. Hidden objects and **In-place** affect the preview only. C4D export still uses the original asset and the separate **Keep root motion** setting. FBX controls are removed for now; a saved FBX preference cannot trigger a hidden export.

Version 0.8.3 starts Skeleton and In-place OFF, adds **Nicknamed first** sorting, and normalizes C4D mesh normals to match Blender while retaining the Normal tags. Existing bridge 0.7.2 installations receive this correction from the updated app; no bridge reinstall is needed. Re-export to apply it to a new scene. Visual C4D/Octane confirmation is still needed.

Version 0.8.2 doubles zoom sensitivity from 0.8.1 for horizontal drag and the mouse wheel.

Version 0.8.1 removes the favorites controls/filter and adds a **polygon count at the viewport's top right**. The count includes loaded child meshes and excludes wireframe, skeleton and grid overlays. Overwatch meshes are triangulated, so each triangle counts as one polygon.

## Start Avolition

1. Extract the **entire ZIP** into a folder of your choice.
2. Open **Avolition.exe**.
3. Keep the executable, `resources` folder, and all accompanying files together. This is a portable application folder, not a single-file executable.

The portable release includes its Electron desktop runtime, Python backend, and preview libraries. Preview and organizing the export list do not require a separate browser, Cinema 4D, Blender, Python, or WebView2 installation. Optional native C4D export requires Cinema 4D with Octane and the included companion plugin. Use 64-bit Windows with graphics support for WebGL2.

Your exported files stay on your computer. Preview and file operations run locally; Toolchain installation/update checks contact GitHub without uploading your assets.

## Browse your exports

1. Open **Animations**, enter the full path to your exported Overwatch folder in **Library source**, or click **Browse** to choose it. Select the common parent containing your `Entities`, `Models`, and animation folders so entity references can resolve.
2. Click **Scan folder**. Avolition searches subfolders for `.owentity`, `.owmdl`, and `.owanimclip` files.
3. Select an entity or model and click **Load**. Referenced `.owmat` materials and PNG textures inside the export root load automatically. The open-file button beside the model selector also lets you choose a file directly.
4. Use **Choose object** to select the rig to animate, then click an animation in the library. Switch clips without reopening the entity; other objects keep their assigned clips.
5. Use the timeline, frame number, playback controls, loop, and speed selector to inspect motion. Search by filename or saved nickname; start with **For selected object**, or switch to the other sorting options. Count alone does not identify a first-person animation.
6. Name the current setup and **Add to export** when you recognize a useful animation.

The native folder and file pickers are part of the desktop application. The path fields also accept pasted paths.

## Materials and textures

Keep the exported `.owmat` files and texture folders with the entity and model files. Choose their common parent as **Library source**. Avolition uses material references and texture binding IDs to assign textures to the correct meshes; there is no separate material import step.

**Textures** starts enabled in the Viewport settings. Switch it off for neutral shading while inspecting motion. Missing or unsupported material data uses neutral or partial shading and is reported without preventing the mesh from loading.

The preview supports common base-color maps, Overwatch packed normals, roughness and metalness, ambient occlusion, supported emissive masks/colors, and explicit cutout or alpha maps. Skin, detail layers, cloth, hair and glass use a simplified preview; the result is not a one-to-one reproduction of Blender's node groups or the game's rendering. The implementation references and verified channel formulas are in [SOURCE_REFERENCE.md](owpreview/SOURCE_REFERENCE.md).

PNG is recommended. Pillow also decodes supported DDS, TGA, TIFF, JPEG, BMP and WebP images; unsupported encodings fall back to shading. Preview images are reduced to a maximum edge of **2048 pixels**. Source images are limited to **64 megapixels** and **128 MiB per file**. Conversion and resizing affect only the preview: original textures and `.owmat` files are never modified. Blender is not required.

## Select and export named setups

The blue **Export Manager** button between Clip details and Preview objects opens the export list. The common output directory is remembered across library scans and restarts, reusing a saved destination when available. Paste a full path or Browse. A new folder is created only when an export is written.

The list stores source paths, assignments and a snapshot of object names, not copies of your assets. Browsing, renaming objects or loading another entity cannot modify an added setup. Add a new entry to capture later name changes. Older entries without saved object names retain their original source names. Keep source files accessible until export. Entries are saved in the local catalog; checkbox selections and result messages are session-only. Successful entries become unchecked, while failed entries remain selected for retry. Remove deletes only the list entry, never an existing exported file.

Exports run one at a time with the output directory and settings captured for that batch. Each entry has Ready, Exporting, Completed or Failed status. Failures display their reason; later selected entries still run. Closing the export menu does not interrupt an active batch, and close the application after the batch finishes. An existing output filename is preserved and a differing export receives a numbered suffix.

Output folders created by Avolition and tracked exports are excluded from the source library scan. If you choose an existing folder containing original assets, those originals stay visible; only tracked outputs are excluded. Source animation files are never renamed or modified.

## Scene export options

Open **Export Manager** and expand **Export settings**. Escape closes the menu. These settings apply to C4D export, independently of the viewport:

- **Bake textures:** saves supported material maps as ordinary PNGs in `tex`, including conversion of the game's packed channels. It does not bake lighting or every proprietary shader effect. Requires Include mesh.
- **Include mesh:** includes geometry and skin weights with the animated skeleton. Off exports the skeleton and animation only.
- **Keep root motion:** preserves the animation's horizontal root movement. Off holds horizontal root position at rest while keeping vertical movement and rotation. The preview's In-place checkbox does not change this setting or the exported animation.

**Choose object** selects the rig that receives the next library clip. Other objects keep their assigned animation or rest pose and follow their attachments. Queued exports include the loaded entity and all saved assignments, preserving original joint names independently of preview visibility.

FBX export is paused in the interface. Its implementation remains in the reviewable source for later work.

## Export native C4D with Octane

1. Close Cinema 4D. If an older **Whitehound C4D Bridge** is installed, move its entire folder outside every Cinema 4D plugin search path. Copy **Cinema 4D/Avolition C4D Bridge** from the extracted download into Cinema 4D's `plugins` folder, including `whitehound_c4d`. Keep only one bridge copy installed. It can sit beside the Overwatch Entities plugin.
2. Restart Cinema 4D with Octane installed and enabled. Keep Cinema 4D open.
3. In Avolition, open the export menu and expand **Export settings** and click **Check connection**.
4. Load an entity and assign one or more clips in **Animation setup**, enter its nickname, then **Add to export**. Open the export menu, choose the output directory, check the desired entries and click **Export selected**.

```text
Your output folder/
  Gameplay1P_reload.c4d
  Gameplay3P_rest.c4d
  tex/
    ...texture PNGs...
```

The bundled **Avolition C4D Bridge 0.7.4** supports compact map instances, retains the existing joint/mesh type checks and automatically flips V on both C4D UV sets. A running 0.7.2 bridge supports animations but cannot export maps; installing new files requires a Cinema 4D restart before the new capabilities become available. The registered plugin name and entry file are now Avolition. The plugin ID, internal `whitehound_c4d` package and `%LOCALAPPDATA%\Whitehound\c4d-bridge` mailbox stay compatible with existing installations. Isolated setups can override the mailbox with `AVOLITION_C4D_QUEUE` or the older `WHITEHOUND_C4D_QUEUE` variable.

The bridge creates a separate document, with native polygon meshes, joints, weight tags, Skin deformers and baked PSR animation. It uses Y up and centimetres, matching the working importer. Include mesh, Bake textures and Keep root motion control this scene export. The open editor scene is left untouched. Each new C4D file is reopened and checked for hierarchy, native object types, skin links, materials and sampled animation before Avolition publishes it. Each entry uses its saved entity_nickname. A failed export publishes none of that job's files.

The initial Octane setup uses editable Universal materials with common albedo, roughness, metallic, normal, opacity and supported emission connections. Packed Overwatch channels are converted to PNG maps in `tex`. Unsupported game-specific layers are approximated or reported. This is an initial material setup for visual feedback, not a complete recreation of every game shader. Keep the scene beside its `tex` folder.

The native exporter rejects clips whose nonuniform animated scales require sheared local joint transforms, because ordinary C4D PSR cannot represent them without changing the hierarchy. Native export is limited to 250,000 sampled bone frames per job. The companion is a private prototype; its plugin ID must be registered before broad public distribution.

The companion was checked against installed Cinema 4D/Octane resources and automated API fixtures, but actual native saving and Octane rendering could not run in this development environment. This release does not claim a verified combined animation or map C4D save. Map tests use synthetic OWLib-format records, real model/material parsing, shared-instance transforms and a mocked native writer for publication tests. Earlier checks prepared the supplied character's 19 clips with 93 tracks successfully; payload preparation is separate from saving or rendering a Cinema scene. See `VALIDATION.md` for the exact verification limits.

## Viewport controls

| Control | Action |
| --- | --- |
| Hold **1** and drag | Pan |
| **Alt + right mouse button**, drag horizontally | Zoom; right moves in, left moves out |
| Hold **2** and drag horizontally | Zoom |
| Hold **3** and drag | Orbit |
| Mouse wheel | Zoom |
| **F** | Frame visible meshes |
| **Space** | Play or pause |
| **Left / Right** | Step one animation frame |
| **Up / Down** | Previous or next clip |

These are Cinema 4D-style navigation shortcuts. Use **Textures**, **Grid**, **Skeleton**, **Wireframe**, and **In-place** to examine motion. In-place holds the root's horizontal position while preserving vertical motion, in the preview only. If an entity contains several rigs, use **Choose object** to choose which receives the next clip.

**Preview objects** stays in the right panel. The loaded entity is the tree root, with attached models nested under their actual parent models. Each model lists its meshes and a **Skeleton** branch; joints nest according to the skeleton's parent indices. Skeleton-only models remain visible and selectable. Missing or invalid parent links appear at the root rather than being inferred from a model name or bone count.

Use **+ / −** to expand or collapse a branch. Skeleton and joint branches start collapsed for gradual exploration; entity and model branches start expanded. Click a model name, or press **Enter** on it, to select its animation target. Clicking a mesh name selects its owning model. Visibility checkboxes affect only the preview; hiding a model's own meshes does not hide its attached children. **Show all / Hide all** affects every loaded mesh.

In either workspace, double-click a scene, model or mesh label to edit it inline. Press **Enter** or leave the field to save; **Escape** cancels. Names are stored locally for that asset and applied to new exports. Renaming never changes source filenames, object IDs, joints, attachments or animation matching. Names in the object tree are display labels; **Export name** and the animation setup nickname still control the output filename. A repeated map mesh can have a different name for each placement without changing its shared geometry.

With focus in the tree, **Up / Down** moves between visible rows; **Home / End** moves to the first or last row. **Right** expands a branch or moves to its first child; **Left** collapses it or moves to its parent. **Space** toggles visibility for the scene, model or mesh row without selecting another animation target. These keys operate the tree while it has focus, without scrubbing or starting playback.

Expansion survives tab switches and animation changes. Visibility remains while switching clips and resets when loading an entity again. With objects hidden, the count shows visible polygons / total polygons. Hidden parts still retain their animation and attachments, and are included in exports.

Textures and Grid start enabled. Skeleton, In-place and Wireframe start disabled. Wireframe overlays thin black edges on the shaded animated surface. Overwatch exports contain triangles, so their triangulation remains visible. Zoom sensitivity is twice that of version 0.8.1; Alt + right-drag and hold-2 drag still use horizontal movement.

## Saved labels and settings

Named export entries, nicknames, object display names, each workflow's directory settings, and the selected workspace/Toolchain tabs are saved in:

```text
%LOCALAPPDATA%\Avolition\catalog.json
```

On the first normal launch, if this file does not exist, Avolition copies the first available catalog from `%LOCALAPPDATA%\Whitehound\catalog.json`, then `%LOCALAPPDATA%\OWIO\catalog.json`, then `%LOCALAPPDATA%\OWAnimationBrowser\catalog.json`. The source catalog is left untouched. An existing Avolition catalog is never replaced by this migration. The copied catalog retains names, notes, export entries, directory choices and other settings; existing format-preference migrations still apply. Notes remain stored although the interface no longer shows them.

Back up `catalog.json` to preserve your annotations and export list. Labels are associated with full file paths, so moving an export folder changes their lookup paths. Electron's desktop cache and logs are stored separately under `%LOCALAPPDATA%\Avolition\desktop`; the current log is `avolition.log`.

For a separate test catalog, launch with `--data-dir "C:\path\to\test-data"` or set `AVOLITION_DATA_DIR`. An explicit data directory disables importing the old catalog and isolates the C4D bridge queue beneath that profile. `WHITEHOUND_DATA_DIR`, `OWIO_DATA_DIR` and `OWPREVIEW_DATA_DIR` remain supported aliases, in that order after `AVOLITION_DATA_DIR`. `WHITEHOUND_C4D_QUEUE` remains the normal-launch bridge queue override.

## Preview scope

- Common material textures load automatically; unsupported shader effects use a simplified or neutral preview.
- Avolition previews exported model, entity, skeletal animation and static map files. The Toolchain tab uses OWLib DataTool for game extraction. Native Cinema 4D export uses the optional companion described above; maps need bridge 0.7.3 or later.
- Map lights, sounds, effects and animated game behavior are outside the static map preview/export scope, even when DataTool includes their source data.
- A clip does not contain a reliable action name or its intended character. Bone-name matches help choose a rig but cannot guarantee that a clip is suitable for it.
- Skeletal playback follows the Blender importer's transform behavior. Additive layers, game animation blending, constraints, and effects may require information outside a single clip and may not appear as they do in the game.
- Missing entity references are reported. Keep the original exported folder structure together when possible.
- The included example robot is synthetic test geometry, not an Overwatch asset. See `VALIDATION.md` for the tested scenarios and remaining verification limits.

## Source development

The source distribution includes the Python backend, the Electron shell in `desktop`, the viewer in `web`, and regression fixtures in `tests`. The portable application already bundles these runtimes; the following development tools are needed only when running or building from source.

The desktop shell uses Electron **44.4.3** and the backend uses **Pillow 12.3.0** to decode textures and **NumPy 2.3.5** for export transform calculations. Both are included in the portable application. Once the backend has been built, its source shell can be started with Node.js/npm:

```powershell
$env:AVOLITION_BACKEND_PATH = "C:\path\to\owio-core.exe"
Set-Location desktop
npm install
npm start
```

The shell uses the source tree's adjacent `web` folder during development. The packaged layout places the shell in `resources/app`, web assets in `resources/web`, and `owio-core.exe` with its `_internal` folder in `resources/backend`. `AVOLITION_BACKEND_PATH` is preferred; `WHITEHOUND_BACKEND_PATH` and `OWIO_BACKEND_PATH` remain fallback aliases. The internal `whitehound://app` protocol is unchanged for compatibility.

To build the portable Windows folder, use a dedicated 64-bit Python environment and the official Electron 44.4.3 Windows x64 archive, `electron-v44.4.3-win32-x64.zip`. Put the release's `SHASUMS256.txt` beside that archive; the builder verifies its checksum. Use a fresh build-output directory:

```powershell
python -m venv .venv
.\.venv\Scripts\python.exe -m pip install -r requirements-build.txt
.\build.ps1 `
  -Python .\.venv\Scripts\python.exe `
  -ElectronArchive "C:\downloads\electron-v44.4.3-win32-x64.zip" `
  -BuildRoot ".\build-output"
```

This produces `build-output\dist\Avolition\Avolition.exe`. Distribute the entire `Avolition` folder, including its support files and license notices. The build script uses the supplied Electron archive and installed Python dependencies; it does not install missing build dependencies automatically.

The script also accepts `-DependencyPath "C:\path\to\python-libs"` for an isolated Python dependency directory. Add `-CheckOnly` to validate build inputs and dependencies without producing an executable. Use a dedicated build environment to avoid collecting unrelated optional packages.

## Development checks

Run from the source folder with the build environment's Python and Node.js:

```powershell
python -m unittest discover -s tests -p "test_*.py"
node tests/test_pose.mjs
node tests/test_viewer_math.mjs
node tests/test_navigation.mjs
node tests/test_materials.mjs
node tests/test_object_hierarchy.mjs
```

The pose test includes sampled matrices produced by an actual Blender 5.1 animation. Blender is not required to run those recorded fixture checks. Python and Node.js are not separate prerequisites for the packaged application.

Texture regression fixtures are synthetic. Earlier releases checked the real Gameplay1P entity and animation with Autodesk's FBX reader; see `VALIDATION.md` for the distinction between parser, renderer, desktop and Cinema 4D checks.

The Electron host's automated check accepts `--smoke-test`, `--root`, `--screenshot`, and `--data-dir`. See `VALIDATION.md` for the invocation and the status of the final desktop-package checks.

## License

Application source is provided under GPL-3.0-or-later. Keep `LICENSE`, `THIRD_PARTY_NOTICES.md`, and the bundled dependency notices with redistributed copies. This is an independent preview tool; Overwatch game assets and trademarks belong to their respective owners and are not included.

