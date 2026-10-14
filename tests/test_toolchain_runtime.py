"""Mocked official release/download tests; no real game extraction is implied."""
import hashlib
import io
import json
from pathlib import Path
import stat
import tempfile
import threading
import unittest
from unittest.mock import patch
from urllib.error import HTTPError, URLError
import zipfile

from owpreview.toolchain_runtime import (
    ASSET_NAME, RELEASE_URL, ToolchainError, ToolchainManager,
    package_differs, release_metadata,
)


def archive(entries=None):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, 'w', zipfile.ZIP_DEFLATED) as package:
        for name, value in (entries or {
            'Toolchain/DataTool.exe': b'MZmock executable',
            'Toolchain/DataTool.dll': b'managed companion',
            'Toolchain/DataTool.runtimeconfig.json': json.dumps({'runtimeOptions': {'includedFrameworks': [{'name': 'Microsoft.NETCore.App'}]}}).encode(),
            'Toolchain/DataTool.deps.json': json.dumps({'runtimeTarget': {'name': 'mock/windows'}, 'targets': {'mock/windows': {'DataTool/1': {'runtime': {'DataTool.dll': {}}, 'native': {'runtimes/win-x64/native/dependency.dll': {}}}}}}).encode(),
            'Toolchain/hostfxr.dll': b'host', 'Toolchain/hostpolicy.dll': b'policy',
            'Toolchain/coreclr.dll': b'runtime',
            'Toolchain/runtimes/win-x64/native/dependency.dll': b'native companion',
            'Toolchain/help/HOW_TO_USE_A_COMMAND_LINE.txt': b'Tutorial',
        }).items():
            package.writestr(name, value)
    return buffer.getvalue()


def release(blob, *, tag='v1.2.3', asset_id=123, digest=True):
    return {
        'id': 90, 'tag_name': tag, 'published_at': '2026-07-11T18:12:13Z',
        'assets': [
            {'name': 'source-code.zip', 'browser_download_url': 'https://invalid.example/source.zip'},
            {'id': asset_id, 'name': ASSET_NAME, 'size': len(blob),
             'updated_at': '2026-07-12T19:13:14Z',
             'digest': ('sha256:' + hashlib.sha256(blob).hexdigest()) if digest else None,
             'browser_download_url': 'https://github.com/overtools/OWLib/releases/download/' + tag + '/' + ASSET_NAME},
        ],
    }


class MockNetwork:
    def __init__(self, blob=None):
        self.blob = blob or archive()
        self.release = release(self.blob)
        self.requests = []
        self.error = None
        self.download_error = None

    def __call__(self, request, timeout=30):
        self.requests.append(request.full_url)
        if request.full_url == RELEASE_URL:
            if self.error:
                raise self.error
            return io.BytesIO(json.dumps(self.release).encode())
        if self.download_error:
            raise self.download_error
        return io.BytesIO(self.blob)


class ToolchainRuntimeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'Whitehound with spaces 漢字' / 'toolchain'
        self.network = MockNetwork()
        self.manager = ToolchainManager(self.root, opener=self.network)

    def install(self):
        self.manager.install_async()
        result = self.manager.wait(5)
        self.assertFalse(result['busy'])
        self.assertIsNone(result['error'], result['error'])
        return result

    def test_fresh_status_does_not_create_directories_or_fake_dates(self):
        value = self.manager.snapshot()
        self.assertIsNone(value['installed'])
        self.assertIsNone(value['latest'])
        self.assertIsNone(value['updateAvailable'])
        self.assertIsNone(value['executable'])
        self.assertFalse(self.root.exists())

    def test_check_only_and_explicit_install_keep_installed_metadata_distinct(self):
        self.manager.check_async()
        checked = self.manager.wait(5)
        self.assertIsNone(checked['installed'])
        self.assertEqual(checked['latest']['version'], 'v1.2.3')
        self.assertTrue(checked['updateAvailable'])
        self.assertFalse(self.root.exists())
        installed = self.install()['installed']
        self.assertEqual(installed['releaseDate'], '2026-07-11T18:12:13Z')
        self.assertEqual(installed['packageUpdatedAt'], '2026-07-12T19:13:14Z')
        self.assertIsNotNone(installed['installedAt'])
        self.assertTrue(installed['installedAt'].endswith('Z'))
        self.assertNotEqual(installed['installedAt'], installed['releaseDate'])
        self.assertTrue(installed['digestVerified'])
        self.network.release = release(self.network.blob, tag='v2.0', asset_id=456)
        self.manager.check_async()
        value = self.manager.wait(5)
        self.assertEqual(value['installed'], installed)
        self.assertEqual(value['latest']['version'], 'v2.0')
        self.assertTrue(value['updateAvailable'])

    def test_full_package_structure_and_zip_preserved_and_reload_works_offline(self):
        self.install()
        executable = self.manager.executable()
        self.assertEqual(executable.name, 'DataTool.exe')
        self.assertTrue(executable.is_relative_to(self.root))
        self.assertEqual((executable.parent / 'DataTool.dll').read_bytes(), b'managed companion')
        self.assertEqual((executable.parent / 'runtimes/win-x64/native/dependency.dll').read_bytes(), b'native companion')
        package = executable.parent.parent.parent
        self.assertEqual((package / ASSET_NAME).read_bytes(), self.network.blob)
        self.assertTrue((package / 'installation.json').is_file())
        self.network.error = URLError('offline')
        reloaded = ToolchainManager(self.root, opener=self.network)
        self.assertEqual(reloaded.executable(), executable)
        self.assertEqual(reloaded.snapshot()['installed'], self.manager.snapshot()['installed'])
        reloaded.check_async()
        value = reloaded.wait(5)
        self.assertIn('internet connection', value['error'])
        self.assertIsNone(value['updateAvailable'])
        self.assertEqual(reloaded.executable(), executable)

    def test_same_tag_replacement_is_an_update(self):
        original = self.install()['installed']
        changed = dict(original, assetId=999)
        self.assertTrue(package_differs(original, changed))
        self.assertTrue(package_differs(original, dict(original, digest='sha256:replacement')))
        self.assertTrue(package_differs(original, dict(original, packageUpdatedAt='2026-09-01T00:00:00Z')))
        self.assertFalse(package_differs(original, dict(original, installedAt='arbitrary local time')))

    def test_legacy_runtime_is_used_without_inventing_release_metadata(self):
        self.root.mkdir(parents=True)
        executable = self.root / 'DataTool.exe'
        executable.write_bytes(b'MZlegacy')
        manager = ToolchainManager(self.root, opener=self.network)
        self.assertEqual(manager.executable(), executable)
        installed = manager.snapshot()['installed']
        self.assertTrue(installed['legacy'])
        for key in ('version', 'releaseDate', 'packageUpdatedAt', 'installedAt', 'digest'):
            self.assertIsNone(installed[key])
        manager.install_async()
        self.assertIsNone(manager.wait(5)['error'])
        self.assertTrue(executable.exists())
        self.assertNotEqual(manager.executable(), executable)

    def test_abandoned_staging_and_inactive_packages_are_never_adopted(self):
        for folder in ('.staging/abandoned', 'packages/unpublished'):
            path = self.root / folder
            path.mkdir(parents=True)
            (path / 'DataTool.exe').write_bytes(b'MZorphan')
        manager = ToolchainManager(self.root, opener=self.network)
        self.assertIsNone(manager.executable())

    def test_digest_failure_preserves_previous_pointer_and_runtime(self):
        self.install()
        before = (self.root / 'active.json').read_bytes()
        executable = self.manager.executable()
        self.network.release = release(self.network.blob, tag='v2')
        self.network.release['assets'][1]['digest'] = 'sha256:' + '0' * 64
        self.manager.check_async()
        self.manager.wait(5)
        self.manager.install_async()
        value = self.manager.wait(5)
        self.assertIn('digest does not match', value['error'])
        self.assertEqual((self.root / 'active.json').read_bytes(), before)
        self.assertEqual(self.manager.executable(), executable)
        self.assertEqual(len(list((self.root / 'packages').iterdir())), 1)
        self.assertEqual(list((self.root / '.staging').iterdir()), [])

    def test_failed_atomic_activation_preserves_old_installation(self):
        self.install()
        previous = (self.root / 'active.json').read_bytes()
        with patch('owpreview.toolchain_runtime.os.replace', side_effect=PermissionError('denied')):
            self.manager.install_async()
            value = self.manager.wait(5)
        self.assertIn('permissions', value['error'])
        self.assertEqual((self.root / 'active.json').read_bytes(), previous)
        self.assertTrue(self.manager.executable().is_file())
        self.assertEqual(len(list((self.root / 'packages').iterdir())), 1)

    def test_missing_asset_rate_limit_offline_and_corrupt_zip_are_actionable(self):
        cases = [
            ('missing', None, 'toolchain-release.zip'),
            ('rate', HTTPError(RELEASE_URL, 403, 'limit', {'X-RateLimit-Reset': '1791151200'}, None), 'rate limit'),
            ('offline', URLError('disconnected'), 'internet connection'),
        ]
        for kind, error, text in cases:
            with self.subTest(kind=kind):
                network = MockNetwork()
                network.error = error
                if kind == 'missing':
                    network.release['assets'] = []
                manager = ToolchainManager(self.root, opener=network)
                manager.check_async()
                self.assertIn(text, manager.wait(5)['error'])
        network = MockNetwork(b'not a zip file')
        manager = ToolchainManager(self.root, opener=network)
        manager.install_async()
        self.assertIn('not a valid ZIP', manager.wait(5)['error'])

    def test_missing_digest_is_explicit_and_does_not_fabricate_verification(self):
        self.network.release = release(self.network.blob, digest=False)
        value = self.install()
        self.assertFalse(value['installed']['digestVerified'])
        self.assertIsNone(value['installed']['digest'])
        self.assertIn('did not provide a digest', value['message'])

    def test_incomplete_download_and_unsupported_digest_are_rejected(self):
        self.network.blob = self.network.blob[:-10]
        self.manager.install_async()
        self.assertIn('incomplete', self.manager.wait(5)['error'])
        self.network = MockNetwork()
        self.network.release['assets'][1]['digest'] = 'md5:' + '0' * 32
        manager = ToolchainManager(self.root, opener=self.network)
        manager.install_async()
        self.assertIn('unsupported or invalid package digest', manager.wait(5)['error'])

    def test_rejects_traversal_windows_aliases_and_symlink_entries(self):
        names = ['../escape.txt', '/absolute.txt', 'C:/escape.txt', '..\\escape.txt',
                 '\\\\server\\share.txt', 'folder/../escape.txt', 'file:stream',
                 'CON.txt', 'folder./DataTool.exe', 'folder /DataTool.exe']
        for name in names:
            with self.subTest(name=name):
                blob = archive({'DataTool.exe': b'MZvalid', name: b'evil'})
                manager = ToolchainManager(self.root, opener=MockNetwork(blob))
                manager.install_async()
                self.assertIn('unsafe path', manager.wait(5)['error'])
                self.assertIsNone(manager.executable())
        link = zipfile.ZipInfo('link')
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        network = MockNetwork(archive({'DataTool.exe': b'MZvalid', link: b'../elsewhere'}))
        manager = ToolchainManager(self.root, opener=network)
        manager.install_async()
        self.assertIn('symbolic links', manager.wait(5)['error'])

    def test_case_insensitive_duplicate_paths_are_rejected(self):
        network = MockNetwork(archive({'DataTool.exe': b'MZone', 'datatool.exe': b'MZtwo'}))
        manager = ToolchainManager(self.root, opener=network)
        manager.install_async()
        self.assertIn('duplicate', manager.wait(5)['error'])

    def test_expected_runtime_must_be_unique_and_a_windows_executable(self):
        for entries in ({'Other.exe': b'MZother'}, {'DataTool.exe': b'not executable'},
                        {'one/DataTool.exe': b'MZone', 'two/DataTool.exe': b'MZtwo'}):
            with self.subTest(entries=list(entries)):
                manager = ToolchainManager(self.root, opener=MockNetwork(archive(entries)))
                manager.install_async()
                self.assertIn('DataTool', manager.wait(5)['error'])
                self.assertIsNone(manager.executable())

    def test_incomplete_runtime_and_manifest_dependencies_are_rejected(self):
        with zipfile.ZipFile(io.BytesIO(archive())) as package:
            complete = {item.filename: package.read(item) for item in package.infolist()}
        for omitted in ('Toolchain/DataTool.dll', 'Toolchain/coreclr.dll',
                        'Toolchain/runtimes/win-x64/native/dependency.dll'):
            with self.subTest(omitted=omitted):
                entries = {name: value for name, value in complete.items() if name != omitted}
                manager = ToolchainManager(self.root, opener=MockNetwork(archive(entries)))
                manager.install_async()
                self.assertIn('runtime is incomplete', manager.wait(5)['error'])
                self.assertIsNone(manager.executable())
        complete['Toolchain/DataTool.deps.json'] = b'corrupt json'
        manager = ToolchainManager(self.root, opener=MockNetwork(archive(complete)))
        manager.install_async()
        self.assertIn('invalid .NET dependency', manager.wait(5)['error'])

    def test_check_failure_keeps_latest_separate_but_status_unverified(self):
        self.install()
        latest = self.manager.snapshot()['latest']
        self.network.error = URLError('offline')
        self.manager.check_async()
        value = self.manager.wait(5)
        self.assertEqual(value['latest'], latest)
        self.assertIsNone(value['updateAvailable'])
        self.assertTrue(value['checkError'])

    def test_update_cannot_start_while_export_holds_shared_lock(self):
        lock = threading.Lock()
        manager = ToolchainManager(self.root, opener=self.network, activity_lock=lock)
        lock.acquire()
        try:
            with self.assertRaisesRegex(ToolchainError, 'active DataTool job'):
                manager.install_async()
            manager.check_async()
            self.assertIsNone(manager.wait(5)['error'])
        finally:
            lock.release()

    def test_install_is_async_prevents_duplicates_and_holds_job_lock(self):
        started = threading.Event()
        finish = threading.Event()
        lock = threading.Lock()
        network = self.network

        def delayed(request, **kwargs):
            started.set()
            finish.wait(5)
            return network(request, **kwargs)

        manager = ToolchainManager(self.root, opener=delayed, activity_lock=lock)
        manager.install_async()
        self.assertTrue(started.wait(2))
        self.assertTrue(manager.snapshot()['busy'])
        self.assertFalse(lock.acquire(blocking=False))
        with self.assertRaisesRegex(ToolchainError, 'already in progress'):
            manager.install_async()
        manager.close()
        finish.set()
        value = manager.wait(5)
        self.assertIn('interrupted', value['error'])
        self.assertFalse(value['busy'])
        self.assertTrue(lock.acquire(blocking=False))
        lock.release()
        self.assertIsNone(manager.executable())

    def test_metadata_only_accepts_exact_asset_name_and_official_asset_url(self):
        self.assertEqual(release_metadata(self.network.release)['assetName'], ASSET_NAME)
        self.network.release['assets'][1]['browser_download_url'] = 'https://evil.example/toolchain-release.zip'
        with self.assertRaisesRegex(ToolchainError, 'unexpected download URL'):
            release_metadata(self.network.release)

    def test_active_pointer_cannot_escape_toolchain_root(self):
        self.root.mkdir(parents=True)
        outside = self.root.parent / 'DataTool.exe'
        outside.write_bytes(b'MZoutside')
        (self.root / 'active.json').write_text(json.dumps({'executable': '../DataTool.exe', 'package': {}}))
        manager = ToolchainManager(self.root, opener=self.network)
        self.assertIsNone(manager.executable())
        self.assertIn('could not be loaded', manager.snapshot()['error'])


if __name__ == '__main__':
    unittest.main()
