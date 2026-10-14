"""Non-destructive, exact-content duplicate indexing for large clip libraries.

Animation IDs and joint counts are not identities: the same filename can contain
different motion, while the same motion may be exported under different names.
Only identical bytes are grouped. Headers prune candidates before streaming the
files, and a bounded stat cache makes repeated folder scans inexpensive.
"""
from __future__ import annotations

from collections import OrderedDict, defaultdict
import hashlib
import os
from pathlib import Path
import threading


class DuplicateIndex:
    def __init__(self, max_entries=32768):
        self.max_entries = max(0, int(max_entries))
        self._cache = OrderedDict()
        self._lock = threading.Lock()

    @staticmethod
    def _path_key(path):
        return os.path.normcase(os.path.abspath(os.fspath(path)))

    @staticmethod
    def _fingerprint(stat):
        # ctime/inode also catch replacements which preserve size and mtime.
        return (stat.st_dev, stat.st_ino, stat.st_size, stat.st_mtime_ns, stat.st_ctime_ns)

    def _digest(self, path, expected_size):
        path = Path(path)
        key = self._path_key(path)
        before = path.stat()
        fingerprint = self._fingerprint(before)
        if before.st_size != expected_size:
            raise OSError('File changed during the scan; scan again to check duplicates.')
        with self._lock:
            cached = self._cache.get(key)
            if cached and cached[0] == fingerprint:
                self._cache.move_to_end(key)
                return cached[1]
        digest = hashlib.sha256()
        with path.open('rb') as source:
            for block in iter(lambda: source.read(1024 * 1024), b''):
                digest.update(block)
        if self._fingerprint(path.stat()) != fingerprint:
            raise OSError('File changed while checking duplicates; scan again.')
        result = digest.hexdigest()
        with self._lock:
            if self.max_entries:
                self._cache[key] = (fingerprint, result)
                self._cache.move_to_end(key)
                while len(self._cache) > self.max_entries:
                    self._cache.popitem(last=False)
        return result

    def annotate(self, clips):
        """Annotate all rows in place, retaining every source and annotation.

        Returns at most 50 diagnostic strings. Unreadable or changing files keep
        a unique path key so hiding duplicates never conceals an unchecked file.
        """
        candidates = defaultdict(list)
        for clip in clips:
            clip['duplicateKey'] = 'path:' + self._path_key(clip['path'])
            clip['duplicateCount'] = 1
            clip.pop('duplicateError', None)
            if not clip.get('error'):
                candidates[(clip.get('size'), clip.get('version'), clip.get('fps'),
                            clip.get('durationFrames'), clip.get('boneCount'))].append(clip)
        warnings = []
        for group in candidates.values():
            if len(group) < 2:
                continue
            identical = defaultdict(list)
            for clip in group:
                try:
                    digest = self._digest(clip['path'], clip['size'])
                except OSError as error:
                    clip['duplicateError'] = str(error)
                    if len(warnings) < 50:
                        warnings.append(f"Could not check duplicates for {clip['path']}: {error}")
                    continue
                clip['duplicateKey'] = 'sha256:' + digest
                identical[digest].append(clip)
            for copies in identical.values():
                for clip in copies:
                    clip['duplicateCount'] = len(copies)
        return warnings
