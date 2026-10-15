"""Assemble portable Avolition from an official Electron runtime and frozen core."""
import argparse
import hashlib
import importlib.metadata as metadata
import json
from pathlib import Path
import shutil
import sys
import zipfile
from bundle_c4d_bridge import bundle as bundle_c4d_bridge


def stamp_windows_branding(target, source):
    from PyInstaller.utils.win32 import icon, versioninfo
    icon.CopyIcons_FromIco(str(target/'Avolition.exe'), [str(source/'web/assets/avolition.ico')])
    info = versioninfo.VSVersionInfo(
        ffi=versioninfo.FixedFileInfo(filevers=(0, 8, 23, 0), prodvers=(0, 8, 23, 0), fileType=1),
        kids=[versioninfo.StringFileInfo([versioninfo.StringTable('040904B0', [
            versioninfo.StringStruct(key, value) for key, value in {
                'FileDescription': 'Avolition', 'FileVersion': '0.8.23', 'InternalName': 'Avolition',
                'OriginalFilename': 'Avolition.exe', 'ProductName': 'Avolition', 'ProductVersion': '0.8.23'
            }.items()])]), versioninfo.VarFileInfo([versioninfo.VarStruct('Translation', [1033, 1200])])])
    versioninfo.write_version_info_to_executable(str(target/'Avolition.exe'), info)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', required=True, type=Path)
    parser.add_argument('--build-root', required=True, type=Path)
    parser.add_argument('--electron', required=True, type=Path)
    parser.add_argument('--go-core', type=Path, help='Go owio-core.exe: the complete backend (no frozen Python core)')
    args = parser.parse_args()
    source, build = args.source.resolve(), args.build_root.resolve()
    target = build / 'dist' / 'Avolition'
    target.relative_to(build)
    if target.exists():
        raise SystemExit('Use a fresh BuildRoot: distribution folder already exists: ' + str(target))
    version = json.loads((source/'desktop/package.json').read_text())['devDependencies']['electron']
    if args.electron.name != f'electron-v{version}-win32-x64.zip':
        raise SystemExit('Electron archive version does not match desktop/package.json')
    manifest = args.electron.parent/'SHASUMS256.txt'
    if not manifest.is_file():
        raise SystemExit('Place the official release SHASUMS256.txt beside the Electron archive.')
    expected = next((line.split()[0] for line in manifest.read_text().splitlines()
                     if line.split()[-1].lstrip('*') == args.electron.name), None)
    with args.electron.open('rb') as runtime:
        actual = hashlib.file_digest(runtime, 'sha256').hexdigest()
    if not expected or actual != expected:
        raise SystemExit('Electron SHA256 checksum does not match the official manifest.')
    target.mkdir(parents=True)
    with zipfile.ZipFile(args.electron) as archive:
        for entry in archive.infolist():
            (target/entry.filename).resolve().relative_to(target.resolve())
        archive.extractall(target)
    (target/'electron.exe').rename(target/'Avolition.exe')
    (target/'LICENSE').rename(target/'LICENSE.electron')
    (target/'resources/default_app.asar').unlink(missing_ok=True)
    app = target/'resources/app'
    app.mkdir()
    for name in ('main.cjs', 'frames.cjs', 'preload.cjs', 'package.json'):
        shutil.copy2(source/'desktop'/name, app/name)
    shutil.copytree(source/'web', target/'resources/web')
    if args.go_core:
        # The Go core is the whole backend (gocore/README.md): no Python runtime ships.
        (target/'resources/backend').mkdir()
        shutil.copy2(args.go_core, target/'resources/backend/owio-core.exe')
    else:
        shutil.copytree(build/'core/owio-core', target/'resources/backend')
    for name in ('README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md', 'VALIDATION.md'):
        shutil.copy2(source/name, target/name)
    shutil.copytree(source/'owpreview', target/'owpreview', ignore=shutil.ignore_patterns('*.py', '__pycache__'))
    shutil.copytree(source/'tests/material-assets', target/'Example assets')
    (target/'library').mkdir()
    (target/'toolchain').mkdir()
    bundle_c4d_bridge(source, target/'Cinema 4D')
    shutil.copytree(source, target/'source', ignore=shutil.ignore_patterns(
        '__pycache__', '*.pyc', '.build', 'node_modules', 'toolchain', 'library', 'Open in Browser.cmd'))
    licenses = target/'LICENSES'
    licenses.mkdir()
    shutil.copytree(source/'licenses', licenses, dirs_exist_ok=True)
    # A Go-core build ships no Python runtime or Python packages.
    bundled = () if args.go_core else ('pyinstaller', 'pyinstaller-hooks-contrib', 'pillow', 'numpy', 'charset-normalizer')
    for candidate in () if args.go_core else (Path(sys.base_prefix)/'LICENSE.txt', Path(sys.base_prefix)/'LICENSE'):
        if candidate.is_file():
            shutil.copy2(candidate, licenses/'PYTHON-LICENSE.txt')
            break
    for name in bundled:
        try:
            dist = metadata.distribution(name)
        except metadata.PackageNotFoundError:
            continue  # Optional Pillow dependencies vary by build environment.
        for entry in dist.files or []:
            item = Path(dist.locate_file(entry))
            if item.is_file() and any(word in item.name.lower() for word in ('license', 'copying', 'notice')):
                destination = licenses/name/Path(str(entry).replace('..', '_'))
                destination.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(item, destination)
    (target/'START HERE.txt').write_text(
        'Avolition 0.8.23\n\nExtract this entire folder, then double-click Avolition.exe.\n'
        'Keep Avolition.exe beside its resources, locales, and runtime files.\n'
        'Avolition opens its own desktop window. Preview needs no browser, Python, Cinema 4D, or internet.\n'
        'Optional C4D/Octane export requires Cinema 4D and the included Cinema 4D/Avolition C4D Bridge plugin.\n\n'
        'Toolchain: choose Overwatch Install and install the official Toolchain. Use its Animations or Maps tabs to choose an asset to extract.\n'
        'Output Directory defaults to library beside Avolition.exe. Use Library restores this default.\n'
        'Toolchain downloads and dependencies stay under toolchain beside Avolition.exe, separate from assets.\n'
        'Animations: paste an exported folder into Library source, then scan and load an entity.\n'
        'Browse models opens a searchable hero/skin browser with an interactive preview. Gameplay1P and Gameplay3P come first.\n'
        'Use its sort dropdown for most/fewest joints or largest/smallest source files within each hero and skin.\n'
        'File size excludes referenced models and textures. Hover an Entity/Model badge for its byte count.\n'
        'Bone counts beside browser names include attached models. A dash means the count is unavailable; hover for details.\n'
        'Select a row to preview it, then Load into Animations when ready. Browsing preserves the current animation setup.\n'
        'Find clips with For selected object, bone-coverage bars and joined search/sort controls.\n'
        'Animation setup: choose the arms object and a clip, then the gun object and its clip. Both stay assigned.\n'
        'Name the setup (entity_nickname) and press Add to export. This saves the setup without writing files.\n'
        'Open the blue Export Manager button between Clip details and Preview objects, choose an output folder, check entries, then Export selected.\n'
        'Entries start unchecked and include the whole entity, even with one assigned clip. C4D is the only export format.\n'
        'Maps: scan an exported map folder, expand maps and variant folders, then choose an .owmap scene.\n'
        'Map textures cover the scene at adaptive preview resolution. C4D baking uses source-resolution textures.\n'
        'Engine collision meshes are omitted using the Blender importer material list. Source geometry keeps its original scale.\n'
        'Map C4D export requires the bundled Avolition C4D Bridge 0.7.5. Map lights, sounds and effects are not included.\n'
        'Double-click scene, model or mesh names in Preview objects to rename them for future exports. Source files and joint names stay unchanged.\n'
        'Example assets contains a small synthetic rig for trying the controls.\n', encoding='utf-8')
    (target/'BUILD.json').write_text(json.dumps({'app': 'Avolition', 'version': '0.8.23',
        'electron': version, 'electronSHA256': actual,
        **({'backend': 'go'} if args.go_core else {'python': sys.version})}, indent=2), encoding='utf-8')
    stamp_windows_branding(target, source)
    print(target)


if __name__ == '__main__':
    main()
