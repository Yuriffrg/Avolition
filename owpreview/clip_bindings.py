"""Small read-only batches of clip binding names for the animation browser."""
from __future__ import annotations

from collections import OrderedDict
import os
from pathlib import Path
import threading

from .formats import read_clip_binding_metadata


class ClipBindingIndex:
    """Cache exact bone names, never motion or guesses based on track counts.

    Failed files return ``boneNames: None`` so they cannot look like a checked
    zero-match clip. LRU entries are bounded by both count and estimated memory.
    No data is written beside the source files or persisted in the catalog.
    """
    MAX_BATCH = 128

    def __init__(self, max_entries=32768, max_bytes=64 * 1024 * 1024):
        self.max_entries = max(0, int(max_entries))
        self.max_bytes = max(0, int(max_bytes))
        self._cache = OrderedDict()
        self._bytes = 0
        self._lock = threading.Lock()

    @staticmethod
    def _fingerprint(stat):
        return (stat.st_dev, stat.st_ino, stat.st_size, stat.st_mtime_ns, stat.st_ctime_ns)

    def _read_one(self, path, pose_bones=None):
        if not isinstance(path, str) or not path or len(path) > 32767:
            raise ValueError('Use the full path to an .owanimclip file.')
        source = Path(path)
        if not source.is_absolute() or source.suffix.lower() != '.owanimclip':
            raise ValueError('Use the full path to an .owanimclip file.')
        # Cache hits need one stat, not another filesystem path-resolution walk.
        # Symlink spellings may occupy separate entries; fingerprints still
        # describe the target file and each request retains its original key.
        source = Path(os.path.abspath(path))
        key = os.path.normcase(str(source))
        before = self._fingerprint(source.stat())
        if pose_bones:
            metadata = read_clip_binding_metadata(source, pose_bones)
            if self._fingerprint(source.stat()) != before:
                raise OSError('File changed while reading animation bindings; scan again.')
            return {'boneNames': metadata['boneNames'], 'firstPose': metadata['firstPose']}
        with self._lock:
            cached = self._cache.get(key)
            if cached and cached[0] == before:
                self._cache.move_to_end(key)
                return {'boneNames': list(cached[1])}
            if cached:
                self._bytes -= cached[2]
                del self._cache[key]
        metadata = read_clip_binding_metadata(source)
        if self._fingerprint(source.stat()) != before:
            raise OSError('File changed while reading animation bindings; scan again.')
        names = tuple(metadata['boneNames'])
        cost = len(key.encode('utf-8')) + 128 + sum(len(name.encode('utf-8')) + 64 for name in names)
        with self._lock:
            if self.max_entries and cost <= self.max_bytes:
                # Another reader may have completed this path concurrently.
                previous = self._cache.pop(key, None)
                if previous:
                    self._bytes -= previous[2]
                self._cache[key] = (before, names, cost)
                self._bytes += cost
                while len(self._cache) > self.max_entries or self._bytes > self.max_bytes:
                    _, removed = self._cache.popitem(last=False)
                    self._bytes -= removed[2]
        return {'boneNames': list(names)}

    def read(self, paths, pose_bones=None):
        """Return ``{clips: [{path, boneNames, error?}]}`` in request order.

        The caller can submit batches asynchronously. Original path strings are
        retained as UI keys while cache lookup normalizes filesystem identity.
        """
        if not isinstance(paths, list) or len(paths) > self.MAX_BATCH:
            raise ValueError(f'Request animation bindings in batches of at most {self.MAX_BATCH} paths.')
        if pose_bones is not None and (not isinstance(pose_bones, list) or len(pose_bones) > 1024
                or any(not isinstance(name, str) or not name for name in pose_bones)):
            raise ValueError('Invalid frame-zero bone selection.')
        pose_bones = set(pose_bones or [])
        results = []
        for path in paths:
            try:
                results.append({'path': path, **self._read_one(path, pose_bones)})
            except (OSError, ValueError, TypeError) as error:
                results.append({'path': path if isinstance(path, str) else None,
                                'boneNames': None, 'error': str(error) or type(error).__name__})
        return {'clips': results}
