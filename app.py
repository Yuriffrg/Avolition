"""Offline desktop animation browser. All asset access stays on this machine."""
from __future__ import annotations

import argparse
import copy
import functools
import json
import os
from pathlib import Path
import re
import secrets
import sys
import threading
import time
import traceback
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer

from owpreview.formats import load_asset, load_clip, read_clip_header
from owpreview.bone_counts import BoneCountIndex
from owpreview.library_scan import MAP_ASSET_FOLDERS, entity_sort_key, has_map_marker, inside_map_assets
from owpreview.textures import TextureStore
from owpreview.export_bundle import export_bundle, fbx_options
from owpreview.c4d_bridge import BridgeClient
from owpreview.duplicates import DuplicateIndex
from owpreview.workflow_paths import WorkflowPaths
from owpreview.toolchain_runtime import ToolchainManager
from owpreview.datatool import DataToolJobs, validate_paths, CATEGORIES
from owpreview.animation_analysis import AnalysisManager
from owpreview.clip_bindings import ClipBindingIndex
from owpreview.object_names import validate_name, validate_names, apply_object_names, object_name_keys
from owpreview.map_jobs import MapExportJobs


BASE = Path(__file__).resolve().parent
MAX_EXPORT_QUEUE = 1000
API_METHODS = ('bootstrap', 'browse_root', 'browse_output', 'set_output_folder', 'set_export_settings', 'export_animation', 'export_animation_set', 'c4d_status',
               'browse_entity', 'scan_root', 'load_entity', 'preview_entity', 'release_entity_preview', 'load_texture', 'load_animation', 'save_annotation', 'ping', 'close_app',
               'workflow_settings', 'use_library_directory', 'browse_workflow_directory', 'open_toolchain_output',
               'toolchain_status', 'toolchain_check', 'toolchain_install', 'toolchain_validate', 'toolchain_list',
               'toolchain_start', 'toolchain_cancel', 'analysis_start', 'analysis_status', 'analysis_index', 'analysis_detail',
               'read_clip_bindings', 'library_settings', 'export_queue_save', 'export_queue_remove',
               'export_queue_run', 'open_export_output', 'maps_settings', 'scan_maps', 'load_map', 'export_map',
               'export_map_start', 'export_map_status', 'browse_map', 'open_map_output', 'object_names', 'save_object_name')


def reply(function):
    @functools.wraps(function)
    def wrapped(*args, **kwargs):
        try:
            return {'ok': True, 'data': function(*args, **kwargs)}
        except Exception as error:
            return {'ok': False, 'error': str(error) or type(error).__name__}
    return wrapped


