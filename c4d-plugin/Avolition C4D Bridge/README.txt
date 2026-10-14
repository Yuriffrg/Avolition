Avolition C4D Bridge — 0.7.5

Large maps reuse identical Octane material graphs across object bindings.
Static scenes are evaluated once before saving and once after reopening for
verification. Avolition 0.8.18 shows background map export progress and allows
up to 30 minutes for a native map save after Cinema starts processing it.

UPDATE FROM AN EARLIER BRIDGE
1. Close Cinema 4D.
2. Move the old "Whitehound C4D Bridge" folder out of every Cinema 4D plugins
   folder or custom plugin search path. Keep any backup outside those paths;
   renaming an old folder inside plugins does not disable it.
3. If "Avolition C4D Bridge" is already installed, replace that entire folder
   with this new copy. Do not combine old and new files.
4. Copy this complete "Avolition C4D Bridge" folder to the plugins location
   and restart Cinema 4D with Octane enabled. Keep only one bridge installed.

The companion now uses Avolition's official name. Its private plugin ID,
internal whitehound_c4d package and local bridge storage stay compatible.
Earlier animation exports remain compatible. This update advertises map
export support to Avolition so the map export availability check succeeds.

Static map scenes use compact shared geometry over the local bridge. Map
placements retain translation, rotation, signed/nonuniform scale,
entity hierarchy and actual socket attachments. Geometry becomes independent
native objects in Cinema. Lights, sounds, effects and gameplay logic are not
included. The UV conversion and joint/mesh validation fixes from 0.7.2 remain.

Re-export the animation to create a corrected .c4d scene. Existing scenes
are not modified. A reused nickname may receive a numbered suffix.

SETUP (once)
1. Close Cinema 4D.
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
sampled PSR animation and editable Octane Universal materials. Static maps
contain no invented animation tracks. C4D uses Y up. Keep tex with the scene.

The bridge creates, verifies and closes a separate document. It does not
modify, save or replace the scene you are editing. Files in the output
folder are published only after the requested exports succeed.

Cinema 4D and the Octane plugin are required for C4D export. They are not
required for preview. No Blender
installation is needed. This bridge uses no network service.

This is an initial Octane setup for feedback, not a complete reproduction
of every proprietary game shader. Unsupported emission masks are reported.
Zero-scale or sheared native local transforms fail explicitly to preserve the
hierarchy and transforms.

Private prototype plugin ID: 1066202606. Source license: GPL-3.0-or-later.
Cinema 4D and Octane binaries are not included.
