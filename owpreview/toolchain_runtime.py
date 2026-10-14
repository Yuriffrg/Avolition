"""Managed, independently updateable OWLib Toolchain installation.

The caller supplies Avolition's toolchain directory and, optionally, a lock
shared with DataTool jobs. Packages are immutable after validation. Publishing
one small atomic pointer activates a package, so an interrupted installation
never overwrites the runtime a previous session used.
"""
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import threading
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen
import uuid
import zipfile


RELEASE_URL = 'https://api.github.com/repos/overtools/OWLib/releases/latest'
ASSET_NAME = 'toolchain-release.zip'
MAX_DOWNLOAD = 2 * 1024 ** 3
MAX_EXPANDED = 8 * 1024 ** 3
MAX_ENTRIES = 50_000
USER_AGENT = 'Avolition-Toolchain-Manager'


class ToolchainError(RuntimeError):
    """An actionable installation or metadata error safe to show in the UI."""


def _utc_now():
    return datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z')


def _download_url(value):
    parsed = urlsplit(value or '')
    if (parsed.scheme != 'https' or parsed.hostname != 'github.com'
            or not parsed.path.lower().startswith('/overtools/owlib/releases/download/')
            or parsed.username or parsed.password):
        raise ToolchainError('The Toolchain release has an unexpected download URL. Check the official OWLib release.')
    return value


def release_metadata(release):
    """Keep release dates and asset dates distinct; do not substitute local time."""
    if (not isinstance(release, dict) or not isinstance(release.get('tag_name'), str)
            or not release['tag_name'].strip()):
        raise ToolchainError('GitHub returned invalid Toolchain release metadata. Try checking again later.')
    matches = [asset for asset in release.get('assets', [])
               if isinstance(asset, dict) and asset.get('name') == ASSET_NAME]
    if len(matches) != 1:
        raise ToolchainError('The latest OWLib release does not contain exactly one toolchain-release.zip package.')
    asset = matches[0]
    size = asset.get('size')
    if not isinstance(size, int) or isinstance(size, bool) or size <= 0 or size > MAX_DOWNLOAD:
        raise ToolchainError('GitHub reported an invalid Toolchain package size.')
    return {
        'version': release['tag_name'], 'releaseId': release.get('id'),
        'releaseDate': release.get('published_at'),
        'packageUpdatedAt': asset.get('updated_at'), 'installedAt': None,
        'assetId': asset.get('id'), 'assetName': ASSET_NAME, 'assetSize': size,
        'digest': asset.get('digest') or None,
        'downloadUrl': _download_url(asset.get('browser_download_url')),
    }


def package_differs(installed, available):
    if installed is None or installed.get('legacy'):
        return True
    # updated_at and size also catch replacement assets when GitHub supplies no
    # digest. No repository commit or last-check timestamp is used here.
    keys = ('version', 'releaseId', 'assetId', 'digest', 'assetSize', 'packageUpdatedAt')
    return any(installed.get(key) != available.get(key) for key in keys)


def _safe_member(name):
    normalized = name.replace('\\', '/')
    path = PurePosixPath(normalized)
    parts = normalized.rstrip('/').split('/')
    reserved = re.compile(r'^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)', re.I)
    if (not normalized or '\x00' in normalized or path.is_absolute()
            or any(not part or part in ('.', '..') or ':' in part
                   or part.endswith((' ', '.')) or reserved.match(part)
                   for part in parts)):
        raise ToolchainError(f'Toolchain archive contains an unsafe path: {name!r}.')
    return parts


def _runtime_executable(root):
    candidates = [path for path in root.rglob('*')
                  if path.is_file() and path.name.lower() == 'datatool.exe']
    if len(candidates) != 1:
        raise ToolchainError('Toolchain must contain exactly one DataTool.exe runtime. The downloaded package is incomplete or ambiguous.')
    executable = candidates[0]
    with executable.open('rb') as stream:
        if stream.read(2) != b'MZ':
            raise ToolchainError('The Toolchain package does not contain a valid Windows DataTool executable.')
    _validate_runtime(executable)
    return executable


