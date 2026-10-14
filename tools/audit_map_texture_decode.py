"""Read-only complete preview texture decode/timing audit; writes no pixels."""
from collections import Counter
import json
from pathlib import Path
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from owpreview.map_formats import load_map
from owpreview.textures import TextureStore

started = time.perf_counter()
asset = load_map(sys.argv[1])
store = TextureStore()
store.register(asset['textures'], owner='maps')
edge = int(sys.argv[2]) if len(sys.argv) > 2 else 128
total = len(asset['textures'])
print(json.dumps(dict(stage='geometry', seconds=round(time.perf_counter()-started, 2), textures=total)), flush=True)
decoded_started = time.perf_counter()
errors, elapsed, sizes = [], [], Counter()
bytes_total = 0
for i, key in enumerate(asset['textures'], 1):
    before = time.perf_counter()
    try:
        result = store.load(key, edge)
        bytes_total += len(result['dataUri'])
        sizes[str((result['width'], result['height']))] += 1
    except Exception as error:
        errors.append(dict(id=key, path=asset['textures'][key]['path'], error=str(error)))
    elapsed.append(time.perf_counter()-before)
    if i % 250 == 0 or i == total:
        print(json.dumps(dict(stage='textures', done=i, total=total, errors=len(errors),
                              seconds=round(time.perf_counter()-decoded_started, 2))), flush=True)
print(json.dumps(dict(stage='complete', textureSeconds=round(time.perf_counter()-decoded_started, 2),
                      totalSeconds=round(time.perf_counter()-started, 2), decoded=total-len(errors),
                      operations=total, totalPngDataUriBytes=bytes_total, sizes=sizes, errors=errors,
                      slowestDecodeSeconds=round(max(elapsed, default=0), 3)), indent=2), flush=True)
