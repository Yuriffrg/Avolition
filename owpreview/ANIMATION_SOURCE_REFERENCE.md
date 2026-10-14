# Animation identity: verified OWLib source

Reviewed 2026-10-05. This reference explains evidence the analyzer can preserve; it does not assign gameplay names from bone counts, folder numbers, or hashes.

## Version and scope

The inspected installation's `toolchain/active.json` records `v2.24.1.0+1149`, release ID `389444677`, asset ID `566418418`, and verified package digest `sha256:64042bddf43ad1b772e8a9660ca9af070ed42df4447378933a711cb4c840482c`. Its local installation time is `2026-10-05T02:32:57.931844Z`. The [official release](https://github.com/overtools/OWLib/releases/tag/v2.24.1.0%2B1149) points to [commit df522f0ceea8748dcb51463ceb990234ceb45c9a](https://github.com/overtools/OWLib/commit/df522f0ceea8748dcb51463ceb990234ceb45c9a). References below are pinned to that commit, rather than today's development branch.

**This identifies the installed analysis/extraction tool, not the producer of an older export.** Neither its release date nor the game version supported by that release establishes the build that produced the selected assets. An export without a producer manifest retains unknown game/export versions.

The installed `DataTool.pdb` and `TankLib.pdb` also contain SHA-256 source-document checksums. A read-only audit matched all 769 local source documents located from these PDBs, including every implementation cited here, after accounting for the Windows checkout's CRLF and UTF-8 BOM encoding. This independently verifies the existing `work/owlib/OWLib-develop` files against the installed package. Reproducer and full checksums: `work/animation-research/identity-source/check_pdb_documents.py` and `pdb-source-checksums.json`. The audit does not modify the installation or game assets.

## Resource identity and names

