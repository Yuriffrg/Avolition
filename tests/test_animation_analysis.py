"""Resolver regression fixtures, independent of the real local export audit."""
from copy import deepcopy
import json
import os
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

from owpreview import animation_analysis as analysis
from owpreview import animation_evidence_formats as evidence_formats
from test_formats import clip_bytes, entity_bytes, model_bytes, pack, string
from test_animation_evidence_formats import effect_header, guid, playback


class AnimationAnalysisTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.root = self.base / 'Export with spaces 非ASCII'
        self.root.mkdir()

    def write(self, relative, content):
        path = self.root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    def clip(self, relative='animations/00000000007B.owanimclip', bones=None, duration=31):
        return self.write(relative, clip_bytes(bones=bones, duration=duration))

    def model(self, reference='000000000123.00C', bones=None, prefix=''):
        blob = bytearray(model_bytes(bones=bones, mesh=False, name=reference))
        index_offset = 4 + len(string('')) + len(string(reference))
        blob[index_offset:index_offset+4] = pack('I', int(reference[:-4], 16))
        return self.write(prefix + f'Models/{reference}/{reference[:-4]}.owmdl', blob)

    def entity(self, reference, model, children=()):
        blob = bytearray(entity_bytes(reference, model, children=list(children)))
        model_index_offset = len(string('owentity')) + 4 + len(string(reference)) + len(string(model)) + len(string('null')) + 4
        blob[model_index_offset:model_index_offset+4] = pack('I', int(model[:-4], 16))
        return self.write(f'Entities/{reference}/{reference}.owentity',
                          blob)

    def reference_companion(self, index, model_reference, relative=None):
        stem = f'{index:012X}'
        target = f'Models\\{model_reference}\\AnimationEffects\\{stem}\\{stem}.oweffect'
        return self.write(relative or f'Entities/E/AnimationEffects/{stem}.owanim',
                          string('owanim') + pack('HHIfi', 1, 0, index, 30, 1) + string(target))

    def exact_guid_chain(self, folder, full_guid, duration=31):
        index = full_guid & 0xFFFFFFFF
        stem = f'{index:012X}'
        clip = self.clip(f'{folder}/{stem}.owanimclip', duration=duration)
        companion = string('owanim') + pack('HHIfi', 1, 0, index, 30, 0)
        companion += string(clip.name) + string('unavailable.owmdl') + effect_header()
        self.write(f'{folder}/motion.owanim', companion)
        effect = effect_header((1, 0, 0, 0, 0, 0, 0)) + playback()
        effect += pack('QQQ', full_guid, 0, 0) + string('null') + string('motion.owanim')
        self.write(f'{folder}/use.oweffect', effect)
        return clip

    def run_manager(self, manager, cached_only=False):
        manager.start(self.root, cached_only=cached_only)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            status = manager.status(self.root, check_cache=False)
            if not status['busy']:
                return status
            time.sleep(.01)
        self.fail('Analysis worker did not finish within five seconds')

    def test_missing_metadata_stays_unknown_and_track_definition_is_explicit(self):
        self.clip()
        result = analysis.analyze(self.root)
        asset = result['assets'][0]
        self.assertIsNone(asset['sourceName'])
        self.assertIsNone(asset['resource']['fullGuid'])
        self.assertIsNone(result['gameBuild'])
        self.assertIsNone(result['dataToolVersion'])
        self.assertEqual(asset['statuses'], ['unknown'])
        self.assertEqual(asset['trackCount'], 1)
        self.assertEqual(asset['transformChannelCount'], 3)
        self.assertIn('bone records', asset['trackDefinition'])

    def test_same_bytes_different_short_resource_ids_are_separate_assets(self):
        self.clip('A/00000000007B.owanimclip')
        self.clip('B/00000000007C.owanimclip')
        result = analysis.analyze(self.root)
        self.assertEqual(result['assetCount'], 2)
        self.assertEqual(len(result['contentGroups']), 1)
        self.assertEqual(len(result['contentGroups'][0]['assetIds']), 2)

    def test_same_short_filename_different_full_guids_stay_separate(self):
        one, two = guid(123), guid(123, engine=15)
        self.exact_guid_chain('one', one)
        self.exact_guid_chain('two', two)
        result = analysis.analyze(self.root)
        self.assertEqual(result['assetCount'], 2)
        self.assertEqual({a['resource']['fullGuid'] for a in result['assets']}, {f'{one:016X}', f'{two:016X}'})
        self.assertTrue(all(a['evidence'] for a in result['assets']))

    def test_conflicting_full_guid_reference_chains_remain_unresolved(self):
        self.exact_guid_chain('one', guid(123))
        effect = effect_header((1, 0, 0, 0, 0, 0, 0)) + playback()
        effect += pack('QQQ', guid(123, engine=15), 0, 0) + string('null') + string('motion.owanim')
        self.write('one/conflicting.oweffect', effect)
        result = analysis.analyze(self.root)
        self.assertIsNone(result['assets'][0]['resource']['fullGuid'])
        self.assertTrue(any('Multiple full GUID' in c for c in result['assets'][0]['conflicts']))

    def test_same_short_filename_different_payloads_are_separate_assets(self):
        self.clip('A/00000000007B.owanimclip', duration=31)
        self.clip('B/00000000007B.owanimclip', duration=61)
        self.assertEqual(analysis.analyze(self.root)['assetCount'], 2)

    def test_short_resource_scope_changes_between_export_roots(self):
        resource = analysis.exported_resource('00000000007B.owanimclip')
        self.assertNotEqual(analysis.local_identity(self.root, resource, 'same'),
                            analysis.local_identity(self.base/'Other', resource, 'same'))

    def test_duplicate_occurrences_keep_distinct_named_contexts(self):
        self.clip('Heroes/Widowmaker/HighlightIntro/Default/Test Intro/Animations/00000000007B.owanimclip')
        self.clip('Heroes/Widowmaker/Emote/Default/Test Emote/Animations/00000000007B.owanimclip')
        result = analysis.analyze(self.root)
        self.assertEqual((result['assetCount'], result['occurrenceCount']), (1, 2))
        asset = result['assets'][0]
        self.assertEqual({c['label'] for c in asset['contexts']}, {'Test Intro', 'Test Emote'})
        self.assertEqual(result['coverage']['namedContexts'], 1)
        self.assertIsNone(asset['sourceName'])
        self.assertTrue(all('actor role unknown' in c['role'] for c in asset['contexts']))
        self.assertEqual(len({o['contexts'][0]['id'] for o in result['occurrences']}), 2)

    def test_generic_highlightintro_entity_folder_is_not_a_named_unlock(self):
        self.clip('Entities/HighlightIntro/Animations/00000000007B.owanimclip')
        result = analysis.analyze(self.root)
        self.assertEqual(result['contexts'], [])
        self.assertEqual(result['coverage']['namedContexts'], 0)

    def test_explicit_path_keeps_priority_group_without_assigning_action(self):
        self.model()
        self.clip('Models/000000000123.00C/Animations/1/32/00000000007B.owanimclip')
        result = analysis.analyze(self.root)
        occurrence, asset = result['occurrences'][0], result['assets'][0]
        self.assertEqual((occurrence['priority'], occurrence['group']), (1, 32))
        self.assertEqual(asset['models'][0]['kind'], 'explicit-export-path')
        self.assertIsNone(asset['sourceName'])
        self.assertIn('unknown', asset['statuses'])

    def test_partial_body_and_multiple_skeleton_candidates_are_retained(self):
        self.model('000000000123.00C')
        self.model('000000000124.00C', bones=[('bone_0001', -1, (0,0,0), (0,0,0)),
                                             ('bone_0002', 0, (0,0,0), (0,0,0)),
                                             ('bone_0003', 0, (0,0,0), (0,0,0))])
        self.clip()
        result = analysis.analyze(self.root)
        asset = result['assets'][0]
        self.assertEqual(len(asset['models']), 2)
        self.assertEqual({m['matchedBoneCount'] for m in asset['models']}, {1})
        self.assertEqual({m['skeletonBoneCount'] for m in asset['models']}, {2, 3})
        self.assertIn('suggested', asset['statuses'])
        self.assertIn('unknown', asset['statuses'])
        self.assertTrue(asset['conflicts'])

    def test_bone_array_positions_are_not_used_as_identifiers(self):
        self.model(bones=[('bone_9000', -1, (0,0,0), (0,0,0)), ('bone_9001', 0, (0,0,0), (0,0,0))])
        self.clip()
        asset = analysis.analyze(self.root)['assets'][0]
        self.assertEqual(asset['models'], [])
        self.assertEqual(asset['skeletonComparisons'][0]['matchedBoneCount'], 0)

    def test_unanimated_bone_records_do_not_count_as_skeleton_evidence(self):
        self.model()
        self.clip(bones=[('bone_0002', [])])
        asset = analysis.analyze(self.root)['assets'][0]
        self.assertEqual(asset['trackCount'], 1)
        self.assertEqual(asset['animatedBoneIds'], [])
        self.assertEqual(asset['models'], [])

    def test_dangling_effect_still_retains_scoped_model_reference(self):
        self.model()
        self.clip()
        self.reference_companion(123, '000000000123.00C')
        result = analysis.analyze(self.root)
        self.assertTrue(result['companions'][0]['referenceMissing'])
        self.assertEqual(result['assets'][0]['models'][0]['kind'], 'entity-reference')
        self.assertIsNone(result['assets'][0]['resource']['fullGuid'])
        self.assertIsNone(result['assets'][0]['sourceName'])

    def test_partial_reference_cannot_reach_other_export_subtree(self):
        self.model(prefix='First/')
        self.model(prefix='Second/')
        self.clip('Second/animations/00000000007B.owanimclip')
        self.reference_companion(123, '000000000123.00C', 'First/Entities/E/AnimationEffects/00000000007B.owanim')
        result = analysis.analyze(self.root)
        self.assertEqual(result['companions'][0]['candidateOccurrences'], [])
        self.assertTrue(all(m['kind'] != 'entity-reference' for m in result['assets'][0]['models']))

    def test_full_reference_key_disagreement_is_a_conflict_not_identity(self):
        self.exact_guid_chain('one', guid(123, upper_key=0xABCD))
        result = analysis.analyze(self.root)
        self.assertIsNone(result['assets'][0]['resource']['fullGuid'])
        self.assertTrue(any('disagrees with clip key/type' in c for c in result['assets'][0]['conflicts']))

    def test_explicit_model_is_preserved_when_only_some_bones_match(self):
        self.model(bones=[('bone_0001', -1, (0,0,0), (0,0,0))])
        bones = [('bone_0001', [(0, [(0, (0,0,0))])]), ('bone_0075', [(0, [(0, (0,0,0))])])]
        self.clip('Models/000000000123.00C/Animations/0/0/00000000007B.owanimclip', bones=bones)
        asset = analysis.analyze(self.root)['assets'][0]
        self.assertEqual(asset['models'][0]['kind'], 'explicit-export-path')
        self.assertEqual(asset['models'][0]['matchedBoneCount'], 1)
        self.assertEqual(asset['models'][0]['animatedBoneCount'], 2)
        self.assertEqual(asset['models'][0]['missingBoneIds'], ['bone_0075'])

    def test_ambiguous_partial_index_does_not_choose_payload(self):
        self.model()
        self.clip('animations/00000000007B.owanimclip')
        self.clip('Models/000000000123.00C/Animations/0/0/00000000007B.owanimclip', duration=61)
        self.reference_companion(123, '000000000123.00C')
        result = analysis.analyze(self.root)
        self.assertTrue(result['companions'][0]['ambiguous'])
        self.assertTrue(all('entity-reference' not in [m['kind'] for m in a['models']] for a in result['assets']))
        self.assertTrue(all(any('Ambiguous partial' in c for c in a['conflicts']) for a in result['assets']))

    def test_exported_entity_model_and_child_conventions_resolve(self):
        self.model('00000000013E.00C')
        self.model('0000000001D4.00C')
        self.entity('Gameplay1P', '00000000013E.00C', [('00000000002A.003','')])
        self.entity('00000000002A.003', '0000000001D4.00C')
        result = analysis.analyze(self.root)
        parent = next(e for e in result['entities'] if e['name'] == 'Gameplay1P')
        child = next(e for e in result['entities'] if e['name'] == '00000000002A.003')
        self.assertEqual(len(parent['modelIds']), 1)
        self.assertEqual(parent['childEntityIds'], [child['id']])
        self.assertEqual(len(child['modelIds']), 1)

    def test_parse_errors_separate_from_missing_metadata(self):
        self.write('animations/00000000007B.owanimclip', b'broken')
        result = analysis.analyze(self.root)
        self.assertEqual(len(result['parseErrors']), 1)
        self.assertEqual(len(result['assets'][0]['parseErrors']), 1)
        self.assertIsNone(result['assets'][0]['trackCount'])

    def test_nicknames_and_assets_unchanged_by_analysis(self):
        path = self.clip()
        before = path.read_bytes()
        labels = {analysis.key(path): {'name':'Reload arms', 'notes':'User context only', 'favorite':True}}
        labels_before = deepcopy(labels)
        result = analysis.analyze(self.root, labels)
        self.assertEqual(labels, labels_before)
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(result['occurrences'][0]['nickname']['value'], 'Reload arms')
        self.assertIsNone(result['assets'][0]['sourceName'])
        self.assertEqual(result['coverage']['userVerified'], 0)
        self.assertEqual(result['coverage']['userLabels'], 1)

    def test_unavailable_labels_and_installed_version_are_not_export_facts(self):
        self.clip()
        result = analysis.analyze(self.root, installed_toolchain={'version':'fixture installed version'}, labels_available=False)
        self.assertIsNone(result['coverage']['userLabels'])
        self.assertIsNone(result['dataToolVersion'])
        self.assertEqual(result['installedToolchain']['version'], 'fixture installed version')

    def test_background_worker_does_not_block_or_start_duplicate_job(self):
        self.clip()
        manager = analysis.AnalysisManager(self.base/'cache')
        entered, release = threading.Event(), threading.Event()
        original = analysis.snapshot
        def slow_snapshot(*args, **kwargs):
            entered.set()
            if not release.wait(3):
                raise RuntimeError('test release timed out')
            return original(*args, **kwargs)
        with patch.object(analysis, 'snapshot', side_effect=slow_snapshot) as mocked:
            try:
                first = manager.start(self.root)
                self.assertTrue(first['busy'])
                self.assertTrue(entered.wait(1))
                self.assertTrue(manager.start(self.root)['busy'])
                self.assertEqual(mocked.call_count, 1)
                self.assertIsNone(manager.index(self.root))
            finally:
                release.set()
            self.assertEqual(self.run_manager(manager)['state'], 'Completed')

    def test_manager_cache_invalidates_bytes_even_when_stat_is_unchanged(self):
        path = self.clip()
        manager = analysis.AnalysisManager(self.base/'cache')
        first = self.run_manager(manager)
        stamp = path.stat()
        content = bytearray(path.read_bytes()); content[-4:] = pack('f', 2.0)
        path.write_bytes(content)
        os.utime(path, ns=(stamp.st_atime_ns, stamp.st_mtime_ns))
        second = self.run_manager(manager)
        self.assertEqual(first['state'], 'Completed')
        self.assertNotEqual(first['revision'], second['revision'])

    def test_manager_cache_invalidates_aggregate_parser_and_provenance_metadata(self):
        self.clip()
        manager = analysis.AnalysisManager(self.base/'cache')
        first = self.run_manager(manager)['revision']
        self.write('export-manifest.json', b'{"build":"unverified fixture"}')
        second = self.run_manager(manager)['revision']
        self.assertNotEqual(first, second)
        with patch.object(analysis, 'PARSER_VERSION', 'fixture next parser'):
            third = self.run_manager(manager)['revision']
        self.assertNotEqual(second, third)

    def test_manager_cache_invalidates_evidence_parser_version(self):
        self.clip()
        manager = analysis.AnalysisManager(self.base/'cache')
        first = self.run_manager(manager)['revision']
        with patch.object(evidence_formats, 'PARSER_VERSION', 'fixture next evidence parser'):
            second = self.run_manager(manager)['revision']
        self.assertNotEqual(first, second)

    def test_cache_hit_and_detail_do_not_overwrite_fresh_nickname(self):
        path = self.clip()
        labels = {analysis.key(path): {'name':'First'}}
        manager = analysis.AnalysisManager(self.base/'cache', annotations=lambda: labels)
        self.assertEqual(self.run_manager(manager)['state'], 'Completed')
        with patch.object(analysis, 'analyze', side_effect=AssertionError('cache should be reused')):
            self.assertEqual(self.run_manager(manager)['state'], 'Completed')
        labels[analysis.key(path)]['name'] = 'Edited since analysis'
        self.assertEqual(manager.detail(path)['occurrence']['nickname']['value'], 'Edited since analysis')
        self.assertEqual(labels[analysis.key(path)]['name'], 'Edited since analysis')

    def test_failed_refresh_never_presents_old_cache_as_current(self):
        self.clip()
        manager = analysis.AnalysisManager(self.base/'cache')
        self.assertEqual(self.run_manager(manager)['state'], 'Completed')
        with patch.object(analysis, 'snapshot', side_effect=OSError('unavailable')):
            self.assertEqual(self.run_manager(manager)['state'], 'Failed')
        self.assertIsNone(manager.index(self.root))

    def wait_api_analysis(self, api, root=None):
        root = str(root or self.root)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            reply = api.analysis_status(root)
            self.assertTrue(reply['ok'], reply)
            if not reply['data']['busy']:
                return reply['data']
            time.sleep(.01)
        self.fail('API analysis worker did not finish within five seconds')

    def test_api_fresh_scan_does_not_automatically_analyze_without_cache(self):
        from app import BrowserAPI
        self.clip()
        api = BrowserAPI(data_dir=self.base/'state', app_dir=self.base/'app')
        with patch.object(analysis, 'analyze', side_effect=AssertionError('Fresh scan should wait for Analyze')):
            self.assertTrue(api.scan_root(str(self.root))['ok'])
            self.assertEqual(self.wait_api_analysis(api)['state'], 'Ready')
        self.assertIsNone(api.analysis_index(str(self.root))['data'])

    def test_api_rescan_leaves_dormant_analysis_idle_until_explicitly_requested(self):
        from app import BrowserAPI
        path = self.clip()
        api = BrowserAPI(data_dir=self.base/'state', app_dir=self.base/'app')
        self.assertTrue(api.analysis_start(str(self.root))['ok'])
        previous = self.wait_api_analysis(api)
        self.assertEqual(previous['state'], 'Completed')
        path.write_bytes(clip_bytes(duration=61))
        entered, release = threading.Event(), threading.Event()
        original = analysis.snapshot
        def slow_snapshot(*args, **kwargs):
            entered.set()
            if not release.wait(3):
                raise RuntimeError('test release timed out')
            return original(*args, **kwargs)
        with patch.object(analysis, 'snapshot', side_effect=slow_snapshot):
            try:
                scan = api.scan_root(str(self.root))
                self.assertTrue(scan['ok'], scan)
                self.assertFalse(entered.is_set(), 'Ordinary scans must not run removed evidence analysis.')
                self.assertTrue(api.analysis_start(str(self.root))['ok'])
                self.assertTrue(entered.wait(1))
                self.assertIsNone(api.analysis_index(str(self.root))['data'])
                self.assertIsNone(api.analysis_detail(str(path), str(self.root))['data'])
                self.assertIsNone(api.analysis_status(str(self.root))['data']['revision'])
            finally:
                release.set()
            current = self.wait_api_analysis(api)
        self.assertEqual(current['state'], 'Completed')
        self.assertNotEqual(previous['revision'], current['revision'])
        detail = api.analysis_detail(str(path), str(self.root))['data']
        self.assertEqual(detail['asset']['durationSeconds'], 2.0)

    def test_api_exclusions_remove_only_managed_directory_and_tracked_copy(self):
        from app import BrowserAPI
        original = self.clip('Originals/00000000007B.owanimclip')
        existing_original = self.clip('Existing destination/00000000007C.owanimclip')
        tracked_copy = self.clip('Existing destination/nicknamed-copy.owanimclip')
        managed_copy = self.clip('Managed destination/nicknamed-copy.owanimclip')
        api = BrowserAPI(data_dir=self.base/'state', app_dir=self.base/'app')
        api._catalog['exportDirectories'] = [analysis.key(existing_original.parent), analysis.key(managed_copy.parent)]
        api._catalog['managedOutputDirectories'] = [analysis.key(managed_copy.parent)]
        api._catalog['exportedFiles'] = [analysis.key(tracked_copy)]
        self.assertTrue(api.save_annotation(str(existing_original), {'name':'Keep this original'})['ok'])
        scan = api.scan_root(str(self.root))
        self.assertTrue(scan['ok'], scan)
        expected = {str(original), str(existing_original)}
        self.assertEqual({c['path'] for c in scan['data']['clips']}, expected)
        self.wait_api_analysis(api)
        self.assertTrue(api.analysis_start(str(self.root))['ok'])
        self.assertEqual(self.wait_api_analysis(api)['state'], 'Completed')
        index = api.analysis_index(str(self.root))['data']
        self.assertEqual({entry['path'] for entry in index['entries']}, expected)
        detail = api.analysis_detail(str(existing_original), str(self.root))['data']
        self.assertEqual(detail['occurrence']['nickname']['value'], 'Keep this original')
        self.assertIsNone(api.analysis_detail(str(tracked_copy), str(self.root))['data'])
        self.assertIsNone(api.analysis_detail(str(managed_copy), str(self.root))['data'])
        self.assertTrue(all(p.is_file() for p in (original, existing_original, tracked_copy, managed_copy)))

    def test_api_detail_uses_requested_root_when_scanned_roots_overlap(self):
        from app import BrowserAPI
        path = self.clip('Nested/animations/00000000007B.owanimclip')
        nested = self.root/'Nested'
        api = BrowserAPI(data_dir=self.base/'state', app_dir=self.base/'app')
        self.assertTrue(api.analysis_start(str(self.root))['ok'])
        self.assertEqual(self.wait_api_analysis(api)['state'], 'Completed')
        broad = api.analysis_detail(str(path), str(self.root))['data']
        self.assertTrue(api.analysis_start(str(nested))['ok'])
        self.assertEqual(self.wait_api_analysis(api, nested)['state'], 'Completed')
        narrow = api.analysis_detail(str(path), str(nested))['data']
        self.assertNotEqual(broad['asset']['id'], narrow['asset']['id'])
        self.assertEqual(narrow['occurrence']['relativePath'], 'animations/00000000007B.owanimclip')
        self.assertEqual(api.analysis_detail(str(path), str(self.root))['data']['asset']['id'], broad['asset']['id'])


if __name__ == '__main__':
    unittest.main()
