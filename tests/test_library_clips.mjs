import assert from 'node:assert/strict';
import { visibleClips } from '../web/library-clips.js';

let checks = 0;
function same(actual, expected, message) { assert.deepEqual(actual, expected, message); checks++; }
function paths(rows) { return rows.map(row => row.path); }
function clip(path, duplicateKey, nickname = '', extra = {}) {
  return {path, name:path.split('/').pop(), duplicateKey, annotation:{name:nickname}, ...extra};
}

const original = clip('/Genji/Default/001.owanimclip', 'motion:reload');
const skin = clip('/Genji/Skin/001.owanimclip', 'motion:reload');
const renamed = clip('/Genji/Named/reload.owanimclip', 'motion:reload', 'Reload arms');
const different = clip('/Genji/Other/001.owanimclip', 'motion:different', 'Attack');
const rows = [original, skin, renamed, different];
const before = JSON.stringify(rows);

same(paths(visibleClips(rows)), [renamed.path, different.path], 'Hide duplicates defaults on and prefers nicknamed copies');
same(paths(visibleClips(rows, {hideDuplicates:false})), paths(rows), 'Turning filtering off exposes every original path');
same(JSON.stringify(rows), before, 'Filtering never changes original rows or nicknames');
same(paths(visibleClips([original, different])), [original.path, different.path], 'Same filename with different content keys remains distinct');
same(paths(visibleClips([original, skin], {selectedPath:skin.path})), [skin.path], 'Keep the selected source visible within its duplicate group');
same(paths(visibleClips(rows, {selectedPath:original.path})), [original.path, different.path], 'Selected copy wins over another copy with a nickname');
same(paths(visibleClips([original, skin], {entityPath:'/Genji/Skin/Entities/Gameplay1P.owentity'})), [skin.path], 'Prefer a source near the loaded entity when no selected/nicknamed source wins');
same(paths(visibleClips([renamed, skin], {entityPath:'/Genji/Skin/Entities/Gameplay1P.owentity'})), [renamed.path], 'A saved nickname remains accessible even when another duplicate is nearer the entity');
same(paths(visibleClips(rows, {query:'  ReLoAd ArMs  ', selectedPath:original.path})), [renamed.path], 'Search evaluates every alias before collapsing; selected nonmatching copy cannot hide a matching nickname');
same(paths(visibleClips(rows, {query:'/Genji/Skin/'})), [skin.path], 'Searching a hidden source path reveals that exact duplicate');
same(paths(visibleClips(rows, {query:'001.owanimclip', selectedPath:skin.path})), [skin.path, different.path], 'Filename search preserves different-content matches');
same(paths(visibleClips(rows, {query:'does not exist'})), [], 'Unmatched searches return no rows');
same(paths(visibleClips(rows, {query:'001.owanimclip', hideDuplicates:false})), [original.path, skin.path, different.path], 'Search still works with duplicate filtering disabled');

const anotherLabel = clip('/Genji/Another/001.owanimclip', 'motion:reload', 'Magazine removal');
same(paths(visibleClips([renamed, anotherLabel], {query:'magazine'})), [anotherLabel.path], 'One named copy never hides another matching named alias');
same(paths(visibleClips([renamed, anotherLabel], {query:'reload'})), [renamed.path], 'Each nickname can retrieve its original source');

const bad = clip('/Genji/Bad/001.owanimclip', 'motion:reload', '', {error:'Truncated clip'});
const denied = clip('/Genji/Denied/001.owanimclip', 'motion:reload', '', {duplicateError:'Access denied'});
same(paths(visibleClips([original, bad, denied])), [original.path, bad.path, denied.path], 'Unreadable clips remain separate even with an unreliable duplicate key');
const oldOne = clip('/Old/A/001.owanimclip', undefined);
const oldTwo = clip('/Old/B/001.owanimclip', undefined);
same(paths(visibleClips([oldOne, oldTwo])), [oldOne.path, oldTwo.path], 'Missing duplicate metadata does not hide same-named files');
same(paths(visibleClips([skin, original])), paths(visibleClips([original, skin])), 'Representative selection is deterministic regardless of file enumeration order');
same(paths(visibleClips([clip('/A/1.owanimclip', 'same', '   '), clip('/B/1.owanimclip', 'same', 'Name')])), ['/B/1.owanimclip'], 'Whitespace-only nicknames do not outrank a meaningful nickname');
same(paths(visibleClips([])), [], 'Empty library is supported');

console.log(`Library duplicate filtering: ${checks} checks passed.`);
