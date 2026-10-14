"""Bake representative real map materials to a caller-selected review directory.

Reads exported sources only. This checks texture conversion, not a native C4D save.
"""
from collections import Counter
import hashlib
import json
from pathlib import Path
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from owpreview.export_textures import bake_materials
from owpreview.map_formats import load_map
from PIL import Image

started = time.perf_counter()
asset = load_map(sys.argv[1])
selected = {}
for model in asset['models']:
    for material in model.get('materials', {}).values():
        shader = material['shaderId']
        if shader not in selected or len(material['maps']) > len(selected[shader]['maps']):
            selected[shader] = material
sample = dict(asset, models=[dict(id='shader-' + str(shader), name='Shader ' + str(shader),
                                  materials={'sample': material}) for shader, material in selected.items()], warnings=[])
sources = {Path(material['sourcePath']) for material in selected.values()}
sources.update(Path(asset['textures'][mapping['textureId']]['path']) for material in selected.values()
               for mapping in material['maps'].values())
sources.add(Path(sys.argv[1]))
before = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in sources}
result = bake_materials(sample, Path(sys.argv[2]) / 'tex')
unchanged = all(hashlib.sha256(Path(path).read_bytes()).hexdigest() == digest for path, digest in before.items())
if not unchanged:
    raise RuntimeError('Source file content changed during the bake audit.')
sizes = Counter()
for filename in result['files']:
    with Image.open(filename) as image:
        sizes[str(image.size)] += 1
report = dict(seconds=round(time.perf_counter()-started, 2), sampledShaders=sorted(selected),
              materialCount=len(selected), files=len(result['files']),
              totalBytes=sum(Path(name).stat().st_size for name in result['files']),
              sourceHashesVerified=len(before), sourceFilesUnchanged=unchanged,
              sizes=sizes, warnings=result['warnings'])
(Path(sys.argv[2]) / 'report.json').write_text(json.dumps(report, indent=2), encoding='utf-8')
print(json.dumps(report, indent=2))