class BrowserAPI:
    def __init__(self, data_dir=None, initial_root=None, desktop_mode=False, app_dir=None):
        self._desktop_mode = desktop_mode
        self._server = None
        self._last_ping = time.monotonic()
        self._has_pinged = False
        self._lock = threading.RLock()
        self._queue_activity_lock = threading.Lock()
        self._queue_running = set()
        self._naming_assets = {}
        self._entity_preview_lock = threading.Lock()
        self._entity_preview_generation = 0
        self._map_exports = MapExportJobs()
        self._textures = TextureStore()
        self._duplicates = DuplicateIndex()
        self._clip_bindings = ClipBindingIndex()
        explicit_data_dir = (data_dir or os.environ.get('AVOLITION_DATA_DIR') or os.environ.get('WHITEHOUND_DATA_DIR') or
                             os.environ.get('OWIO_DATA_DIR') or os.environ.get('OWPREVIEW_DATA_DIR'))
        local_data = Path(os.environ.get('LOCALAPPDATA', str(Path.home())))
        self._data_dir = Path(explicit_data_dir or local_data / 'Avolition').resolve()
        self._data_dir.mkdir(parents=True, exist_ok=True)
        # Keep the installed companion plugin's shared queue; explicit profiles remain isolated.
        self._c4d_bridge = BridgeClient(self._data_dir / 'c4d-bridge' if explicit_data_dir else None)
        self._catalog_path = self._data_dir / 'catalog.json'
        self._catalog = {'lastRoot': '', 'annotations': {}, 'outputFolders': {},
                         'outputFolder': '', 'exportDirectories': [], 'exportedFiles': [],
                         'managedOutputDirectories': [], 'fbxOptions': fbx_options(), 'exportQueue': []}
        self._catalog_warning = None
        # Copy the previous catalog once. Originals remain available to the old app.
        if not explicit_data_dir and not self._catalog_path.exists():
            try:
                legacy = next((candidate for candidate in (local_data/'Whitehound/catalog.json', local_data/'OWIO/catalog.json',
                               local_data/'OWAnimationBrowser/catalog.json') if candidate.is_file()), None)
                if legacy is not None:
                    previous = legacy.read_text(encoding='utf-8')
                    saved = json.loads(previous)
                    if not isinstance(saved, dict) or not isinstance(saved.get('annotations', {}), dict):
                        raise ValueError('Invalid legacy library metadata')
                    with self._catalog_path.open('x', encoding='utf-8') as destination:
                        destination.write(previous)
            except FileExistsError:
                pass
            except (ValueError, OSError) as error:
                self._catalog_warning = 'Could not migrate saved labels: ' + str(error)
        if self._catalog_path.exists():
            try:
                saved = json.loads(self._catalog_path.read_text(encoding='utf-8'))
                if not isinstance(saved, dict) or not isinstance(saved.get('annotations', {}), dict):
                    raise ValueError('Invalid library metadata')
                if not isinstance(saved.get('outputFolders', {}), dict):
                    raise ValueError('Invalid output-folder metadata')
                for field in ('exportDirectories', 'exportedFiles', 'managedOutputDirectories'):
                    if not isinstance(saved.get(field, []), list):
                        raise ValueError('Invalid exported-file metadata')
                self._catalog.update(saved)
            except (ValueError, OSError) as error:
                self._catalog_warning = 'Could not read saved labels: ' + str(error)
        try:
            saved_options = self._catalog.get('fbxOptions')
            migrate_formats = isinstance(saved_options, dict) and 'clipEnabled' not in saved_options
            if migrate_formats:
                # The old raw clip copy was implicit. Introduce the three explicit
                # choices once without losing mesh/axis settings or library data.
                saved_options = dict(saved_options, clipEnabled=False, enabled=False, c4dEnabled=True)
            self._catalog['fbxOptions'] = fbx_options(saved_options)
        except ValueError:
            self._catalog['fbxOptions'] = fbx_options()
            self._catalog_warning = 'Saved FBX options were invalid and have been reset to their defaults.'
            migrate_formats = False
        # Move the previous default once. Subsequent explicit sort choices must
        # survive restart, including a user choosing Most tracks first again.
        sort_revision = self._catalog.get('librarySortRevision', 0)
        old_sort = self._catalog.get('libraryOptions')
        migrate_library_sort = (not isinstance(sort_revision, int) or sort_revision < 1) and \
            isinstance(old_sort, dict) and old_sort.get('sort') == 'tracks-desc'
        if migrate_library_sort:
            self._catalog['libraryOptions'] = dict(old_sort, sort='object-match')
        self._catalog['librarySortRevision'] = max(sort_revision, 1) if isinstance(sort_revision, int) else 1
        root = initial_root or self._catalog['lastRoot']
        self._root = Path(root).resolve() if root else None
        if initial_root:
            self._catalog['lastRoot'] = str(initial_root)
        self._workflow = WorkflowPaths(self._catalog, app_dir)
        self._toolchain_lock = threading.Lock()
        self._toolchain = ToolchainManager(self._workflow.toolchain, activity_lock=self._toolchain_lock)
        self._datatool = DataToolJobs(self._data_dir / 'datatool', operation_lock=self._toolchain_lock)
        self._analysis = AnalysisManager(self._data_dir / 'animation-analysis',
            annotations=lambda: dict(self._catalog['annotations']),
            installed=lambda: self._toolchain.snapshot().get('installed'),
            excluded=lambda: self._catalog.get('managedOutputDirectories', []) + self._catalog.get('exportedFiles', []),
            labels_available=lambda: not (self._catalog_warning or '').startswith('Could not read saved labels'))
        if migrate_formats or migrate_library_sort:
            try:
                self._save()
            except OSError as error:
                self._catalog_warning = 'Could not save updated export defaults: ' + str(error)

    def _save(self):
        with self._lock:
            temporary = self._catalog_path.with_suffix('.tmp')
            temporary.write_text(json.dumps(self._catalog, ensure_ascii=False, indent=2), encoding='utf-8')
            temporary.replace(self._catalog_path)

    def _key(self, path):
        return os.path.normcase(str(Path(path).resolve()))

    def _annotation(self, path):
        return self._catalog['annotations'].get(self._key(path), {'name': '', 'notes': '', 'favorite': False})

    def _output_folder(self):
        if self._root:
            return self._catalog['outputFolders'].get(self._key(self._root), str(self._root/'Renamed Animations'))
        return self._catalog['outputFolder']

    def _folder_path(self, value):
        clean = str(value or '').strip()
        if len(clean) >= 2 and clean[0] == clean[-1] and clean[0] in ('"', "'"):
            clean = clean[1:-1].strip()
        if not clean:
            raise ValueError('Set an output folder before saving a renamed copy.')
        path = Path(os.path.expandvars(clean)).expanduser()
        if not path.is_absolute():
            raise ValueError('Use the full path to the output folder.')
        path = path.resolve()
        if path.exists() and not path.is_dir():
            raise ValueError('The output path is a file. Choose a folder.')
        return path

    def _copy_name(self, nickname):
        name = str(nickname or '').strip()
        if name.lower().endswith('.owanimclip'):
            name = name[:-len('.owanimclip')]
        if not name or name in ('.', '..'):
            raise ValueError('Enter a nickname to name the animation copy.')
        if re.search(r'[<>:"/\\|?*\x00-\x1f]', name) or name.endswith(('.', ' ')):
            raise ValueError('The nickname contains characters that Windows cannot use in a filename.')
        if re.fullmatch(r'(?i)(CON|PRN|AUX|NUL|COM[1-9¹²³]|LPT[1-9¹²³])', name.split('.')[0]):
            raise ValueError('That nickname is a reserved Windows filename. Choose another name.')
        if len(name.encode('utf-16-le')) // 2 > 180:
            raise ValueError('Keep the nickname within 180 characters for the exported filename.')
        return name + '.owanimclip'

    @reply
    def bootstrap(self):
        queue_warning = None
        try:
            queue = self._export_queue_entries()
        except ValueError as error:
            # A damaged new export list must not hide the user's existing
            # library and annotations. Mutating queue APIs still reject it.
            queue, queue_warning = [], str(error)
        return {'lastRoot': self._catalog['lastRoot'], 'catalog': self._catalog['annotations'],
                'outputFolder': self._catalog.get('outputFolder') or self._output_folder(),
                'warning': ' '.join(value for value in (self._catalog_warning, queue_warning) if value) or None,
                'fbxOptions': fbx_options(self._catalog.get('fbxOptions')),
                'workflow': self._workflow.settings(),
                'libraryOptions': self._library_options(),
                'exportQueue': queue,
                'maps': self._map_settings(),
                'mapExport': self._map_exports.snapshot(),
                'version': '0.8.24', 'appName': 'Avolition', 'browserMode': not self._desktop_mode}

    def _map_settings(self):
        saved = self._catalog.get('maps', {})
        if not isinstance(saved, dict):
            saved = {}
        return {'root': saved.get('root') or str(self._workflow.library),
                'outputFolder': saved.get('outputFolder') or self._catalog.get('outputFolder') or str(self._workflow.library / 'Map exports'),
                'bakeTextures': saved.get('bakeTextures', True) is not False}

    @reply
    def maps_settings(self, changes=None):
        from owpreview.workflow_paths import absolute_folder
        with self._lock:
            settings = self._map_settings()
            if changes is not None:
                if not isinstance(changes, dict) or set(changes) - {'root', 'outputFolder', 'bakeTextures'}:
                    raise ValueError('Invalid map workspace settings.')
                for field in ('root', 'outputFolder'):
                    if field in changes:
                        settings[field] = absolute_folder(changes[field], field)
                        if not settings[field]:
                            raise ValueError('Choose a full folder path.')
                if 'bakeTextures' in changes:
                    if not isinstance(changes['bakeTextures'], bool):
                        raise ValueError('Bake textures must be on or off.')
                    settings['bakeTextures'] = changes['bakeTextures']
                previous = self._catalog.get('maps')
                self._catalog['maps'] = settings
                try:
                    self._save()
                except Exception:
                    if previous is None: self._catalog.pop('maps', None)
                    else: self._catalog['maps'] = previous
                    raise
            return settings

    @reply
    def scan_maps(self, root):
        from owpreview.workflow_paths import absolute_folder
        resolved = absolute_folder(root, 'Map library source')
        if not resolved:
            raise ValueError('Choose a full Map library source folder path.')
        folder = Path(resolved)
        if not folder.is_dir():
            raise ValueError('Map Library source is unavailable. Choose an existing exported asset folder.')
        maps, warnings = [], []
        excluded = [Path(value) for value in self._catalog.get('managedOutputDirectories', [])]
        def unreadable(error):
            if len(warnings) < 50: warnings.append(str(error))
        for parent, directories, files in os.walk(folder, followlinks=False, onerror=unreadable):
            current = Path(parent)
            directories[:] = [name for name in directories if not (current/name).is_symlink()
                and not any((current/name).resolve().is_relative_to(output) for output in excluded)]
            for filename in files:
                if Path(filename).suffix.lower() != '.owmap': continue
                item = current/filename
                try:
                    maps.append({'path': str(item.resolve()), 'name': item.stem,
                                 'relativePath': str(item.relative_to(folder)), 'size': item.stat().st_size})
                except OSError as error:
                    warnings.append(f'{filename}: {error}')
                if len(maps) >= 10000:
                    raise ValueError('This folder has more than 10,000 map files. Choose a smaller Library source.')
        result = BrowserAPI.maps_settings.__wrapped__(self, {'root': str(folder)})
        return {'root': result['root'], 'maps': sorted(maps, key=lambda item: item['relativePath'].casefold()), 'warnings': warnings}

    def _register_names(self, path, asset):
        # Retain only editable names, never a second copy of loaded geometry.
        prototypes = asset.get('meshPrototypes', {})
        models = []
        for model in asset.get('models', []):
            prototype = prototypes.get(model.get('meshPrototype'), {})
            models.append({'id': model['id'], 'name': model.get('name', ''),
                           'meshes': [{'name': mesh.get('name', ''), 'sourceMeshIndex': mesh.get('sourceMeshIndex', index)}
                                      for index, mesh in enumerate(model.get('meshes', prototype.get('meshes', [])))]})
        key = self._key(path)
        with self._lock:
            self._naming_assets[key] = {'name': asset.get('name', ''), 'models': models}
            while len(self._naming_assets) > 8:
                del self._naming_assets[next(iter(self._naming_assets))]
        names = BrowserAPI.object_names.__wrapped__(self, path)
        known = object_name_keys(self._naming_assets[key])
        # Changed source structure must not block previewing or current exports.
        accepted = {node: name for node, name in names.items() if node in known}
        asset['objectNames'] = accepted
        return asset

    @reply
    def object_names(self, path):
        if not isinstance(path, str) or not Path(path).is_absolute():
            raise ValueError('Load an asset before editing object names.')
        with self._lock:
            saved = self._catalog.get('objectNames', {})
            if not isinstance(saved, dict):
                raise ValueError('Saved object names are invalid. Restore the catalog before editing names.')
            return validate_names(saved.get(self._key(path), {}))

    @reply
    def save_object_name(self, path, node, name):
        name = validate_name(name)
        key = self._key(path)
        with self._lock:
            asset = self._naming_assets.get(key)
            if asset is None:
                raise ValueError('Reload this asset before editing its object names.')
            names = BrowserAPI.object_names.__wrapped__(self, path)
            names.update(validate_names({node: name}, asset))
            previous = self._catalog.get('objectNames', {})
            self._catalog['objectNames'] = dict(previous, **{key: names})
            try: self._save()
            except Exception:
                self._catalog['objectNames'] = previous
                raise
            return copy.deepcopy(names)

    @reply
    def load_map(self, path, root=None):
        from owpreview.map_formats import load_map
        asset = load_map(path, root_path=root or self._map_settings()['root'])
        self._textures.register(asset.get('textures', {}), owner='maps')
        return self._register_names(path, asset)

    @reply
    def browse_map(self):
        raise RuntimeError('Map file dialogs are provided by the Avolition desktop window.')

    @reply
    def open_map_output(self):
        folder = self._folder_path(self._map_settings()['outputFolder'])
        if not folder.is_dir():
            raise ValueError('Map output folder is unavailable. Choose an existing folder or export first.')
        return str(folder)

    def _map_export_request(self, path, name, root=None, output=None, bake_textures=True):
        """Capture all user-controlled choices before the worker can begin."""
        from owpreview.workflow_paths import absolute_folder
        filename = Path(self._copy_name(name)).stem
        if not isinstance(bake_textures, bool):
            raise ValueError('Bake textures must be on or off.')
        with self._lock:
            names = BrowserAPI.object_names.__wrapped__(self, path)
            settings = self._map_settings()
        folder = self._folder_path(output or settings['outputFolder'])
        return dict(path=str(Path(path).resolve()), name=filename, names=copy.deepcopy(names),
                    root=absolute_folder(root or settings['root'], 'Map library source'),
                    folder=folder, folderExisted=folder.exists(), bakeTextures=bake_textures,
                    bridge=self._c4d_bridge)

    def _run_map_export(self, request, progress):
        from owpreview.map_export import export_map
        from owpreview.map_formats import load_map
        progress('Loading map…')
        asset = load_map(request['path'], root_path=request['root'])
        known = object_name_keys(asset)
        names = {node: value for node, value in request['names'].items() if node in known}
        asset = apply_object_names(asset, names)
        result = export_map(asset, request['folder'], request['name'],
                            bake_textures=request['bakeTextures'], bridge=request['bridge'],
                            progress=progress)
        progress('Recording exported map…')
        with self._lock:
            self._record_export(request['folder'], request['folderExisted'], result)
        return result

    @reply
    def export_map(self, path, name, root=None, output=None, bake_textures=True):
        request = self._map_export_request(path, name, root, output, bake_textures)
        return self._map_exports.run(lambda progress: self._run_map_export(request, progress))

    @reply
    def export_map_start(self, path, name, root=None, output=None, bake_textures=True):
        request = self._map_export_request(path, name, root, output, bake_textures)
        return self._map_exports.start(lambda progress: self._run_map_export(request, progress))

    @reply
    def export_map_status(self, job_id=None):
        return self._map_exports.snapshot(job_id)

    def _library_options(self):
        saved = self._catalog.get('libraryOptions', {})
        if not isinstance(saved, dict):
            saved = {}
        sorts = ('object-match', 'tracks-desc', 'tracks-asc', 'name', 'nicknamed-first',
                 'size-desc', 'size-asc', 'duration-desc', 'duration-asc')
        return {'sort': saved.get('sort') if saved.get('sort') in sorts else 'object-match',
                'hideDuplicates': saved.get('hideDuplicates', True) is not False}

    @reply
    def library_settings(self, changes=None):
        with self._lock:
            options = self._library_options()
            if changes is not None:
                if not isinstance(changes, dict) or set(changes) - {'sort', 'hideDuplicates'}:
                    raise ValueError('Invalid animation library settings.')
                if 'sort' in changes and changes['sort'] not in ('object-match', 'tracks-desc', 'tracks-asc',
                        'name', 'nicknamed-first', 'size-desc', 'size-asc', 'duration-desc', 'duration-asc'):
                    raise ValueError('Unknown animation sorting option.')
                if 'hideDuplicates' in changes and not isinstance(changes['hideDuplicates'], bool):
                    raise ValueError('Hide duplicates must be on or off.')
                options.update(changes)
                self._catalog['libraryOptions'] = options
                self._save()
            return options

    @reply
    def read_clip_bindings(self, paths):
        return self._clip_bindings.read(paths)

    def _export_queue_entries(self):
        entries = self._catalog.get('exportQueue', [])
        if not isinstance(entries, list):
            raise ValueError('The saved export list is invalid. Restore a valid catalog.json before editing it.')
        seen = set()
        for entry in entries:
            valid = isinstance(entry, dict) and all(isinstance(entry.get(key), str) and entry[key]
                for key in ('id', 'name', 'entityPath', 'rootPath'))
            if valid:
                valid = bool(re.fullmatch(r'[0-9a-f]{32}', entry['id'])) and entry['id'] not in seen
                assignments = entry.get('assignments')
                valid = valid and isinstance(assignments, list) and 1 <= len(assignments) <= 512
            if valid:
                models = set()
                for assignment in assignments:
                    if (not isinstance(assignment, dict)
                        or not isinstance(assignment.get('modelId'), str) or not assignment['modelId']
                        or assignment['modelId'] in models or not isinstance(assignment.get('clipPath'), str)
                        or not assignment['clipPath']):
                        valid = False
                        break
                    models.add(assignment['modelId'])
            if not valid:
                raise ValueError('The saved export list contains invalid entries. Restore a valid catalog.json before editing it.')
            validate_names(entry.get('objectNames', {}))
            seen.add(entry['id'])
        # Do not require source files on restore: unavailable entries must remain
        # visible so the user can retry after reconnecting their asset drive.
        return copy.deepcopy(entries)

    @staticmethod
    def _queue_id(value):
        if not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{32}', value):
            raise ValueError('Choose an existing entry from the export list.')
        return value

    def _save_export_queue(self, entries):
        previous = self._catalog.get('exportQueue', [])
        self._catalog['exportQueue'] = entries
        try:
            self._save()
        except Exception:
            self._catalog['exportQueue'] = previous
            raise

    @reply
    def export_queue_save(self, entry):
        """Save a named scene snapshot locally without exporting any files."""
        if not isinstance(entry, dict) or set(entry) - {'id', 'name', 'entityPath', 'rootPath', 'assignments', 'objectNames'}:
            raise ValueError('Invalid export list entry.')
        ident = self._queue_id(entry['id']) if 'id' in entry else secrets.token_hex(16)
        name = entry.get('name')
        if not isinstance(name, str):
            raise ValueError('Enter a name for this animation setup.')
        name = name.strip()
        if name.lower().endswith('.c4d'):
            name = name[:-4]
        name = Path(self._copy_name(name)).stem
        entity, root = entry.get('entityPath'), entry.get('rootPath')
        if not isinstance(entity, str) or not Path(entity).is_absolute() or Path(entity).suffix.lower() not in ('.owentity', '.owmdl'):
            raise ValueError('Load an entity or model before adding an export.')
        if not isinstance(root, str) or not Path(root).is_absolute() or not Path(root).is_dir():
            raise ValueError('Choose an existing Library Source folder before adding an export.')
        assignments = entry.get('assignments')
        if not isinstance(assignments, list) or not 1 <= len(assignments) <= 512:
            raise ValueError('Assign at least one animation to an object before adding an export.')
        captured, seen = [], set()
        for assignment in assignments:
            if not isinstance(assignment, dict) or set(assignment) != {'modelId', 'clipPath'}:
                raise ValueError('Invalid animation assignment.')
            model_id, path = assignment.get('modelId'), assignment.get('clipPath')
            if not isinstance(model_id, str) or not model_id or len(model_id) > 128 or model_id in seen:
                raise ValueError('Each object must have one unique animation assignment.')
            if not isinstance(path, str) or not Path(path).is_absolute() or Path(path).suffix.lower() != '.owanimclip':
                raise ValueError('Each assigned animation must be an existing .owanimclip file.')
            read_clip_header(path)
            captured.append({'modelId': model_id, 'clipPath': str(Path(path).resolve())})
            seen.add(model_id)
        asset = load_asset(entity, root_path=root)
        models = {model['id']: model for model in asset['models'] if model.get('bones')}
        if any(assignment['modelId'] not in models for assignment in captured):
            raise ValueError('An assigned object is not a loaded skeleton. Reload the entity and choose its animation again.')
        record = {'id': ident, 'name': name, 'entityPath': str(Path(entity).resolve()),
                  'rootPath': str(Path(root).resolve()), 'assignments': captured,
                  'objectNames': validate_names(entry['objectNames'] if 'objectNames' in entry else
                                                self._register_names(entity, asset)['objectNames'], asset)}
        with self._lock:
            entries = self._export_queue_entries()
            existing = next((index for index, item in enumerate(entries) if item['id'] == ident), None)
            if 'id' in entry and existing is None:
                raise ValueError('This export list entry no longer exists. Add it again.')
            if existing is None:
                if len(entries) >= MAX_EXPORT_QUEUE:
                    raise ValueError(f'The export list is limited to {MAX_EXPORT_QUEUE} entries. Remove completed entries first.')
                entries.append(record)
            else:
                entries[existing] = record
            self._save_export_queue(entries)
            return {'entry': copy.deepcopy(record), 'entries': copy.deepcopy(entries)}

    @reply
    def export_queue_remove(self, ident):
        ident = self._queue_id(ident)
        with self._lock:
            entries = self._export_queue_entries()
            remaining = [entry for entry in entries if entry['id'] != ident]
            if len(remaining) == len(entries):
                raise ValueError('This export list entry no longer exists.')
            self._save_export_queue(remaining)
            return {'entries': copy.deepcopy(remaining)}

    @reply
    def export_queue_run(self, ident, output_folder=None, options=None):
        """Explicitly export a stored full-entity setup; never a raw clip copy."""
        ident = self._queue_id(ident)
        if options is not None and not isinstance(options, dict):
            raise ValueError('Invalid scene export options.')
        # Keep duplicate-job protection separate from the catalog lock, which
        # the native exporter holds while Cinema creates the document.
        with self._queue_activity_lock:
            if ident in self._queue_running:
                raise ValueError('This animation setup is already exporting.')
            self._queue_running.add(ident)
        try:
            with self._lock:
                snapshot = next((entry for entry in self._export_queue_entries() if entry['id'] == ident), None)
                if snapshot is None:
                    raise ValueError('This export list entry no longer exists.')
                destination = (self._catalog.get('outputFolder') or self._output_folder()) if output_folder is None else output_folder
            settings = dict(options or {}, rootPath=snapshot['rootPath'], objectNames=snapshot.get('objectNames', {}))
            return BrowserAPI.export_animation_set.__wrapped__(self, snapshot['entityPath'], snapshot['assignments'],
                                                                snapshot['name'], destination, settings)
        finally:
            with self._queue_activity_lock:
                self._queue_running.discard(ident)

    @reply
    def open_export_output(self):
        """The desktop host opens only the saved common export destination."""
        folder = self._folder_path(self._catalog.get('outputFolder') or self._output_folder())
        if not folder.is_dir():
            raise ValueError('The export output folder is unavailable. Choose an existing folder or export first.')
        return str(folder)

    @reply
    def workflow_settings(self, changes=None):
        with self._lock:
            if changes is not None:
                result = self._workflow.update(changes)
                self._save()
                return result
            return self._workflow.settings()

    @reply
    def use_library_directory(self):
        with self._lock:
            result = self._workflow.use_library()
            self._save()
            return result

    @reply
    def browse_workflow_directory(self, kind):
        raise RuntimeError('Folder dialogs are provided by the Avolition desktop window.')

    @reply
    def open_toolchain_output(self):
        # The desktop host opens only this configured directory with shell.openPath.
        path = Path(self._workflow.settings()['outputDirectory'])
        if not path.is_dir():
            raise ValueError('Output Directory is not available. Choose an existing folder or export first.')
        return str(path)

    def _toolchain_snapshot(self):
        return {'runtime': self._toolchain.snapshot(), 'job': self._datatool.snapshot(),
                'capabilities': {'categories': CATEGORIES}}

    @reply
    def toolchain_status(self):
        return self._toolchain_snapshot()

    @reply
    def toolchain_check(self):
        self._toolchain.check_async()
        return self._toolchain_snapshot()

    def _check_toolchain_location(self):
        configured = self._workflow.settings()['overwatchInstall']
        game = Path(configured) if configured else None
        if game and (game / '.flavor.info').is_file():
            game = game.parent
        root = self._workflow.toolchain
        inside_game = game is not None and root.is_relative_to(game)
        for parent in root.parents:
            if (parent / 'data').is_dir() and ((parent / '.build.info').is_file() or (parent / 'data/.build.config').is_file()):
                inside_game = True
                break
        if inside_game:
            raise ValueError('Move the Avolition folder outside the Overwatch installation before using Toolchain.')

    @reply
    def toolchain_install(self):
        self._check_toolchain_location()
        self._toolchain.install_async()
        return self._toolchain_snapshot()

    @reply
    def toolchain_validate(self):
        settings = self._workflow.settings()
        validation = validate_paths(settings['overwatchInstall'], settings['outputDirectory'])
        output = Path(settings['outputDirectory'])
        if output.is_relative_to(self._workflow.toolchain) or self._workflow.toolchain.is_relative_to(output):
            validation['ok'] = False
            validation['errors']['outputDirectory'] = 'Choose the library or another asset folder separate from the Toolchain runtime.'
        return validation

    def _datatool_executable(self):
        self._check_toolchain_location()
        executable = self._toolchain.executable()
        if not executable:
            raise ValueError('Install Toolchain before loading assets or exporting.')
        return executable

    @reply
    def toolchain_list(self, options=None):
        if options is not None and not isinstance(options, dict):
            raise ValueError('Invalid Toolchain listing options.')
        workflow = (options or {}).get('workflow', 'heroes')
        self._datatool.start_catalog(self._datatool_executable(), self._workflow.settings()['overwatchInstall'], workflow=workflow)
        return self._toolchain_snapshot()

    @reply
    def toolchain_start(self, selection):
        settings = self._workflow.settings()
        validation = self.toolchain_validate()
        if not validation['ok']:
            raise ValueError(validation['error'])
        if not validation['data']['ok']:
            raise ValueError(' '.join(validation['data']['errors'].values()))
        self._datatool.start_export(self._datatool_executable(), settings['overwatchInstall'],
                                   settings['outputDirectory'], selection)
        return self._toolchain_snapshot()

    @reply
    def toolchain_cancel(self):
        self._datatool.cancel()
        return self._toolchain_snapshot()

    def shutdown(self):
        self._map_exports.close()
        self._datatool.close()
        self._toolchain.close()

    @reply
    def analysis_start(self, root):
        return self._analysis.start(root)

    @reply
    def analysis_status(self, root):
        return self._analysis.status(root)

    @reply
    def analysis_index(self, root):
        return self._analysis.index(root)

    @reply
    def analysis_detail(self, path, root=None):
        return self._analysis.detail(path, root)

    @reply
    def set_export_settings(self, options):
        value = fbx_options(options)
        with self._lock:
            self._catalog['fbxOptions'] = value
            self._save()
        return value

    @reply
    def set_output_folder(self, value):
        path = self._folder_path(value)
        with self._lock:
            self._catalog['outputFolder'] = str(path)
            if self._root:
                self._catalog['outputFolders'][self._key(self._root)] = str(path)
            self._save()
        return {'outputFolder': str(path)}

    @reply
    def browse_output(self):
        raise RuntimeError('Folder dialogs are provided by the Avolition desktop window.')

    @reply
    def c4d_status(self):
        return self._c4d_bridge.status()

    @reply
    def export_animation(self, path, nickname, output_folder=None, options=None):
        settings = fbx_options(options)
        if not any(settings[key] for key in ('clipEnabled', 'enabled', 'c4dEnabled')):
            return {'skipped': True, 'files': [], 'path': None, 'name': None}
        source = Path(path).resolve()
        if source.suffix.lower() != '.owanimclip':
            raise ValueError('Select an .owanimclip animation to copy.')
        read_clip_header(source)
        filename = self._copy_name(nickname)
        selected_id = (options or {}).get('selectedModelId')
        scoped_asset = None
        if selected_id:
            if not isinstance(selected_id, str) or len(selected_id) > 128:
                raise ValueError('Invalid selected object.')
            entity_path = (options or {}).get('entityPath')
            root_path = (options or {}).get('rootPath') or self._root
            if not isinstance(entity_path, str) or not Path(entity_path).is_absolute():
                raise ValueError('Load an entity and select an object before exporting it.')
            if root_path and not Path(root_path).is_absolute():
                raise ValueError('Invalid export root.')
            from owpreview.c4d_export import select_model_asset
            scoped_asset = select_model_asset(load_asset(entity_path, root_path=root_path), selected_id)
            model = scoped_asset['models'][0]
            prefix = re.sub(r'\.(?:[0-9a-f]{3}|owmdl)$', '', model.get('name') or selected_id, flags=re.I)
            filename = self._copy_name(prefix + '_' + nickname)
        folder = self._folder_path(self._output_folder() if output_folder is None else output_folder)
        desired = folder/filename
        if settings['clipEnabled'] and self._key(source) == self._key(desired):
            raise ValueError('The destination is the same source file. Choose a different output folder or nickname.')
        if settings['enabled'] or settings['c4dEnabled']:
            return self._export_fbx_animation(source, folder, filename, options or {}, settings, scoped_asset=scoped_asset)
        with self._lock:
            folder_existed = folder.exists()
            result = export_bundle(source, folder, filename, None, ('clip',))
            self._record_export(folder, folder_existed, result)
            return result

    def _record_export(self, folder, folder_existed, result):
        if self._key(folder) not in self._catalog['exportDirectories']:
            self._catalog['exportDirectories'].append(self._key(folder))
        for path in result['files']:
            key = self._key(path)
            if key not in self._catalog['exportedFiles']:
                self._catalog['exportedFiles'].append(key)
        if not folder_existed and self._key(folder) not in self._catalog['managedOutputDirectories']:
            self._catalog['managedOutputDirectories'].append(self._key(folder))
        self._save()

    @reply
    def export_animation_set(self, entity_path, assignments, scene_name, output_folder=None, options=None):
        """Publish one complete C4D scene from explicit per-object assignments.

        This is independent of nickname-triggered selected-object copies. The
        complete entity hierarchy carries attachment motion; source clips are
        sampled only on their own rigs and never merged by shared bone names.
        """
        from owpreview.c4d_export import make_set_payload
        from owpreview.export_textures import bake_materials
        if options is not None and not isinstance(options, dict):
            raise ValueError('Invalid scene export options.')
        options = dict(options or {})
        settings = fbx_options(dict(options, clipEnabled=False, enabled=False, c4dEnabled=True))
        if not isinstance(entity_path, str) or not Path(entity_path).is_absolute():
            raise ValueError('Load an entity before exporting a combined animation.')
        if not isinstance(assignments, list) or not assignments or len(assignments) > 512:
            raise ValueError('Assign at least one animation to a loaded object before exporting.')
        root_path = options.get('rootPath') or self._root
        if root_path and not Path(root_path).is_absolute():
            raise ValueError('Invalid export root.')
        name = str(scene_name or '').strip()
        if name.lower().endswith('.c4d'):
            name = name[:-4]
        filename = self._copy_name(name)
        folder = self._folder_path(self._output_folder() if output_folder is None else output_folder)
        captured = []
        seen = set()
        for assignment in assignments:
            if not isinstance(assignment, dict):
                raise ValueError('Invalid animation assignment.')
            model_id, clip_path = assignment.get('modelId'), assignment.get('clipPath')
            if not isinstance(model_id, str) or not model_id or len(model_id) > 128 or model_id in seen:
                raise ValueError('Each loaded object must have one unique animation assignment.')
            if not isinstance(clip_path, str) or not Path(clip_path).is_absolute() or Path(clip_path).suffix.lower() != '.owanimclip':
                raise ValueError('Each assigned animation must be an existing .owanimclip file.')
            seen.add(model_id)
            captured.append({'modelId': model_id, 'clip': load_clip(clip_path)})
        asset = load_asset(entity_path, root_path=root_path)
        names = options['objectNames'] if 'objectNames' in options else self._register_names(entity_path, asset)['objectNames']
        asset = apply_object_names(asset, names)
        # Validate all models, clips, timing and hierarchy before contacting
        # Cinema or creating the output folder. Reuse this sampled payload.
        payload = make_set_payload(asset, captured, {}, include_mesh=settings['includeMesh'],
                                   root_motion=settings['rootMotion'], name=Path(filename).stem)
        status = self._c4d_bridge.status()
        if not status['connected'] or not status.get('octane'):
            raise RuntimeError(status['message'])
        with self._lock:
            folder_existed = folder.exists()
            def build(destinations, texture_folder):
                if settings['includeMesh'] and settings['bakeTextures']:
                    baked = bake_materials(asset, texture_folder)
                else:
                    baked = {'materials': {model['id']: {str(k): dict(v, maps={})
                        for k, v in model.get('materials', {}).items()} for model in asset['models']}, 'warnings': []}
                payload['materials'] = baked['materials']
                result = self._c4d_bridge.export(payload, destinations['c4d'], texture_folder)
                warnings = payload.get('warnings', []) + baked.get('warnings', []) + result.get('warnings', [])
                for entry in captured:
                    warnings += entry['clip'].get('warnings', [])
                return {'warnings': list(dict.fromkeys(warnings))}
            result = export_bundle(Path(captured[0]['clip']['path']), folder, filename, build, ('c4d',))
            result['assignments'] = payload['animation'].get('assignments', [])
            result['fps'] = payload['animation']['fps']
            result['lastFrame'] = payload['animation']['lastFrame']
            self._record_export(folder, folder_existed, result)
            return result

    def _export_fbx_animation(self, source, folder, filename, options, settings, scoped_asset=None):
        from owpreview.export_textures import bake_materials
        from owpreview.fbx import export_fbx
        entity_path = str(options.get('entityPath') or '')
        if not entity_path or not Path(entity_path).is_absolute():
            raise ValueError('Load an entity or model before saving an FBX or C4D copy.')
        rig_id = options.get('rigId', 'auto')
        if scoped_asset is not None:
            rig_id = scoped_asset['models'][0]['id']
        if not isinstance(rig_id, str):
            raise ValueError('Invalid FBX animation target.')
        root_path = options.get('rootPath') or (str(self._root) if self._root else None)
        if root_path and (not isinstance(root_path, str) or not Path(root_path).is_absolute()):
            raise ValueError('Invalid FBX export root.')
        if settings['c4dEnabled']:
            status = self._c4d_bridge.status()
            if not status['connected'] or not status.get('octane'):
                raise RuntimeError(status['message'])
        with self._lock:
            # Reload from the captured path, never whichever entity the user
            # happened to select after starting this export.
            asset = scoped_asset if scoped_asset is not None else load_asset(entity_path, root_path=root_path)
            clip = load_clip(source)
            folder_existed = folder.exists()

            def build(destinations, texture_folder):
                baked = {'materials': {}, 'warnings': []}
                if settings['includeMesh'] and settings['bakeTextures']:
                    baked = bake_materials(asset, texture_folder)
                elif settings['c4dEnabled']:
                    # No texture export: retain material colors/factors without
                    # writing references to original source images.
                    baked['materials'] = {model['id']: {str(key): dict(value, maps={})
                        for key, value in model.get('materials', {}).items()} for model in asset['models']}
                warnings = asset.get('warnings', []) + clip.get('warnings', []) + baked.get('warnings', [])
                if 'fbx' in destinations:
                    result = export_fbx(destinations['fbx'], asset, clip, up_axis=settings['upAxis'],
                                        include_mesh=settings['includeMesh'], root_motion=settings['rootMotion'],
                                        materials=baked['materials'], clip_name=Path(filename).stem, rig_id=rig_id)
                    warnings += result.get('warnings', [])
                if 'c4d' in destinations:
                    from owpreview.c4d_export import make_payload
                    payload = make_payload(asset, clip, baked['materials'], include_mesh=settings['includeMesh'],
                                           root_motion=settings['rootMotion'], clip_name=Path(filename).stem, rig_id=rig_id)
                    result = self._c4d_bridge.export(payload, destinations['c4d'], texture_folder)
                    warnings += payload.get('warnings', []) + result.get('warnings', [])
                result = {'warnings': list(dict.fromkeys(warnings))}
                return result

            formats = [kind for kind, enabled in [('clip', settings['clipEnabled']),
                       ('fbx', settings['enabled']), ('c4d', settings['c4dEnabled'])] if enabled]
            result = export_bundle(source, folder, filename, build, formats)
            self._record_export(folder, folder_existed, result)
            return result

    @reply
    def ping(self):
        self._last_ping = time.monotonic()
        self._has_pinged = True
        return True

    @reply
    def close_app(self):
        if self._server:
            threading.Timer(0.3, self._server.shutdown).start()
        return True

    @reply
    def browse_root(self):
        raise RuntimeError('Folder dialogs are provided by the Avolition desktop window.')

    @reply
    def browse_entity(self):
        raise RuntimeError('File dialogs are provided by the Avolition desktop window.')

    @reply
    def scan_root(self, value, activate=True):
        clean = str(value).strip().strip('"')
        if not clean:
            raise ValueError('Paste the full path to your extracted files first.')
        path = Path(os.path.expandvars(clean)).expanduser().resolve()
        if not path.is_dir():
            raise ValueError('Root folder does not exist. Paste the full path to your extracted files.')
        with self._lock:
            first_root = self._root is None
            root_key = self._key(path)
            if activate:
                self._root = path
                self._catalog['lastRoot'] = str(path)
                if root_key not in self._catalog['outputFolders']:
                    self._catalog['outputFolders'][root_key] = (self._catalog['outputFolder'] if first_root and self._catalog['outputFolder']
                                                              else str(path/'Renamed Animations'))
            output_folder = self._catalog['outputFolders'].get(root_key, str(path/'Renamed Animations'))
            # The export list has one explicit shared destination. Scanning
            # another library must not replace that destination on restart.
            if activate and not self._catalog.get('outputFolder'):
                self._catalog['outputFolder'] = output_folder
            # A pre-existing destination may also contain original assets.
            # Prune only folders this app created; otherwise filter our copies.
            output_directories = set(self._catalog['managedOutputDirectories'])
            exported_files = set(self._catalog['exportedFiles'])
            if activate:
                self._save()
        entities, clips, warnings = [], [], []
        count = 0
        def unreadable(error):
            if len(warnings) < 50:
                warnings.append(str(error))
        # Map variants own their neighboring exported asset branches. Do not
        # decode those thousands of model/clip dependencies as character assets.
        folders = () if inside_map_assets(path) else os.walk(path, followlinks=False, onerror=unreadable)
        for parent, directories, files in folders:
            map_export = has_map_marker(files)
            directories[:] = [d for d in directories if d not in ('.git', '__pycache__')
                              and not (map_export and d.casefold() in MAP_ASSET_FOLDERS)
                              and self._key(Path(parent)/d) not in output_directories]
            if map_export:
                continue
            for name in files:
                count += 1
                if count > 250000:
                    warnings.append('Scan stopped after 250,000 files. Select a more specific root folder.')
                    break
                item = Path(parent) / name
                suffix = item.suffix.lower()
                if suffix == '.owanimclip' and self._key(item) in exported_files:
                    continue
                if suffix in ('.owentity', '.owmdl'):
                    entities.append({'path': str(item), 'name': name, 'relativePath': str(item.relative_to(path)),
                                     'kind': 'entity' if suffix == '.owentity' else 'model'})
                elif suffix == '.owanimclip':
                    try:
                        info = read_clip_header(str(item))
                    except Exception as error:
                        try:
                            size = item.stat().st_size
                        except OSError:
                            size = 0
                        info = {'path': str(item), 'name': name, 'size': size,
                                'fps': 0, 'durationFrames': 0, 'durationSeconds': 0,
                                'boneCount': 0, 'error': str(error)}
                    info['relativePath'] = str(item.relative_to(path))
                    info['annotation'] = self._annotation(item)
                    clips.append(info)
            if count > 250000:
                break
        entities.sort(key=entity_sort_key)
        bone_counts = BoneCountIndex(path, (item['path'] for item in entities))
        for item in entities:
            item.update(bone_counts.metadata(item['path']))
            try:
                item['size'] = Path(item['path']).stat().st_size
            except OSError as error:
                item.update(size=None, sizeError=str(error))
        clips.sort(key=lambda item: item['relativePath'].lower())
        warnings.extend(self._duplicates.annotate(clips)[:max(0, 50 - len(warnings))])
        unique_clip_count = len({clip['duplicateKey'] for clip in clips})
        # Evidence analysis is currently an opt-in backend research capability;
        # ordinary library scans do not start or restore analysis jobs.
        return {'root': str(path), 'outputFolder': output_folder, 'entities': entities, 'clips': clips,
                'uniqueClipCount': unique_clip_count, 'duplicateClipCount': len(clips) - unique_clip_count,
                'warnings': warnings}

    @reply
    def load_entity(self, path):
        asset = load_asset(str(Path(path).resolve()), root_path=str(self._root) if self._root else None)
        self._textures.register(asset.get('textures', {}), owner='animations')
        return self._register_names(path, asset)

    @reply
    def preview_entity(self, path):
        with self._entity_preview_lock:
            self._entity_preview_generation += 1
            generation = self._entity_preview_generation
        with self._lock:
            root = str(self._root) if self._root else None
            names = BrowserAPI.object_names.__wrapped__(self, path)
        asset = load_asset(str(Path(path).resolve()), root_path=root)
        known = object_name_keys(asset)
        names = {node: value for node, value in names.items() if node in known}
        if names:
            asset = apply_object_names(asset, names)
        asset['objectNames'] = names
        with self._entity_preview_lock:
            if generation != self._entity_preview_generation:
                raise ValueError('The model preview was closed or replaced.')
            self._textures.register(asset.get('textures', {}), owner='model-browser')
        # Previewing candidates never registers editable scene names: doing so
        # would evict the active scene when browsing more than eight models.
        return asset

    @reply
    def release_entity_preview(self):
        with self._entity_preview_lock:
            self._entity_preview_generation += 1
            self._textures.release('model-browser')
        return {'released': True}

    @reply
    def load_texture(self, texture_id, max_edge=2048):
        return self._textures.load(texture_id, max_edge)

    @reply
    def load_animation(self, path):
        return load_clip(str(Path(path).resolve()))

    @reply
    def save_annotation(self, path, annotation):
        if not isinstance(annotation, dict):
            raise ValueError('Invalid annotation')
        value = {'name': str(annotation.get('name', ''))[:200],
                 'notes': str(annotation.get('notes', ''))[:10000],
                 'favorite': bool(annotation.get('favorite', False))}
        with self._lock:
            self._catalog['annotations'][self._key(path)] = value
            self._save()
        return value


