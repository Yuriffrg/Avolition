"""Test oracle: run the reference Python implementation and print JSON.

Used only by Go tests while the Python modules remain in the repository.
Reads one JSON object from stdin:
  {"call": "owpreview.formats:load_asset", "args": [...], "kwargs": {...}}
or
  {"exec": "<python code that assigns `result`>"}
and prints {"ok": true, "result": ...} or {"ok": false, "error": "...", "type": "..."}.
"""
import base64
import importlib
import json
import math
import sys
from pathlib import Path, PurePath

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))


def plain(value):
    try:
        import numpy as np
        if isinstance(value, np.ndarray):
            return plain(value.tolist())
        if isinstance(value, np.generic):
            return plain(value.item())
    except ImportError:
        pass
    if isinstance(value, dict):
        return {str(k): plain(v) for k, v in value.items()}
    if isinstance(value, (list, tuple, set, frozenset)):
        return [plain(v) for v in value]
    if isinstance(value, PurePath):
        return str(value)
    if isinstance(value, (bytes, bytearray, memoryview)):
        return {'__bytes__': base64.b64encode(bytes(value)).decode('ascii')}
    if isinstance(value, float) and not math.isfinite(value):
        return {'__float__': repr(value)}
    if hasattr(value, '__dataclass_fields__'):
        return {k: plain(getattr(value, k)) for k in value.__dataclass_fields__}
    return value


def main():
    request = json.load(sys.stdin)
    try:
        if 'exec' in request:
            scope = {'args': request.get('args', []), 'kwargs': request.get('kwargs', {})}
            exec(request['exec'], scope)
            result = scope.get('result')
        else:
            module, _, name = request['call'].partition(':')
            target = importlib.import_module(module)
            for part in name.split('.'):
                target = getattr(target, part)
            result = target(*request.get('args', []), **request.get('kwargs', {}))
        out = {'ok': True, 'result': plain(result)}
    except Exception as error:
        out = {'ok': False, 'error': str(error), 'type': type(error).__name__}
    json.dump(out, sys.stdout, ensure_ascii=False, allow_nan=False)


if __name__ == '__main__':
    main()
