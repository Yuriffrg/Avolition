"""Assemble the optional companion using stdlib-only Cinema 4D modules."""
from pathlib import Path
import shutil


def bundle(source, target):
    source, target = Path(source), Path(target)
    if (target/'Whitehound C4D Bridge').exists():
        raise ValueError('Use a fresh companion bundle destination: it contains an older Whitehound C4D Bridge folder.')
    destination = target/'Avolition C4D Bridge'
    if destination.is_dir() and any(path.name != 'Avolition C4D Bridge.pyp' for path in destination.glob('*.pyp')):
        raise ValueError('Use a fresh companion bundle destination: it contains another plugin entry point.')
    shutil.copytree(source/'c4d-plugin/Avolition C4D Bridge', destination, dirs_exist_ok=True)
    library = destination/'whitehound_c4d'
    library.mkdir(exist_ok=True)
    # The package/protocol name stays stable so existing exports and mailbox
    # discovery remain compatible across the product rename.
    (library/'__init__.py').write_text('"""Avolition native export companion."""\n', encoding='utf-8')
    for name in ('c4d_bridge.py', 'c4d_scene.py', 'c4d_octane.py', 'c4d_motion.py', 'c4d_pov.py'):
        shutil.copy2(source/'owpreview'/name, library/name)
    shutil.copy2(source/'LICENSE', destination/'LICENSE')
    return destination


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument('--target', type=Path, required=True)
    args = parser.parse_args()
    print(bundle(args.source, args.target))
