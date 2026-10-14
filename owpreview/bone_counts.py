"""Cheap skeleton totals for the model browser, without decoding model geometry."""
from pathlib import Path

from .formats import (FormatError, MAX_ENTITY_DEPTH, MAX_FILE_BYTES, MAX_MODELS,
                      _Reader, _Resolver, _read_entity, _version)


class _HeaderReader(_Reader):
    """Reuse format validation while reading only requested header bytes."""
    def __init__(self, path, stream):
        self.path, self.stream, self.offset = path, stream, 0
        self.size = path.stat().st_size
        if self.size > MAX_FILE_BYTES:
            self.fail('file exceeds the 512 MB preview limit')

    @property
    def remaining(self):
        return self.size - self.offset

    def take(self, size):
        if size < 0 or size > self.remaining:
            self.fail(f'truncated data (need {size:,} bytes, have {self.remaining:,})')
        data = self.stream.read(size)
        if len(data) != size:
            self.fail('truncated header')
        self.offset += size
        return memoryview(data)


def read_model_bone_count(path):
    """Return the declared skeleton size; mesh/skeleton/material data stays unread."""
    path = Path(path)
    with path.open('rb') as stream:
        reader = _HeaderReader(path, stream)
        major, minor = reader.unpack('HH')
        _version(reader, major, minor, 'model')
        reader.string()  # Material reference.
        reader.string()  # Model name.
        _, bones, meshes, sockets = reader.unpack('IHII')
        reader.count(meshes, 16384, 'mesh count')
        reader.count(sockets, 65536, 'socket count')
        return bones


class BoneCountIndex:
    """One scan's cached references and totals, counting every attachment instance.

    An incomplete hierarchy has no total: missing references must not look like
    a smaller valid rig. Explicit null-model entities remain legitimate wrappers.
    Passing scanned paths avoids another root walk and excludes map dependencies
    already pruned by the animation library scan.
    """
    def __init__(self, root_path=None, paths=None):
        self.resolver = _Resolver(root_path)
        if paths is not None:
            self.resolver.index = {}
            for value in paths:
                path = Path(value)
                self.resolver.index.setdefault(path.name.lower(), []).append(path)
        self._headers = {}
        self._entities = {}
        self._references = {}
        self._totals = {}

    @staticmethod
    def _cached(cache, key, read):
        if key not in cache:
            try:
                cache[key] = read()
            except (OSError, FormatError) as error:
                cache[key] = error
        value = cache[key]
        if isinstance(value, Exception):
            raise value
        return value

    def _find(self, reference, owner, kind, relative='../../'):
        key = (reference, owner, kind, relative)
        return self._cached(self._references, key,
                            lambda: self.resolver.find(reference, owner, kind, relative))

    def _summary(self, path, active, depth=0):
        if depth > MAX_ENTITY_DEPTH:
            raise FormatError('Entity hierarchy exceeds 48 levels.')
        if path in active:
            raise FormatError(f'Entity reference cycle: {path.name}')
        if path in self._totals:
            summary = self._totals[path]
            if depth + summary[2] > MAX_ENTITY_DEPTH:
                raise FormatError('Entity hierarchy exceeds 48 levels.')
            return summary
        active.add(path)
        try:
            entity = None
            if path.suffix.lower() == '.owentity':
                entity = self._cached(self._entities, path, lambda: _read_entity(path))
                model = self._find(entity['model'], path, 'model', entity['relative'])
                if entity['model'] and model is None:
                    raise FormatError(f"{path.name}: missing model {entity['model']}.")
            elif path.suffix.lower() == '.owmdl':
                model = path
            else:
                raise FormatError('Select an .owentity or .owmdl file.')
            bones = self._cached(self._headers, model, lambda: read_model_bone_count(model)) if model else 0
            nodes, height = 1, 0
            for child in entity['children'] if entity else ():
                if not child['reference']:
                    continue
                child_path = self._find(child['reference'], path, 'entity')
                if child_path is None:
                    raise FormatError(f"{path.name}: missing child entity {child['reference']}.")
                child_bones, child_nodes, child_height = self._summary(child_path, active, depth + 1)
                bones += child_bones
                nodes += child_nodes
                height = max(height, child_height + 1)
                if nodes > MAX_MODELS:
                    raise FormatError('Entity exceeds 512 model nodes.')
            summary = (bones, nodes, height)
            self._totals[path] = summary
            return summary
        finally:
            active.remove(path)

    def metadata(self, path):
        try:
            total, _, _ = self._summary(Path(path).expanduser().resolve(), set())
            return {'boneCount': total, 'boneCountError': ''}
        except (OSError, FormatError) as error:
            return {'boneCount': None, 'boneCountError': str(error)}
