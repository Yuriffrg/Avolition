"""Local, read-only animation evidence. Never assigns, renames or edits assets.

The inventory separates resource identity, byte equivalence and occurrences.
Exported indexes are incomplete identities; no missing GUID bits are invented.
See ANIMATION_SOURCE_REFERENCE.md for the version-pinned exporter evidence.
"""
from __future__ import annotations

from collections import defaultdict
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import threading

from .formats import load_clip
from .animation_evidence_formats import read_model, read_entity, read_companion, read_effect
from . import animation_evidence_formats

PARSER_VERSION = '1.0.0'
TRACK_DEFINITION = 'Exported bone records in the .owanimclip header; not skeleton size or transform-channel count.'
SUFFIXES = {'.owanimclip', '.owmdl', '.owentity', '.owanim', '.oweffect'}
CONTEXT_TYPES = {'highlightintro': 'HighlightIntro', 'emote': 'Emote', 'victorypose': 'VictoryPose'}
LIMITATIONS = [
    'A shortened export identifier lacks part of the resource GUID. Local identities are scoped to this scan root.',
    'Model association and bone compatibility do not identify an action. Shared bones can match multiple models.',
    'Named unlock folders establish dependency membership, not the main actor or original internal animation name.',
    'No game build or exporting DataTool version is inferred from the currently installed Toolchain.',
]


def key(path):
    return os.path.normcase(str(Path(path).resolve()))


def digest_json(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode('utf-8')).hexdigest()


def parser_token():
    return [PARSER_VERSION, animation_evidence_formats.PARSER_VERSION]


def local_identity(root, resource, content_hash, relative_path=''):
    """Full GUIDs, if proven, remain strings. Different IDs never merge on bytes."""
    full = resource.get('fullGuid')
    if full:
        if not isinstance(full, str) or not re.fullmatch(r'(?:0x)?[0-9A-Fa-f]{16}', full):
            raise ValueError('A full GUID must be a lossless 16-hex-character string.')
        token = ['guid', full.removeprefix('0x').upper()]
    else:
        token = ['scoped', key(root), resource.get('longKey'), resource.get('type'),
                 resource.get('index'), '' if resource.get('longKey') else relative_path]
    return 'animation:' + digest_json([token, content_hash])


def exported_resource(filename):
    stem = Path(filename).stem
    # GetNameIndex may be user/lookup text. Hex-looking filenames are evidence
    # of a displayed key only, never proof of a complete 64-bit resource GUID.
    raw = stem.upper() if re.fullmatch(r'[0-9a-fA-F]{12}', stem) else None
    return {'fullGuid': None, 'longKey': raw, 'index': raw[-8:] if raw else None,
            'type': '006', 'typeProvenance': 'OWLib animation resource/exporter format',
            'display': stem, 'limitation': 'Full GUID unavailable: filename/header do not preserve all resource identity bits.'}


def evidence(kind, source, field, value, description):
    return dict(kind=kind, source=source, field=field, value=value, description=description)


def _files(root, excluded=()):
    exclusions = [Path(p).resolve() for p in excluded]
    result = []
    def unreadable(error):
        raise error
    count = 0
    for directory, dirs, names in os.walk(root, followlinks=False, onerror=unreadable):
        base = Path(directory)
        dirs[:] = sorted(d for d in dirs if d not in ('.git', '__pycache__')
                         and not (base/d).is_symlink()
                         and not any((base/d).resolve().is_relative_to(p) for p in exclusions))
        for name in sorted(names):
            count += 1
            if count > 250000:
                raise ValueError('This export exceeds 250,000 files. Select a more specific library root to analyze.')
            path = base/name
            if path.is_symlink() or not path.resolve().is_relative_to(root):
                continue
            if path.suffix.lower() in SUFFIXES or name.lower() in ('.build.info', 'manifest.json', 'export-manifest.json', 'guidnames.csv'):
                if not any(path.resolve().is_relative_to(p) for p in exclusions):
                    result.append(path)
    return sorted(result, key=lambda p: p.as_posix().casefold())