def make_server(api, port=0):
    """Developer-only test host with a same-origin, token-protected bridge."""
    token = secrets.token_urlsafe(32)
    web_root = BASE / 'web'
    allowed = API_METHODS

    class Handler(SimpleHTTPRequestHandler):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, directory=str(web_root), **kwargs)

        def log_message(self, *args):
            pass

        def do_GET(self):
            if self.path.split('?')[0] in ('/', '/index.html'):
                bridge = '<script>window.pywebview={api:{}};'
                bridge += 'for(const method of ' + json.dumps(allowed) + '){window.pywebview.api[method]=(...args)=>fetch("/api/"+method,{method:"POST",headers:{"Content-Type":"application/json","X-Preview-Token":' + json.dumps(token) + '},body:JSON.stringify(args)}).then(r=>r.json());}'
                bridge += '</script>'
                content = (web_root / 'index.html').read_text(encoding='utf-8').replace('<head>', '<head>' + bridge)
                payload = content.encode('utf-8')
                self.send_response(200)
                self.send_header('Content-Type', 'text/html; charset=utf-8')
                self.send_header('Content-Length', str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)
                return
            super().do_GET()

        def do_POST(self):
            method = self.path.removeprefix('/api/')
            if not self.path.startswith('/api/') or method not in allowed or self.headers.get('X-Preview-Token') != token:
                self.send_error(403)
                return
            try:
                length = int(self.headers.get('Content-Length', '0'))
                if length < 0 or length > 100000:
                    raise ValueError('Request too large')
                args = json.loads(self.rfile.read(length))
                if not isinstance(args, list):
                    raise ValueError('Expected arguments')
                result = getattr(api, method)(*args)
            except Exception as error:
                result = {'ok': False, 'error': str(error)}
            payload = json.dumps(result, ensure_ascii=False, allow_nan=False).encode('utf-8')
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

    server = ThreadingHTTPServer(('127.0.0.1', port), Handler)
    api._server = server
    return server


