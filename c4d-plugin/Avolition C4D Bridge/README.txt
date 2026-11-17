Avolition C4D Bridge — 0.7.14

To repair an existing Stager rig, use this bridge with Avolition Stager 0.19.1.
Open Export Manager, select the original named setups, and choose Create Stager rig.
Review the destination project and click Repair rig. Refresh scene in Stager.
Repair uses one Cinema 4D Undo.

Animations use their source FPS (the highest assigned FPS in a combined setup).
Keys and Motion Clip ranges retain exact whole frames at 30/60 FPS.
Gameplay1P exports use the hero name for the top null and promote the two
clavicles below Joints after baking their inherited motion. Unused joints,
empty helpers and camera-only HUD meshes are omitted. Mixed-weight meshes and
necessary attachments are preserved. Skin binding and visible motion are checked
before and after cleanup. Other entity hierarchies are unchanged.

For Gameplay1P, each clavicle's local Y animation is offset by its own first
frame value. Both start at Y=0 while retaining every Y key and all vertical
motion relative to frame zero. The arms and attached weapon move with the rig;
there is no compensating height offset on Joints or a frozen transform.

Each animation setup becomes one Motion Source and one Motion Clip, named
exactly with its Avolition nickname, on Layer 0. Attached arms and weapon rigs
share their outer Skeleton's Motion System. Independent skeletons share a
Joints parent, with world transforms preserved. Position, rotation and parameter
animation move into the source. Scale CTracks are never created; first-frame
scale stays static. PLA, expression baking and preview pictures are disabled.
The saved scene is reopened and checked for source links, keys and joint poses.

Static maps use 24 FPS, current time 0, and project/preview ranges 0 to 1 F.
Cinema 4D requires a nonempty range; 0 to 0 F shifts Time Min to -1 F.

Large maps reuse identical Octane material graphs across object bindings.
Static scenes are evaluated once before saving and once after reopening for
verification. Avolition 0.8.18 shows background map export progress and allows
up to 30 minutes for a native map save after Cinema starts processing it.

Newly imported Actor skin weights use Cinema 4D's 16-bit weight precision while preserving a total of one per vertex. This lets Stager recognize the native linear skins on the full Widowmaker Actor during Refresh. Re-importing a registered rig keeps its existing Weight tags.

UPDATE FROM AN EARLIER BRIDGE
1. Save your projects and close Cinema 4D.
2. Move the old "Whitehound C4D Bridge" folder out of every Cinema 4D plugins
   folder or custom plugin search path. Keep any backup outside those paths;
   renaming an old folder inside plugins does not disable it.
3. If "Avolition C4D Bridge" is already installed, replace that entire folder
   with this new copy. Do not combine old and new files.
4. Copy this complete "Avolition C4D Bridge" folder to the plugins location
   and restart Cinema 4D with Octane enabled. Keep only one bridge installed.

The companion now uses Avolition's official name. Its private plugin ID,
internal whitehound_c4d package and local bridge storage stay compatible.
Earlier animation exports remain compatible. This update advertises skeletal
motion clips and 24 FPS map timing so Avolition can verify that the running
companion supports the requested export.

Static map scenes use compact shared geometry over the local bridge. Map
placements retain translation, rotation, signed/nonuniform scale,
entity hierarchy and actual socket attachments. Geometry becomes independent
native objects in Cinema. Lights, sounds, effects and gameplay logic are not
included. The UV conversion and joint/mesh validation fixes from 0.7.2 remain.

Re-export the animation to create a corrected .c4d scene. Existing scenes
are not modified. A reused nickname may receive a numbered suffix.

SETUP (once)
1. Save your projects and close Cinema 4D.
2. Copy this entire "Avolition C4D Bridge" folder into your Cinema 4D
   plugins folder, beside Overwatch Entities. Keep whitehound_c4d inside it.
3. Start Cinema 4D with Octane enabled. The bridge runs automatically.
4. Keep Cinema 4D open while exporting from Avolition.

EXPORT
Animations: assign clips to objects, name the scene and Add it to the export
list. Select entries in the export panel and export the selected scenes.
Maps: load a .owmap beside its exported Models/Entities folders, choose the
output directory and scene name, then use the Maps C4D export action.
The output folder receives the .c4d scene and its baked tex/ maps. Different
existing files are preserved by adding a collision suffix.

The native scene contains the entity importer's readable joint names,
entity/model/skeleton hierarchy, centimetre geometry, skin weights,
sampled position/rotation animation and editable Octane Universal materials. Static maps
contain no invented animation tracks. C4D uses Y up. Keep tex with the scene.

For file exports the bridge creates, verifies and closes a separate document. It does not
modify, save or replace the scene you are editing. Files in the output
folder are published only after the requested exports succeed.

Cinema 4D and the Octane plugin are required for C4D export. They are not
required for preview. No Blender
installation is needed. This bridge uses no network service.

This is an initial Octane setup for feedback, not a complete reproduction
of every proprietary game shader. Unsupported emission masks are reported.
Zero-scale or sheared native local transforms fail explicitly to preserve the
hierarchy and transforms.

CREATE STAGER RIG
Select named animation setups in Avolition and choose Create Stager rig.
Review the document, camera and assignments before inserting. The bridge
builds a detached candidate then inserts and registers it in a single Undo.
Its baked textures live under %LOCALAPPDATA%/Avolition/Stager/textures.
Use Save Project with Assets when transferring the project to another machine.

Private prototype plugin ID: 1066202606. Source license: GPL-3.0-or-later.
Cinema 4D and Octane binaries are not included.
