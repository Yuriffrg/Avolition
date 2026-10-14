"""Publish independently selected animation formats without overwriting files."""
from pathlib import Path
import errno
import shutil
import tempfile


DEFAULT_FBX_OPTIONS = {'enabled': False, 'upAxis': 'Y', 'includeMesh': True,
                       'bakeTextures': True, 'rootMotion': True, 'c4dEnabled': True,
                       'clipEnabled': False}


def fbx_options(value=None):
    if value is None:
        return dict(DEFAULT_FBX_OPTIONS)
    if not isinstance(value, dict):
        raise ValueError('Invalid FBX export options.')
    result = dict(DEFAULT_FBX_OPTIONS)
    for key in ('enabled', 'includeMesh', 'bakeTextures', 'rootMotion', 'c4dEnabled', 'clipEnabled'):
        if key in value:
            if not isinstance(value[key], bool):
                raise ValueError(f'Invalid FBX option: {key}')
            result[key] = value[key]
    if 'upAxis' in value:
        if value['upAxis'] not in ('Y', 'Z'):
            raise ValueError('Choose Y or Z as the FBX up axis.')
        result['upAxis'] = value['upAxis']
    return result


def same_bytes(source, destination):
    if not destination.is_file() or source.stat().st_size != destination.stat().st_size:
        return False
    with source.open('rb') as left, destination.open('rb') as right:
        while True:
            block = left.read(1024 * 1024)
            if block != right.read(1024 * 1024):
                return False
            if not block:
                return True


def _publish(source, destination, created):
    """Exclusive create, tracking only files owned by this transaction."""
    if destination.exists():
        if same_bytes(source, destination):
            return False
        raise FileExistsError(errno.EEXIST, 'An existing file has different content', str(destination))
    try:
        handle = destination.open('xb')
    except FileExistsError:
        if same_bytes(source, destination):
            return False
        raise
    created.append(destination)
    with handle, source.open('rb') as original:
        shutil.copyfileobj(original, handle, 1024 * 1024)
    return True


def export_pair(source, folder, filename, builder):
    """builder(stage_fbx, stage_tex) returns {warnings:[], ...}.

    FBX references must use relative tex/<filename> paths. The FBX is completed
    before either user-facing animation file appears. Existing equal files are
    reused; a collision numbers both animation files together.
    """
    return export_bundle(source, folder, filename, lambda paths, tex: builder(paths['fbx'], tex), ('clip', 'fbx'))


def export_bundle(source, folder, filename, builder, formats):
    """Publish only requested formats; builder receives scene destinations only.

    All selected animation files share a collision suffix. Unselected files have
    no bearing on that suffix and are never touched. Clip-only exports do not
    call the scene builder. An empty selection performs no filesystem work.
    """
    requested = set(formats)
    if any(value not in ('clip', 'fbx', 'c4d') for value in requested):
        raise ValueError('Choose .owanimclip, FBX or C4D as an export format.')
    formats = tuple(kind for kind in ('clip', 'fbx', 'c4d') if kind in requested)
    if not formats:
        return {'skipped': True, 'files': [], 'path': None, 'name': None}
    source, folder = Path(source), Path(folder)
    folder.mkdir(parents=True, exist_ok=True)
    desired = folder / filename
    with tempfile.TemporaryDirectory(prefix='.whitehound-export-', dir=folder) as temporary:
        stage = Path(temporary)
        scene_paths = {kind: stage / ('animation.' + kind) for kind in formats if kind != 'clip'}
        stage_tex = stage / 'tex'
        details = (builder(scene_paths, stage_tex) or {}) if scene_paths else {}
        for kind, scene in scene_paths.items():
            if not scene.is_file() or not scene.stat().st_size:
                raise ValueError(kind.upper() + ' export did not produce a file.')
        textures = sorted(stage_tex.glob('*.png')) if stage_tex.exists() else []
        if textures:
            (folder / 'tex').mkdir(exist_ok=True)
        for number in range(1, 10001):
            clip_path = desired if number == 1 else desired.with_name(f'{desired.stem} ({number}).owanimclip')
            if 'clip' in formats and clip_path.resolve() == source.resolve():
                continue
            targets = {kind: clip_path if kind == 'clip' else clip_path.with_suffix('.' + kind)
                       for kind in formats}
            pairs = [(source if kind == 'clip' else scene_paths[kind], targets[kind]) for kind in formats]
            if any(target.exists() and not same_bytes(original, target) for original, target in pairs):
                continue
            created = []
            try:
                # Publish textures before the FBX, so a completed FBX always has
                # its dependencies. Never replace an externally modified texture.
                for texture in textures:
                    _publish(texture, folder / 'tex' / texture.name, created)
                for original, target in pairs:
                    _publish(original, target, created)
            except FileExistsError as error:
                for target in reversed(created):
                    target.unlink(missing_ok=True)
                if getattr(error, 'filename', None) in [str(target) for _original, target in pairs]:
                    continue
                raise
            except Exception:
                for target in reversed(created):
                    target.unlink(missing_ok=True)
                raise
            first_path = targets[formats[0]]
            return {'path': str(first_path), 'name': first_path.name,
                    **{kind + 'Path': str(target) for kind, target in targets.items()},
                    'textureFolder': str(folder / 'tex') if textures else None,
                    'textureCount': len(textures), 'reused': not created,
                    'warnings': details.get('warnings', []),
                    'files': [str(target) for _original, target in pairs] + [str(folder / 'tex' / p.name) for p in textures]}
    raise ValueError('Too many files share that nickname. Choose another name.')