[`teResourceGUID.cs:13`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/TankLib/teResourceGUID.cs#L13), `AttributeEnum`, `Type` (62), `FlipTypeBits` (132), `AsString` (168):

| Value | Exact interpretation |
|---|---|
| Raw GUID | Unsigned 64-bit resource identifier; store as a 16-digit hex string. |
| `Index` | Low 32 bits only. |
| `LongKey` | Low 48 bits, mask `0x0000FFFFFFFFFFFF`. |
| Resource type | Reverse the 12 bits at positions 48–59, then add one. |
| Engine | Bits 60–63. |
| `AsString` | Twelve-digit LongKey plus three-digit decoded type. **Engine is omitted.** |

Knowing LongKey and type still does not establish Engine. Consequently a `.owanimclip` filename plus presumed type `006` does not recover a full GUID. Even `123456789ABC.006` is not a lossless 64-bit identity. Preserve known components and an export-scoped local identity; never silently fill the engine nibble with zero. A raw GUID can be reconstructed only when all components are known: `(engine << 60) | (reverse12(type - 1) << 48) | longKey`. No animation-name-to-GUID hashing function was established here.

[`Helper/IO.cs:54`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/Helper/IO.cs#L54), `LoadGUIDTable` / `GetNullableGUIDName` (170), reads `Static/GUIDNames.csv` by `(LongKey, type)`. The table contains an explicit Canonical column. Noncanonical names gain a hexadecimal suffix. This is **upstream name-table provenance**; a friendly exported filename alone does not establish an original internal animation name. Filename sanitization may also be lossy. The installed table contains five type-006 entries; none matches the 246 numeric clip stems in the inspected Widowmaker export. User nicknames remain a separate source.

## What a track actually means

[`teAnimation.cs:35`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/TankLib/teAnimation.cs#L35), `AnimHeader` / constructor (166–173), reads a `BoneCount`-length array of integer bone IDs. Each bone has independent position, rotation, and scale key collections. It computes `InfoTableSize = (int)(FPS * Duration) + 1`. Non-bone animation data is explicitly not read by this parser. `Header.Effect` is an animation-effect resource reference (type `08F`). `Priority` and `Group` are raw bytes; the source itself leaves the meaning of Group uncertain. They are not established action categories.

[`OverwatchAnimationClip.cs:24`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/TankLib/ExportFormats/OverwatchAnimationClip.cs#L24), `Write`, emits version 2.0, `BoneList.Length`, FPS, and frame count. Every bone entry contains its name and 0–3 populated transform channels. It does **not** include the animation GUID, source name, Priority, Group, Effect GUID, model GUID, or producer build. Whitehound's existing displayed “tracks” value is this header bone-entry count, not the sum of transform channels, number of varying channels, keyframes, or skeleton size. Its displayed duration derives from `(frameCount - 1) / FPS`; the original duration float is not exported verbatim.

[`OverwatchModel.cs:128`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/TankLib/ExportFormats/OverwatchModel.cs#L128), `Write` / `IdToString` (299), writes skeleton names from `skeleton.IDs[i]`, with `bone_` plus hexadecimal ID. The animation writer applies the same naming function to `(uint)BoneList[i]`. Array positions are not interchangeable with IDs. Extra cloth entries use `cloth_` names. Skinning uses a separate skeleton lookup. Compare actual exported bone IDs/names; record matches and missing IDs independently for every candidate skeleton. Full or partial compatibility establishes neither ownership nor an action such as Reload.

## Original paths and associations

[`SaveLogic/Combo.cs:177`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/SaveLogic/Combo.cs#L177), `ConvertAnimation` / `SaveModel` (386, 462), places converted clips under `Models/<model>/Animations/<Priority>/<Group>/<name>.owanimclip`. The numeric directories preserve the parsed header values. A failed raw-animation parse falls back to 16/16, so a raw file's directory values alone are not guaranteed parsed header values. The converted file itself lacks those fields. The same animation can be exported beneath several models; retain every occurrence.

`SaveEntity` also writes animation reference companions. However, **this release unconditionally returns in `SaveAnimation` before writing the data `.owanim` wrapper**. Thus a reference companion can point to a missing generated file. Missing targets must be reported, not treated as successful evidence-chain traversal.

[`FindLogic/Combo.cs:615`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/FindLogic/Combo.cs#L615), type-006 case, adds a discovered animation to the current model/entity sets before its deduplication check. Its graph contains model-component, blend-tree, script, effect, and unlock references. These sets summarize traversal, not action roles. Effect model chunks associate their Model and Animation explicitly; entity-control chunks associate Animation through Identifier. Unknown fields, including `m_36F54327`, `m_C0214513`, and `m_ED5A243E`, must retain their raw names. Traversal currently discards the full edge-by-edge path unless instrumented.

## Companion fields worth preserving

[`SaveLogic/Entity.cs:28`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/SaveLogic/Entity.cs#L28) writes entity/model/effect names and their **32-bit indices**, child names and full hardpoint/identifier values, model-look reference, and path-relative base. These support explicit entity-to-model lookup, while the entity/model indices alone do not restore their full GUIDs. Child identifiers and hardpoints are separate resources; do not relabel them as animation identities.

[`SaveLogic/Effect.cs:52`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/SaveLogic/Effect.cs#L52) writes an effect's own low-32 index. Its DMCE records retain full 64-bit Animation, Material, and Model references. CECE records retain full Animation and Identifier; NECE records retain a full entity GUID. These full references may resolve identities lost by clip filenames, but only after a consistent path/content match, not a same-short-name guess. The `.owanim` Data wrapper (168) contains a low-32 animation index, FPS, clip/model paths, and embedded effect data. The Reference wrapper (202) contains a low-32 index, FPS, and referenced path. Record version, raw field, companion path, target existence, and join limitations.

## Named contexts and limits

[`ExtractHeroUnlocks.cs:282`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/ToolLogic/Extract/ExtractHeroUnlocks.cs#L282), `SaveUnlock`, writes `Heroes/<hero>/<UnlockType>/<event>/<unlock name>/...`. Relevant types include `HighlightIntro`, `Emote`, and `VictoryPose`. Such a directory can support “included in the export for this named unlock” with path provenance. It does not recover a clip's internal name or identify its main actor. A directory named `Entities/HighlightIntro` is an entity alias, not a particular named cosmetic unlock.

[`AnimationItem.cs`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/SaveLogic/Unlock/AnimationItem.cs) traverses the unlock and saves all discovered animations, with animation-effect export disabled. Descendants may represent a prop, actor, camera, or other dependency. They require separate role evidence.

[`HighlightIntro.cs`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/DataTool/SaveLogic/Unlock/HighlightIntro.cs) distinguishes ordinary intros from Default/Heroic. Ordinary intros begin from unlock data; the relevant direct edge is `STUUnlock_POTGAnimation.m_animation`. Default/Heroic instead uses `hero.m_322C521A` → model component → `m_animBlendTreeSet`. Emotes likewise traverse `m_emoteBlendTreeSet`, so none of these should be collapsed to one universal “unlock = single clip” rule.

Exact schema fields: [`STUUnlock_POTGAnimation.cs:10`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/TankLib/STU/Types/STUUnlock_POTGAnimation.cs#L10) maps `m_animation` to `0x3F5B86A4`; [`STUUnlock_Emote.cs:14`](https://github.com/overtools/OWLib/blob/df522f0ceea8748dcb51463ceb990234ceb45c9a/TankLib/STU/Types/STUUnlock_Emote.cs#L14) maps `m_emoteBlendTreeSet` to `0x2B8351DA`. These field identities establish graph roles when actual resource references are available. They supply no particular Widowmaker action GUID by themselves.

## Minimum useful additional evidence

For a future narrow extraction, extend OWLib's export instrumentation rather than guessing from the preview:

1. Write raw GUIDs as hex strings before filename conversion, source/export/game version, output relative path, and SHA-256 for each emitted file.
2. Emit one graph edge per reference: source GUID, exact STU/chunk type, raw field identifier or chunk field, target GUID, occurrence index, and replacement GUID before/after application. Retain traversal context separately from direct edges.
3. For each animation retain its raw header Priority, Group, Effect GUID, BoneList IDs, and any actual name table/source string with provenance. For each named unlock retain its GUID, localized name, type, and direct entry edges.
4. Distinguish direct animation roles from transitive dependency membership. Keep many contexts for reused clips. Preserve unknown fields and unresolved references explicitly.

The prototype can read existing exports locally and preserve these distinctions without game access. Establishing new ability anchors or recording the missing graph requires the corresponding game resources or a trustworthy explicit manifest. No Overwatch installation is available on this machine, so no fresh named-unlock extraction or game-reference traversal was run.