def run_stdio(api, input_stream=None, output_stream=None):
    """Private JSON-lines transport owned by the desktop process, never a network listener."""
    input_stream = input_stream or sys.stdin
    output_stream = output_stream or sys.stdout
    for stream in (input_stream, output_stream):
        if hasattr(stream, 'reconfigure'):
            stream.reconfigure(encoding='utf-8')
    for line in input_stream:
        request_id = None
        try:
            if len(line) > 100000:
                raise ValueError('Request too large')
            request = json.loads(line)
            if not isinstance(request, dict):
                raise ValueError('Expected a request object')
            request_id = request.get('id')
            if isinstance(request_id, bool) or not isinstance(request_id, (str, int)):
                request_id = None
                raise ValueError('Expected a string or integer request id')
            method, args = request.get('method'), request.get('args', [])
            if not isinstance(method, str) or method not in API_METHODS:
                raise ValueError('Unknown API method')
            if not isinstance(args, list):
                raise ValueError('Expected an argument list')
            result = getattr(api, method)(*args)
            payload = json.dumps({'id': request_id, 'result': result}, ensure_ascii=False, allow_nan=False)
        except Exception as error:
            payload = json.dumps({'id': request_id, 'result': {'ok': False, 'error': str(error)}}, ensure_ascii=False)
        output_stream.write(payload + '\n')
        output_stream.flush()


