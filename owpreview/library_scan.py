"""Separate OWLib map dependencies from the character animation library."""
import os
from pathlib import Path


# OWLib places these branches beside its .owmap variants. Classify them only
# when the parent has a map marker; character exports use some of these names too.
MAP_ASSET_FOLDERS = frozenset({
    'models', 'entities', 'effects', 'textures', 'sound', 'sounds', 'voicesets',
    'animations', 'materials', 'modellooks',
})


def has_map_marker(filenames):
    return any(name.casefold().endswith('.owmap') for name in filenames)


def entity_sort_key(item):
    """Show the playable first/third-person entities before other scan results."""
    entity = item.get('kind') == 'entity'
    relative = str(item.get('relativePath') or item.get('path') or item.get('name', ''))
    filename = str(item.get('name') or relative.replace('\\', '/').rsplit('/', 1)[-1]).casefold()
    priority = {'gameplay1p.owentity': 0, 'gameplay3p.owentity': 1}.get(filename, 2) if entity else 2
    return (not entity, priority, relative.lower())


def inside_map_assets(path):
    """Also exclude dependencies when the user scans inside a map export.

    Only inspect parents of recognized asset branches. A mixed library with a
    loose map and a Heroes sibling must still retain its character assets.
    """
    child = Path(path)
    for parent in child.parents:
        if child.name.casefold() in MAP_ASSET_FOLDERS:
            try:
                with os.scandir(parent) as entries:
                    if any(entry.name.casefold().endswith('.owmap') and entry.is_file()
                           for entry in entries):
                        return True
            except OSError:
                # Unreadable parent metadata is not evidence of map ownership.
                pass
        child = parent
    return False
