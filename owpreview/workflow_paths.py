"""Portable application folders and shared workflow settings.

Electron supplies the directory containing Whitehound.exe. Source runs use the
source directory, never the shell's current working directory. The user's
library/toolchain folders therefore keep their established portable locations.
"""
from pathlib import Path
import os
import sys


def absolute_folder(value, label):
    clean = str(value or '').strip()
    if len(clean) > 1 and clean[0] == clean[-1] and clean[0] in ('"', "'"):
        clean = clean[1:-1].strip()
    if not clean:
        return ''
    path = Path(os.path.expandvars(clean)).expanduser()
    if not path.is_absolute():
        raise ValueError(f'{label}: use a full, absolute folder path.')
    return str(path.resolve())


def application_directory(explicit=None):
    if explicit:
        return Path(absolute_folder(explicit, 'Application folder'))
    if getattr(sys, 'frozen', False):
        # Packaged backend: Whitehound/resources/backend/owio-core.exe.
        executable = Path(sys.executable).resolve()
        if executable.parent.name == 'backend' and executable.parent.parent.name == 'resources':
            return executable.parents[2]
        return executable.parent
    return Path(__file__).resolve().parents[1]


class WorkflowPaths:
    def __init__(self, catalog, app_dir=None):
        self.catalog = catalog
        self.app_dir = application_directory(app_dir)
        # Honor a configured library location. lastRoot is a scan selection,
        # not the portable library: it can point at any exported asset folder.
        configured = catalog.get('libraryDirectory') or catalog.get('libraryPath')
        self.library = Path(absolute_folder(configured, 'Library') if configured else self.app_dir / 'library')
        self.toolchain = self.app_dir / 'toolchain'

    def settings(self):
        saved = self.catalog.get('workflow', {})
        if not isinstance(saved, dict):
            saved = {}
        return {
            'overwatchInstall': saved.get('overwatchInstall', ''),
            'outputDirectory': saved.get('outputDirectory') or str(self.library),
            'libraryDirectory': str(self.library),
            'toolchainDirectory': str(self.toolchain),
            'activeTab': saved.get('activeTab') if saved.get('activeTab') in ('animations', 'maps', 'toolchain') else 'animations',
            'toolchainTab': saved.get('toolchainTab') if saved.get('toolchainTab') in ('heroes', 'maps') else 'heroes',
        }

    def update(self, changes):
        if not isinstance(changes, dict):
            raise ValueError('Invalid workflow settings.')
        updated = self.settings()
        for key, label in (('overwatchInstall', 'Overwatch Install'), ('outputDirectory', 'Output Directory')):
            if key in changes:
                updated[key] = absolute_folder(changes[key], label)
        if 'activeTab' in changes:
            if changes['activeTab'] not in ('animations', 'maps', 'toolchain'):
                raise ValueError('Unknown workflow tab.')
            updated['activeTab'] = changes['activeTab']
        if 'toolchainTab' in changes:
            if changes['toolchainTab'] not in ('heroes', 'maps'):
                raise ValueError('Unknown Toolchain export tab.')
            updated['toolchainTab'] = changes['toolchainTab']
        self.catalog['workflow'] = {key: updated[key] for key in ('overwatchInstall', 'outputDirectory', 'activeTab', 'toolchainTab')}
        if updated['outputDirectory'] and os.path.normcase(updated['outputDirectory']) == os.path.normcase(str(self.library)):
            self.catalog['workflow']['outputDirectory'] = ''
        return self.settings()

    def use_library(self):
        return self.update({'outputDirectory': ''})
