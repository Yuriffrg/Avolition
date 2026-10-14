"""Read-only material binding coverage for an exported map."""
from collections import Counter
import json
from pathlib import Path
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from owpreview.map_formats import load_map
from owpreview.map_formats import read_map
import owpreview.map_formats as map_formats

started = time.perf_counter()
failed_sources = {}
original_loader = map_formats.load_asset
def checked_loader(path, root):
    try:
        return original_loader(path, root)
    except (OSError, ValueError) as error:
        failed_sources[str(path)] = str(error)
        raise
map_formats.load_asset = checked_loader
asset = load_map(sys.argv[1])
counts = Counter()
shaders = Counter()
materials = {}
unbound = Counter()
rigged = [model for model in asset['models'] if model.get('bones')]
for model in asset['models']:
    for mesh in asset['meshPrototypes'][model['meshPrototype']]['meshes']:
        counts['meshes'] += 1
        material = model.get('materials', {}).get(mesh['materialKey'])
        if material:
            counts['bound'] += 1
            counts['withBaseColor'] += int('baseColor' in material['maps'])
            counts['withAnyMap'] += int(bool(material['maps']))
            shaders[material['shaderId']] += 1
            materials[material['sourcePath']] = material
        else:
            counts['unbound'] += 1
            unbound[(model['path'], model.get('materialLook'), mesh['materialKey'])] += 1
parsed = read_map(sys.argv[1])
missing = Counter(instance['source'] for instance in parsed['instances']
                  if not (Path(sys.argv[1]).parent / instance['source'].replace('\\', '/')).is_file())
print(json.dumps(dict(seconds=round(time.perf_counter()-started, 2), map=asset['map'], counts=counts,
                      shaders=shaders, uniqueMaterials=len(materials), textureOperations=len(asset['textures']),
                      riggedModels=len(rigged), totalBones=sum(len(model['bones']) for model in rigged),
                      uniqueRigPaths=len({model['path'] for model in rigged}),
                      missingFiles=missing, unresolved=[dict(source=k[0],look=k[1],slot=k[2],count=v) for k,v in unbound.items()],
                      failedSources=failed_sources,
                      warnings=asset['warnings'],
                      materialNames=Counter(m['name'] for m in materials.values())), indent=2))