def _validate_runtime(executable):
    """Validate the released .NET apphost and dependencies from its own manifest."""
    folder = executable.parent
    required = {'DataTool.dll', 'DataTool.deps.json', 'DataTool.runtimeconfig.json'}
    missing = sorted(name for name in required if not (folder / name).is_file()
                     or (folder / name).stat().st_size == 0)
    if missing:
        raise ToolchainError('The DataTool runtime is incomplete; missing ' + ', '.join(missing) + '.')
    try:
        config = json.loads((folder / 'DataTool.runtimeconfig.json').read_text(encoding='utf-8-sig'))
        dependencies = json.loads((folder / 'DataTool.deps.json').read_text(encoding='utf-8-sig'))
        target = dependencies['targets'][dependencies['runtimeTarget']['name']]
        if not isinstance(config['runtimeOptions'], dict) or not isinstance(target, dict):
            raise ValueError('Invalid runtime configuration')
        # OWLib's official Windows package currently includes its .NET runtime.
        # A framework-dependent future package need not carry these host files.
        if config['runtimeOptions'].get('includedFrameworks'):
            required.update(('hostfxr.dll', 'hostpolicy.dll', 'coreclr.dll'))
        for library in target.values():
            for kind in ('runtime', 'native'):
                for name in library.get(kind, {}):
                    # .NET publishes package-relative dependency names either
                    # at their relative path or flattened beside the apphost.
                    parts = _safe_member(name)
                    candidates = (folder.joinpath(*parts), folder / parts[-1])
                    if not any(path.is_file() and path.stat().st_size > 0 for path in candidates):
                        missing.append(name)
        missing.extend(name for name in required if not (folder / name).is_file()
                       or (folder / name).stat().st_size == 0)
    except (ValueError, KeyError, TypeError, AttributeError) as error:
        raise ToolchainError('The DataTool runtime has invalid .NET dependency or runtime configuration files.') from error
    if missing:
        raise ToolchainError('The DataTool runtime is incomplete; missing ' + ', '.join(sorted(set(missing))[:8]) + '.')


def _error_message(error):
    if isinstance(error, HTTPError):
        if error.code in (403, 429):
            reset = error.headers.get('X-RateLimit-Reset') if error.headers else None
            retry = ''
            if reset and reset.isdigit():
                try:
                    retry = ' Retry after ' + datetime.fromtimestamp(int(reset), timezone.utc).strftime('%Y-%m-%d %H:%M UTC') + '.'
                except (ValueError, OverflowError, OSError):
                    pass
            return 'GitHub denied the request or its rate limit was reached.' + retry + ' The installed copy is still available.'
        if error.code == 404:
            return 'The OWLib release or package is unavailable on GitHub. Check for updates again later.'
        return f'GitHub returned HTTP {error.code}. Try again later; the installed copy is unchanged.'
    if isinstance(error, (URLError, TimeoutError, ConnectionError)):
        return 'Cannot reach GitHub. Check your internet connection and try again; the installed copy is unchanged.'
    if isinstance(error, PermissionError):
        return 'Avolition cannot write to its toolchain folder. Check folder permissions and available disk space.'
    if isinstance(error, zipfile.BadZipFile):
        return 'The Toolchain download is not a valid ZIP archive. Try installing again; the installed copy is unchanged.'
    if isinstance(error, OSError):
        return f'Toolchain file operation failed: {error}. Check permissions and free disk space; the installed copy is unchanged.'
    return str(error) or 'The Toolchain operation failed. The installed copy is unchanged.'