def main():
    parser = argparse.ArgumentParser(description='Avolition asset backend; launch Avolition.exe for the desktop app.')
    parser.add_argument('--stdio', action='store_true', help='Private transport used by the Avolition desktop host')
    parser.add_argument('--serve', action='store_true', help='Developer-only loopback test server; never opens a browser')
    parser.add_argument('--port', type=int, default=8765)
    parser.add_argument('--root')
    parser.add_argument('--data-dir')
    parser.add_argument('--app-dir', help='Portable app directory supplied by the desktop host')
    parser.add_argument('--self-test', metavar='REPORT')
    args = parser.parse_args()
    if not (args.stdio or args.serve or args.self_test):
        parser.error('Launch Avolition.exe to open the desktop app.')
    api = BrowserAPI(args.data_dir, args.root, desktop_mode=args.stdio, app_dir=args.app_dir)
    if args.self_test:
        checks = {'ok': True, 'bootstrap': api.bootstrap()}
        if args.root:
            checks['scan'] = api.scan_root(args.root)
            checks['ok'] = checks['scan']['ok']
        Path(args.self_test).write_text(json.dumps(checks, indent=2), encoding='utf-8')
        return
    if args.stdio:
        try:
            run_stdio(api)
        finally:
            api.shutdown()
        return
    server = make_server(api, args.port)
    print('Avolition developer server: http://127.0.0.1:' + str(server.server_port), flush=True)
    try:
        server.serve_forever()
    finally:
        api.shutdown()
        server.server_close()


if __name__ == '__main__':
    try:
        main()
    except Exception:
        traceback.print_exc(file=sys.stderr)
        sys.exit(1)