def snapshot(root, excluded=(), progress=lambda value: None):
    """Hash relevant bytes in the worker, including unparsed provenance files."""
    manifest = []
    paths = _files(root, excluded)
    for n, path in enumerate(paths):
        item = {'relativePath': path.relative_to(root).as_posix()}
        try:
            before = path.stat()
            sha = hashlib.sha256()
            with path.open('rb') as stream:
                for block in iter(lambda: stream.read(1024*1024), b''):
                    sha.update(block)
            after = path.stat()
            if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
                raise OSError('File changed during analysis; analyze again when exports finish.')
            item.update(size=after.st_size, mtimeNs=str(after.st_mtime_ns), sha256=sha.hexdigest())
        except OSError as error:
            item['error'] = str(error)
        manifest.append(item)
        if n % 25 == 0:
            progress(f'Checking local files: {n + 1} / {len(paths)}')
    return paths, manifest


def analyze(root, annotations=None, installed_toolchain=None, excluded=(), progress=lambda value: None,
            prepared=None, labels_available=True):
    root = Path(root).resolve()
    if not root.is_dir():
        raise ValueError('Scan an existing export folder before analyzing its library.')
    paths, manifest = prepared or snapshot(root, excluded, progress)
    manifest_by_rel = {m['relativePath']: m for m in manifest}
    by_path = {key(p): p for p in paths}
    annotations = annotations or {}
    errors, models, entities, companions, effects, occurrences = [], [], [], [], [], []
    inventory = dict(schema=1, parserVersion=PARSER_VERSION, root=str(root),
                     analyzedAt=datetime.now(timezone.utc).isoformat(), gameBuild=None, dataToolVersion=None,
                     installedToolchain=installed_toolchain, sourceNames=[], models=models, entities=entities,
                     companions=companions, effects=effects, assets=[], occurrences=occurrences,
                     parseErrors=errors, limitations=list(LIMITATIONS), manifest=manifest,
                     userLabelsAvailable=labels_available,
                     provenanceFiles=[m for m in manifest if Path(m['relativePath']).suffix.lower() not in SUFFIXES])

    def rel(path):
        return path.relative_to(root).as_posix()

    def resolve(reference, owner):
        """Retain all exact relative matches. Never guess a basename match."""
        if not reference:
            return []
        reference = reference.replace('\\', '/')
        if Path(reference).is_absolute() or re.match(r'^[A-Za-z]:', reference):
            return []
        bases = [owner.parent, *owner.parent.parents]
        found = {}
        for base in bases:
            if not base.is_relative_to(root):
                continue
            candidate = (base/reference).resolve()
            if candidate.is_relative_to(root) and key(candidate) in by_path:
                found[key(candidate)] = by_path[key(candidate)]
        return sorted(found.values())

    def parse(path, parser, destination):
        try:
            row = parser(path)
            row.update(path=str(path), relativePath=rel(path), sha256=manifest_by_rel[rel(path)].get('sha256'))
            row['id'] = path.suffix[1:] + ':' + digest_json([key(root), rel(path)])
            destination.append(row)
        except Exception as error:
            errors.append(dict(path=str(path), relativePath=rel(path), error=str(error)))

    def entity_targets(reference, ent, kind):
        if not reference:
            return []
        owner = Path(ent['path'])
        base = (owner.parent / ent['relativeRoot'].replace('\\', '/')).resolve()
        if not base.is_relative_to(root):
            return []
        folder, extension = ('Models', '.owmdl') if kind == 'model' else ('Entities', '.owentity')
        name = reference.replace('\\', '/')
        if '/' in name or name.lower().endswith(extension):
            return resolve(name, owner)
        filename = name[:-4] if kind == 'model' and name.upper().endswith('.00C') else name
        paths = [base/folder/name/(filename+extension)]
        # Named exports can use GetNameIndex() for the inner filename. The
        # written folder scopes candidates; the header index must also match.
        paths += [p for p in by_path.values() if p.parent == base/folder/name and p.suffix.lower() == extension]
        return sorted({p.resolve() for p in paths if key(p) in by_path})

    parsers = {'.owmdl': (read_model, models), '.owentity': (read_entity, entities),
               '.owanim': (read_companion, companions), '.oweffect': (read_effect, effects)}
    for path in paths:
        if path.suffix.lower() in parsers:
            parse(path, *parsers[path.suffix.lower()])
    model_paths = {key(m['path']): m for m in models}
    entity_paths = {key(e['path']): e for e in entities}
    model_bones = {m['id']: {b['name'] for b in m['bones']} for m in models}
    model_entities = defaultdict(set)
    for ent in entities:
        targets = entity_targets(ent.get('modelReference'), ent, 'model')
        ent['modelReferenceConflicts'] = [str(p) for p in targets if key(p) in model_paths and model_paths[key(p)]['index'] != ent['modelIndex']]
        targets = [p for p in targets if key(p) in model_paths and model_paths[key(p)]['index'] == ent['modelIndex']]
        ent['modelIds'] = [model_paths[key(p)]['id'] for p in targets if key(p) in model_paths]
        ent['effectTargets'] = [str(p) for p in resolve(ent.get('effectReference'), Path(ent['path']))]
        for model_id in ent['modelIds']:
            model_entities[model_id].add(ent['id'])
        ent['childEntityIds'] = []
        for child in ent.get('children', []):
            for p in entity_targets(child.get('reference'), ent, 'entity'):
                if key(p) in entity_paths:
                    ent['childEntityIds'].append(entity_paths[key(p)]['id'])

    # Parent entity membership is useful, but its exact path must be preserved.
    for _ in range(len(entities)):
        changed = False
        for ent in entities:
            for child_id in ent['childEntityIds']:
                for members in model_entities.values():
                    if child_id in members and ent['id'] not in members:
                        members.add(ent['id']); changed = True
        if not changed:
            break

    contexts = {}
    def named_context(path):
        parts = path.parts
        for i in range(1, len(parts)-3):
            category = CONTEXT_TYPES.get(parts[i].lower())
            # Validate Heroes/<hero>/<UnlockType>/<event>/<name>, allowing a
            # chosen scan root inside Heroes. Generic Entities/HighlightIntro
            # is deliberately not a named unlock.
            if not category or i < 2 or parts[i-2].lower() != 'heroes':
                continue
            folder = '/'.join(parts[:i+3])
            ctx_id = 'context:' + digest_json([key(root), folder])
            context = dict(id=ctx_id, label=parts[i+2], category=category, kind='named-unlock-folder',
                           role='Dependency membership; actor role unknown',
                           evidence=[evidence('export-path', rel(path), 'UnlockType/event/unlock.GetName()', folder,
                                              'Verified OWLib unlock export layout; sanitized display label, not an internal clip name.')])
            contexts.setdefault(ctx_id, context)
            return context
        return None

    occurrence_by_key = {}
    for n, path in enumerate(p for p in paths if p.suffix.lower() == '.owanimclip'):
        stat = manifest_by_rel[rel(path)]
        resource = exported_resource(path.name)
        nickname = annotations.get(key(path), {}).get('name', '')
        occurrence = dict(path=str(path), relativePath=rel(path), filename=path.name, resource=resource,
                          contentHash=stat.get('sha256'), nickname={'value': nickname, 'provenance': 'Whitehound user annotation'} if nickname else None,
                          priority=None, group=None, modelEvidence=[], contexts=[], parseErrors=[])
        parts = path.relative_to(root).parts
        for i in range(len(parts)-4):
            if parts[i].lower() == 'models' and parts[i+2].lower() == 'animations':
                model_folder = root.joinpath(*parts[:i+2])
                occurrence['priority'] = int(parts[i+3]) if re.fullmatch(r'\d+', parts[i+3]) else None
                occurrence['group'] = int(parts[i+4]) if re.fullmatch(r'\d+', parts[i+4]) else None
                for model in models:
                    if Path(model['path']).parent == model_folder:
                        occurrence['modelEvidence'].append(dict(modelId=model['id'], kind='explicit-export-path',
                            evidence=[evidence('export-path', rel(path), 'Models/<model>/Animations/<Priority>/<Group>',
                                               '/'.join(parts[:i+5]), 'Explicit model folder; raw priority/group semantics remain unproven.')]))
        context = named_context(path)
        if context:
            occurrence['contexts'].append(context)
        try:
            clip = load_clip(path)
            bones = clip['bones']
            animated = sorted({bone['name'] for bone in bones if any(bone.get(channel, {}).get('frames') for channel in ('positions', 'rotations', 'scales'))})
            channels = sum(bool(bone.get(channel, {}).get('frames')) for bone in bones for channel in ('positions', 'rotations', 'scales'))
            occurrence['clip'] = dict(durationSeconds=clip['durationSeconds'], fps=clip['fps'], trackCount=clip['boneCount'],
                                      trackDefinition=TRACK_DEFINITION, transformChannelCount=channels,
                                      boneRecordIds=[b['name'] for b in bones], animatedBoneIds=animated,
                                      warnings=clip.get('warnings', []), version=clip['version'])
        except Exception as error:
            occurrence['clip'] = dict(durationSeconds=None, fps=None, trackCount=None, trackDefinition=TRACK_DEFINITION,
                                      transformChannelCount=None, boneRecordIds=[], animatedBoneIds=[])
            err = dict(path=str(path), relativePath=rel(path), error=str(error))
            occurrence['parseErrors'].append(err); errors.append(err)
        occurrences.append(occurrence)
        occurrence_by_key[key(path)] = occurrence
        if n % 25 == 0:
            progress(f'Reading animation evidence: {n+1} clips')

    by_index = defaultdict(list)
    for occ in occurrences:
        if occ['resource'].get('index'):
            by_index[occ['resource']['index']].append(occ)
    for companion in companions:
        owner = Path(companion['path'])
        targets = resolve(companion.get('effectReference'), owner)
        companion['effectTargets'] = [str(p) for p in targets]
        companion['referenceMissing'] = bool(companion.get('effectReference') and not targets)
        clip_targets = [occurrence_by_key[key(p)] for p in resolve(companion.get('clipReference'), owner) if key(p) in occurrence_by_key]
        written_reference = companion.get('effectReference') or companion.get('clipReference') or ''
        written_stem = Path(written_reference.replace('\\', '/')).stem
        # The written resource key and scoped path accompany the low32 header.
        # Never attach a global low32 match when the target key is absent.
        model_candidates = [model_paths[key(p)] for p in resolve(companion.get('modelReference'), owner) if key(p) in model_paths]
        ref_parts = str(companion.get('effectReference') or '').replace('\\', '/').split('/')
        if not model_candidates and len(ref_parts) >= 2 and ref_parts[0].lower() == 'models':
            # Resolve the model using the written folder even when the effect
            # target itself is absent; this is explicit path evidence only.
            for model in models:
                if Path(model['path']).parent.name == ref_parts[1]:
                    parent_ref = '/'.join(ref_parts[:2]) + '/' + Path(model['path']).name
                    if Path(model['path']) in resolve(parent_ref, owner):
                        model_candidates.append(model)
        scoped = []
        for model in model_candidates:
            model_folder = Path(model['path']).parent
            export_base = model_folder.parent.parent
            for o in by_index.get(companion['animationIndex'], []):
                p = Path(o['path'])
                if (p.is_relative_to(model_folder/'Animations') or p.parent == export_base/'animations'
                        or p.parent == export_base/'Animations'):
                    scoped.append(o)
        candidates = clip_targets or [o for o in scoped
            if re.fullmatch(r'[0-9a-fA-F]{12}', written_stem)
            and o['resource']['longKey'] == written_stem.upper()]
        candidates = list({o['path']: o for o in candidates}.values())
        inconsistent = [o for o in candidates if o['resource']['index'] and o['resource']['index'] != companion['animationIndex']]
        companion['identityConflicts'] = [o['relativePath'] for o in inconsistent]
        for occ in inconsistent:
            occ.setdefault('referenceConflicts', []).append('Companion animation index disagrees with clip filename: '+companion['relativePath'])
        candidates = [o for o in candidates if o not in inconsistent]
        # Path/index disagreements and multiple payloads stay unresolved.
        identities = {(o['resource']['longKey'], o['contentHash']) for o in candidates}
        companion['candidateOccurrences'] = [o['relativePath'] for o in candidates]
        companion['ambiguous'] = len(identities) > 1 or len(model_candidates) > 1 or len(clip_targets) > 1
        if companion['ambiguous']:
            continue
        for occ in candidates:
            for model in model_candidates:
                occ['modelEvidence'].append(dict(modelId=model['id'], kind='entity-reference',
                    evidence=[evidence('companion-reference', companion['relativePath'], 'Animation.Index + model path',
                                       {'index': companion['animationIndex'], 'reference': companion.get('effectReference') or companion.get('modelReference')},
                                       'Scoped partial-index match and explicit model reference. Missing effect target does not prove an action.')]))

    # Recover complete animation GUIDs only through an exact effect->companion
    # ->clip path chain. Dangling references and basename matches are not proof.
    companion_paths = {key(c['path']): c for c in companions}
    full_candidates = defaultdict(list)
    all_effects = [(e, Path(e['path'])) for e in effects]
    all_effects += [(c['effect'], Path(c['path'])) for c in companions if c.get('effect')]
    for effect, owner in all_effects:
        for kind in ('dmce', 'cece'):
            for ref in effect.get(kind, []):
                guid = ref.get('animation', {})
                if not guid.get('fullGuid') or int(guid['fullGuid'], 16) == 0:
                    continue
                companion_targets = resolve(ref.get('animationReference'), owner)
                if len(companion_targets) != 1:
                    continue
                for cp in companion_targets:
                    comp = companion_paths.get(key(cp))
                    if not comp:
                        continue
                    clip_paths = resolve(comp.get('clipReference'), cp)
                    if len(clip_paths) != 1:
                        continue
                    for target in clip_paths:
                        occ = occurrence_by_key.get(key(target))
                        if occ:
                            if comp.get('animationIndex') != guid.get('index') or guid.get('type') != '006' or (occ['resource']['longKey'] and occ['resource']['longKey'] != guid.get('longKey')):
                                occ.setdefault('referenceConflicts', []).append('Full GUID disagrees with clip key/type: '+rel(owner))
                                continue
                            full_candidates[key(target)].append((guid, evidence('resource-reference', rel(owner), kind.upper()+'.Animation',
                                guid['fullGuid'], f'Exact reference chain: {rel(owner)} -> {rel(cp)} -> {rel(target)}')))
    for occ in occurrences:
        proven = full_candidates[key(occ['path'])]
        occ['identityConflicts'] = occ.get('referenceConflicts', [])
        if len({g['fullGuid'] for g, _ in proven}) == 1:
            occ['resource'].update(proven[0][0]); occ['resource']['limitation'] = None
            occ['identityEvidence'] = [e for _, e in proven]
        elif proven:
            occ['identityConflicts'].append('Multiple full GUID references reach this occurrence; identity remains unresolved.')

    grouped = {}
    for occ in occurrences:
        asset_id = local_identity(root, occ['resource'], occ['contentHash'], occ['relativePath'])
        occ['assetId'] = asset_id
        if asset_id not in grouped:
            grouped[asset_id] = dict(id=asset_id, resource=deepcopy(occ['resource']), contentHash=occ['contentHash'],
                sourceName=None, suggestedLabels=[], statuses=[], gameBuild=None, dataToolVersion=None,
                models=[], contexts=[], evidence=[], conflicts=[], parseErrors=[], occurrencePaths=[], **occ['clip'])
        asset = grouped[asset_id]
        asset['occurrencePaths'].append(occ['path'])
        asset['parseErrors'].extend(occ['parseErrors'])
        asset['conflicts'].extend(occ['identityConflicts'])
        asset['evidence'].extend(occ.get('identityEvidence', []))
        for ctx in occ['contexts']:
            if not any(c['id'] == ctx['id'] for c in asset['contexts']):
                asset['contexts'].append(ctx)
        for association in occ['modelEvidence']:
            present = next((m for m in asset['models'] if m['id'] == association['modelId']), None)
            if present:
                present['evidence'].extend(association['evidence'])
            else:
                asset['models'].append(dict(id=association['modelId'], kind=association['kind'], evidence=association['evidence'][:]))
    models_by_id = {m['id']: m for m in models}
    for asset in grouped.values():
        animated = set(asset['animatedBoneIds'])
        asset['skeletonComparisons'] = []
        for model in models:
            skeleton = model_bones[model['id']]
            matched = len(animated & skeleton)
            comparison = dict(modelId=model['id'], matchedBoneCount=matched, animatedBoneCount=len(animated),
                              skeletonBoneCount=len(skeleton), missingBoneIds=sorted(animated-skeleton),
                              allAnimatedBonesPresent=bool(animated) and animated <= skeleton)
            asset['skeletonComparisons'].append(comparison)
            if comparison['allAnimatedBonesPresent'] and not any(m['id'] == model['id'] for m in asset['models']):
                asset['models'].append(dict(id=model['id'], kind='compatible-bones', evidence=[
                    evidence('bone-set-comparison', model['relativePath'], 'bone names (skeleton.IDs, not positions)',
                             {'matched': matched, 'animated': len(animated)}, 'All nonempty animated bone IDs occur in this skeleton. This is a candidate, not a proven use.')]))
        for association in asset['models']:
            model = models_by_id[association['id']]
            association.update(next(c for c in asset['skeletonComparisons'] if c['modelId'] == model['id']))
            association['label'] = model['name'] or Path(model['path']).stem
            association['entityIds'] = sorted(model_entities[model['id']])
            association['entityReferences'] = [dict(id=e['id'], name=e['name'], index=e['index'], relativePath=e['relativePath'])
                                               for e in entities if e['id'] in model_entities[model['id']]]
        compatible = [m for m in asset['models'] if m['kind'] == 'compatible-bones']
        if compatible:
            asset['suggestedLabels'] = [{'value': 'Compatible skeleton: '+m['label'], 'provenance': 'Measured animated bone IDs; action unknown', 'modelId': m['id']} for m in compatible]
            asset['statuses'].append('suggested')
        if len(asset['models']) > 1:
            asset['conflicts'].append('Multiple models are supported or compatible; no single model is assigned.')
        if asset['contexts']:
            asset['statuses'].append('referenced-by-named-context')
        # Unknown describes unresolved action/source naming, independently of
        # known object associations and any non-action skeleton suggestions.
        if not asset['sourceName'] and not asset['contexts']:
            asset['statuses'].append('unknown')
        if asset['sourceName']:
            asset['statuses'].append('source-name-found')
    for companion in companions:
        if companion.get('ambiguous'):
            for occ in occurrences:
                if occ['relativePath'] in companion['candidateOccurrences']:
                    grouped[occ['assetId']]['conflicts'].append('Ambiguous partial animation index in '+companion['relativePath']+'; association withheld.')
    inventory['assets'] = list(grouped.values())
    inventory['contexts'] = list(contexts.values())
    inventory['contentGroups'] = [{'sha256': sha, 'assetIds': [a['id'] for a in grouped.values() if a['contentHash'] == sha],
                                   'occurrences': [o['relativePath'] for o in occurrences if o['contentHash'] == sha]}
                                  for sha in sorted({o['contentHash'] for o in occurrences if o['contentHash']})]
    inventory['coverage'] = dict(sourceNames=sum(bool(a['sourceName']) for a in grouped.values()),
        namedContexts=sum(bool(a['contexts']) for a in grouped.values()),
        userLabels=sum(bool(o['nickname']) for o in occurrences) if labels_available else None,
        userVerified=0, suggestions=sum(bool(a['suggestedLabels']) for a in grouped.values()),
        unknown=sum('unknown' in a['statuses'] for a in grouped.values()),
        explicitModelAssociations=sum(any(m['kind'] != 'compatible-bones' for m in a['models']) for a in grouped.values()),
        compatibleSkeletons=sum(any(c['allAnimatedBonesPresent'] for c in a['skeletonComparisons']) for a in grouped.values()),
        definition='Asset counts except userLabels (occurrences). Categories overlap; unknown means no source name or named usage context. Suggestions are skeleton compatibility only.')
    inventory['assetCount'], inventory['occurrenceCount'] = len(grouped), len(occurrences)
    inventory['parserVersions'] = parser_token()
    inventory['revision'] = digest_json([parser_token(), manifest, annotations, installed_toolchain, labels_available])
    return inventory