class ToolchainManager:
    def __init__(self, root, *, activity_lock=None, opener=None):
        self.root = Path(root).expanduser().resolve()
        self.activity_lock = activity_lock if activity_lock is not None else threading.Lock()
        self._opener = opener or urlopen
        self._mutex = threading.RLock()
        self._thread = None
        self._cancelled = threading.Event()
        self._busy = False
        self._state = 'ready'
        self._message = ''
        self._error = None
        self._check_error = None
        self._latest = None
        self._checked_at = None
        self._installed = None
        self._executable = None
        self._load_installed()

    def _load_installed(self):
        pointer = self.root / 'active.json'
        if pointer.exists():
            try:
                if pointer.stat().st_size > 1024 * 1024:
                    raise ToolchainError('Toolchain installation metadata is invalid.')
                value = json.loads(pointer.read_text(encoding='utf-8'))
                if value.get('schema') != 1 or not isinstance(value.get('package'), dict):
                    raise ToolchainError('Toolchain installation metadata is invalid.')
                relative = value['executable']
                parts = _safe_member(relative)
                executable = self.root.joinpath(*parts).resolve()
                if not executable.is_relative_to(self.root):
                    raise ToolchainError('Toolchain installation metadata points outside its folder.')
                with executable.open('rb') as stream:
                    if stream.read(2) != b'MZ':
                        raise ToolchainError('The installed DataTool executable is invalid.')
                self._installed = value['package']
                self._executable = executable
                return
            except (OSError, ValueError, KeyError, TypeError, ToolchainError) as error:
                self._error = f'The installed Toolchain could not be loaded: {error}. Install a fresh copy.'
                return
        # A previously unpacked Toolchain is usable without inventing a release
        # version or date. Never adopt an abandoned staging/new package folder.
        if self.root.exists():
            try:
                candidates = [path for path in self.root.rglob('*')
                              if path.name.lower() == 'datatool.exe' and path.is_file()
                              and not set(path.relative_to(self.root).parts) & {'.staging', 'packages'}]
                if len(candidates) == 1:
                    with candidates[0].open('rb') as stream:
                        valid = stream.read(2) == b'MZ'
                    if valid:
                        self._executable = candidates[0].resolve()
                        self._installed = {'version': None, 'releaseDate': None,
                                           'packageUpdatedAt': None, 'installedAt': None,
                                           'assetId': None, 'digest': None, 'legacy': True}
            except OSError as error:
                self._error = _error_message(error)

    def _require_owned_path(self, path):
        resolved = path.resolve()
        if resolved == self.root or not resolved.is_relative_to(self.root):
            raise ToolchainError('The toolchain staging or package folder points outside Avolition\'s toolchain directory.')
        return resolved

    def executable(self):
        with self._mutex:
            return self._executable if self._executable and self._executable.is_file() else None

    def snapshot(self):
        with self._mutex:
            executable = self.executable()
            return {
                'root': str(self.root), 'installed': deepcopy(self._installed),
                'latest': deepcopy(self._latest), 'executable': str(executable) if executable else None,
                'updateAvailable': (not executable or package_differs(self._installed, self._latest)
                                    if self._latest and not self._check_error else None),
                'checkedAt': self._checked_at, 'state': self._state,
                'busy': self._busy, 'error': self._error, 'checkError': self._check_error,
                'message': self._message,
            }

    def _set(self, state, message):
        with self._mutex:
            self._state, self._message = state, message

    def _start(self, operation, install=False):
        with self._mutex:
            if self._busy:
                raise ToolchainError('A Toolchain operation is already in progress.')
            if install and not self.activity_lock.acquire(blocking=False):
                raise ToolchainError('Wait for the active DataTool job to finish before installing or updating Toolchain.')
            self._busy = True
            self._error = None
            self._cancelled.clear()
            self._set('installing' if install else 'checking',
                      'Preparing Toolchain installation…' if install else 'Checking the official OWLib release…')

            def run():
                try:
                    operation()
                except Exception as error:
                    with self._mutex:
                        self._error = _error_message(error)
                        self._state = 'failed'
                        self._message = self._error
                        if not install:
                            self._check_error = self._error
                finally:
                    if install:
                        self.activity_lock.release()
                    with self._mutex:
                        self._busy = False

            self._thread = threading.Thread(target=run, name='Avolition-Toolchain', daemon=True)
            try:
                self._thread.start()
            except Exception:
                self._busy = False
                if install:
                    self.activity_lock.release()
                raise
            return self.snapshot()

    def check_async(self):
        return self._start(self._check)

    def install_async(self):
        return self._start(self._install, install=True)

    def wait(self, timeout=None):
        thread = self._thread
        if thread is not None:
            thread.join(timeout)
        return self.snapshot()

    def close(self):
        # A closed app can abandon a staging directory, but never its last
        # working package. Chunk boundaries also permit orderly cancellation.
        self._cancelled.set()

    def _request(self, url, json_response=False):
        headers = {'User-Agent': USER_AGENT}
        if json_response:
            headers['Accept'] = 'application/vnd.github+json'
            headers['X-GitHub-Api-Version'] = '2022-11-28'
        return self._opener(Request(url, headers=headers), timeout=30)

    def _check(self):
        with self._request(RELEASE_URL, True) as response:
            payload = response.read(4 * 1024 * 1024 + 1)
        if len(payload) > 4 * 1024 * 1024:
            raise ToolchainError('GitHub returned unexpectedly large release metadata.')
        try:
            metadata = release_metadata(json.loads(payload))
        except (ValueError, UnicodeError, TypeError) as error:
            raise ToolchainError('GitHub returned invalid release information. Try checking again later.') from error
        with self._mutex:
            self._latest = metadata
            self._checked_at = _utc_now()
            self._check_error = None
            self._state = 'ready'
            self._message = ('Toolchain package available.' if package_differs(self._installed, metadata)
                             else 'The installed package matches the latest official release.')

    def _check_cancelled(self):
        if self._cancelled.is_set():
            raise ToolchainError('Toolchain installation was interrupted. The installed copy is unchanged.')

    def _download(self, metadata, path):
        digest = metadata.get('digest')
        algorithm, expected = ('sha256', None)
        if digest:
            try:
                algorithm, expected = digest.split(':', 1)
                if algorithm not in ('sha256', 'sha512') or not re.fullmatch(r'[0-9a-fA-F]+', expected):
                    raise ValueError()
                if len(expected) != hashlib.new(algorithm).digest_size * 2:
                    raise ValueError()
            except (AttributeError, ValueError) as error:
                raise ToolchainError('GitHub supplied an unsupported or invalid package digest; installation was stopped.') from error
        checksum = hashlib.new(algorithm)
        size = 0
        with self._request(metadata['downloadUrl']) as response, path.open('xb') as output:
            while True:
                self._check_cancelled()
                chunk = response.read(1024 * 1024)
                if not chunk:
                    break
                size += len(chunk)
                if size > MAX_DOWNLOAD or size > metadata['assetSize']:
                    raise ToolchainError('The download size does not match the Toolchain release. Check for updates and retry.')
                output.write(chunk)
                checksum.update(chunk)
            output.flush()
            os.fsync(output.fileno())
        if size != metadata['assetSize']:
            raise ToolchainError('The Toolchain download is incomplete. Try installing again; the installed copy is unchanged.')
        if expected and checksum.hexdigest().lower() != expected.lower():
            raise ToolchainError('The Toolchain package digest does not match GitHub. The download was rejected; check for updates and retry.')
        return bool(expected)

    def _extract(self, archive, target):
        target.mkdir()
        with zipfile.ZipFile(archive) as package:
            members = package.infolist()
            if not members or len(members) > MAX_ENTRIES or sum(item.file_size for item in members) > MAX_EXPANDED:
                raise ToolchainError('The Toolchain archive is empty or exceeds the supported extraction limits.')
            seen = set()
            verified = []
            for member in members:
                parts = _safe_member(member.orig_filename)
                key = '/'.join(parts).casefold()
                if key in seen:
                    raise ToolchainError('The Toolchain archive contains duplicate or conflicting paths.')
                seen.add(key)
                mode = member.external_attr >> 16
                kind = stat.S_IFMT(mode)
                if kind not in (0, stat.S_IFREG, stat.S_IFDIR):
                    raise ToolchainError('Toolchain archives cannot contain symbolic links or special files.')
                destination = target.joinpath(*parts).resolve()
                if not destination.is_relative_to(target.resolve()):
                    raise ToolchainError('A Toolchain archive path escapes the extraction directory.')
                verified.append((member, destination))
            # Validate every path before writing the first member.
            for member, destination in verified:
                self._check_cancelled()
                if member.is_dir():
                    destination.mkdir(parents=True, exist_ok=True)
                else:
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    with package.open(member) as source, destination.open('xb') as output:
                        while chunk := source.read(1024 * 1024):
                            self._check_cancelled()
                            output.write(chunk)

    def _install(self):
        if self._latest is None or self._check_error:
            self._check()
        with self._mutex:
            metadata = deepcopy(self._latest)
        self._check_cancelled()
        identifier = uuid.uuid4().hex
        staging = self.root / '.staging' / identifier
        package = self.root / 'packages' / identifier
        temporary_pointer = self.root / f'.active-{identifier}.tmp'
        activated = False
        try:
            self._require_owned_path(staging)
            self._require_owned_path(package)
            staging.mkdir(parents=True)
            self._set('downloading', 'Downloading the complete Toolchain package…')
            verified_digest = self._download(metadata, staging / ASSET_NAME)
            self._set('extracting', 'Extracting Toolchain and its dependencies…')
            self._extract(staging / ASSET_NAME, staging / 'runtime')
            self._set('validating', 'Validating the DataTool runtime…')
            executable = _runtime_executable(staging / 'runtime')
            relative_executable = executable.relative_to(staging)
            metadata['installedAt'] = _utc_now()
            metadata['digestVerified'] = verified_digest
            (staging / 'installation.json').write_text(json.dumps(metadata, ensure_ascii=False, indent=2), encoding='utf-8')
            self._check_cancelled()
            package.parent.mkdir(parents=True, exist_ok=True)
            staging.replace(package)
            executable = package / relative_executable
            value = {'schema': 1, 'executable': executable.relative_to(self.root).as_posix(), 'package': metadata}
            with temporary_pointer.open('x', encoding='utf-8') as output:
                json.dump(value, output, ensure_ascii=False, indent=2)
                output.flush()
                os.fsync(output.fileno())
            self._check_cancelled()
            os.replace(temporary_pointer, self.root / 'active.json')
            activated = True
            with self._mutex:
                self._installed = metadata
                self._executable = executable
                self._state = 'ready'
                self._message = ('Toolchain installed and ready.' if verified_digest else
                                 'Toolchain installed and ready. GitHub did not provide a digest for this package.')
        finally:
            # These are internally generated UUID paths under our toolchain
            # root. No cleanup ever touches the active or a legacy runtime.
            if staging.exists():
                shutil.rmtree(self._require_owned_path(staging), ignore_errors=True)
            if not activated and package.exists():
                shutil.rmtree(self._require_owned_path(package), ignore_errors=True)
            temporary_pointer.unlink(missing_ok=True)