def index_summary(inventory):
    assets = {a['id']: a for a in inventory['assets']}
    return {field: inventory[field] for field in ('root', 'revision', 'coverage', 'assetCount', 'occurrenceCount')} | {
        'entries': [dict(path=o['path'], assetId=o['assetId'], modelIds=[m['id'] for m in assets[o['assetId']]['models']],
                         contextIds=[c['id'] for c in o['contexts']], statuses=assets[o['assetId']]['statuses']) for o in inventory['occurrences']],
        'models': [dict(id=m['id'], label=(m['name'] or Path(m['path']).stem)+' · '+m['relativePath']) for m in inventory['models']],
        'contexts': [dict(id=c['id'], label=c['category']+' · '+c['label']) for c in inventory['contexts']]}


class AnalysisManager:
    """Background parsing/cache validation; status polling performs no file I/O."""
    def __init__(self, cache_dir, annotations=lambda: {}, installed=lambda: None, excluded=lambda: [], labels_available=lambda: True):
        self.cache_dir = Path(cache_dir)
        self.annotations, self.installed, self.excluded = annotations, installed, excluded
        self.labels_available = labels_available
        self.lock = threading.RLock()
        self.records = {}

    def _record(self, root):
        return self.records.setdefault(key(root), dict(root=str(Path(root).resolve()), state='Ready', busy=False,
            message='Analyze local metadata without changing assets.', error=None, revision=None, inventory=None))

    def _directory(self, root):
        return self.cache_dir / digest_json(key(root))[:24]

    def start(self, root, cached_only=False):
        root = Path(root).resolve()
        if not root.is_dir():
            raise ValueError('The library folder is unavailable. Scan an existing folder first.')
        with self.lock:
            record = self._record(root)
            if record['busy']:
                return self.status(root, check_cache=False)
            record.update(busy=True, error=None, inventory=None, revision=None, state='Checking cache' if cached_only else 'Analyzing')
            threading.Thread(target=self._run, args=(root, cached_only), daemon=True, name='Whitehound analysis').start()
            return self.status(root, check_cache=False)

    def _run(self, root, cached_only):
        def progress(message):
            with self.lock:
                self._record(root)['message'] = message
        try:
            destination = self._directory(root)/'animation-index.json'
            if cached_only and not destination.is_file():
                with self.lock:
                    self._record(root).update(busy=False, state='Ready', message='Analyze this library to inspect local evidence.')
                return
            annotations, installed, excluded = deepcopy(self.annotations()), deepcopy(self.installed()), list(self.excluded())
            labels_available = bool(self.labels_available())
            prepared = snapshot(root, excluded, progress)
            revision = digest_json([parser_token(), prepared[1], annotations, installed, labels_available])
            inventory = None
            if destination.is_file():
                try:
                    cached = json.loads(destination.read_text(encoding='utf-8'))
                    if cached.get('revision') == revision and cached.get('parserVersion') == PARSER_VERSION:
                        inventory = cached
                except (OSError, ValueError):
                    pass
            if inventory is None:
                inventory = analyze(root, annotations, installed, excluded, progress, prepared, labels_available)
                # Do not retain a result if files changed while being parsed.
                for item in inventory['manifest']:
                    if 'mtimeNs' in item:
                        stat = (root/item['relativePath']).stat()
                        if str(stat.st_mtime_ns) != item['mtimeNs'] or stat.st_size != item['size']:
                            raise OSError('Files changed during analysis. Wait for extraction to finish, then analyze again.')
                destination.parent.mkdir(parents=True, exist_ok=True)
                temporary = destination.with_suffix('.tmp')
                temporary.write_text(json.dumps(inventory, ensure_ascii=False, indent=2), encoding='utf-8')
                temporary.replace(destination)
            with self.lock:
                self._record(root).update(inventory=inventory, revision=revision, busy=False, state='Completed',
                    message=f"{inventory['assetCount']} local assets · {inventory['occurrenceCount']} occurrences · {len(inventory['parseErrors'])} parse errors")
        except Exception as error:
            with self.lock:
                # A failed refresh never presents old evidence as current.
                self._record(root).update(busy=False, state='Failed', error=str(error), inventory=None, revision=None)

    def status(self, root, check_cache=True):
        with self.lock:
            unseen = key(root) not in self.records
            record = self._record(root)
            if unseen and check_cache:
                self.start(root, cached_only=True)
            return {k: v for k, v in record.items() if k != 'inventory'}

    def index(self, root):
        with self.lock:
            inventory = self._record(root)['inventory']
            return index_summary(inventory) if inventory else None

    def detail(self, path, root=None):
        with self.lock:
            for record in self.records.values():
                if root is not None and key(record['root']) != key(root):
                    continue
                inv = record['inventory']
                if not inv:
                    continue
                occurrence = next((o for o in inv['occurrences'] if key(o['path']) == key(path)), None)
                if occurrence:
                    asset = next(a for a in inv['assets'] if a['id'] == occurrence['assetId'])
                    result = deepcopy(dict(asset=asset, occurrence=occurrence,
                        occurrences=[o for o in inv['occurrences'] if o['assetId'] == asset['id']],
                        limitations=inv['limitations'], cache={'parserVersion': inv['parserVersion'], 'analyzedAt': inv['analyzedAt'],
                        'path': str(self._directory(inv['root'])/'animation-index.json'), 'revision': inv['revision']}))
                    nickname = self.annotations().get(key(path), {}).get('name', '')
                    result['occurrence']['nickname'] = {'value': nickname, 'provenance': 'Whitehound user annotation'} if nickname else None
                    return result
            return None
