"""Read exported identity/reference evidence without changing preview behavior.

Schemas follow OWLib's OverwatchModel, SaveLogic.Entity and SaveLogic.Effect
writers. These are exported indexes/references, not recovered animation names.
Missing full GUIDs are never reconstructed from uint32 indexes or filenames.
All full GUIDs returned to JavaScript are hexadecimal strings.
"""
from __future__ import annotations

import math
from pathlib import Path
import re

from .formats import _Reader

PARSER_VERSION = '1'
MAX_EFFECT_RECORDS = 100_000


def decode_guid(value):
    """Decode an exact ulong using teResourceGUID's masks and reversed type bits.

    String input is a full 16-digit hexadecimal GUID (optional ``0x`` prefix),
    never OWLib's shortened ``LongKey.Type`` display notation.
    """
    if isinstance(value, str):
        clean = value[2:] if value.lower().startswith('0x') else value
        if not re.fullmatch(r'[0-9a-fA-F]{16}', clean):
            raise ValueError('A full GUID requires exactly 16 hexadecimal digits.')
        value = int(clean, 16)
    if isinstance(value, bool) or not isinstance(value, int) or not 0 <= value <= 0xFFFFFFFFFFFFFFFF:
        raise ValueError('GUID must be an unsigned 64-bit integer or full hexadecimal string.')
    mangled_type = (value >> 48) & 0xFFF
    resource_type = int(f'{mangled_type:012b}'[::-1], 2) + 1
    result = {'fullGuid': f'{value:016X}', 'longKey': f'{value & 0xFFFFFFFFFFFF:012X}',
              'type': f'{resource_type:03X}', 'index': f'{value & 0xFFFFFFFF:08X}',
              'engine': f'{value >> 60:X}', 'isNull': value == 0}
    result['display'] = result['longKey'] + '.' + result['type']
    return result


def _start(path, magic=None, versions=()):
    reader = _Reader.file(Path(path))
    result = _header(reader, magic, versions)
    return reader, result


def _header(reader, magic, versions):
    if magic is not None and reader.string() != magic:
        reader.fail(f'invalid {magic} identifier')
    major, minor = reader.unpack('HH')
    if (major, minor) not in versions:
        reader.fail(f'unsupported {magic or "model"} evidence version {major}.{minor}')
    return {'path': str(reader.path), 'version': f'{major}.{minor}', 'warnings': []}


def _index(value):
    return f'{value:08X}'


def _float(reader, label, minimum=None):
    value = reader.unpack('f')[0]
    if not math.isfinite(value) or (minimum is not None and value < minimum):
        reader.fail(f'invalid {label}')
    return value


def _complete(reader, result):
    result['trailingBytes'] = reader.remaining
    if reader.remaining:
        result['warnings'].append(f'{reader.remaining} trailing bytes are not interpreted as evidence.')
    return result


def read_model(path):
    """Read the header and skeleton only; geometry is outside this evidence parser."""
    reader, result = _start(path, versions=((2, 0),))
    material, name = reader.string(), reader.string()
    index, bone_count, mesh_count, socket_count = reader.unpack('IHII')
    reader.count(mesh_count, 16384, 'mesh count')
    reader.count(socket_count, 65536, 'socket count')
    bones, names = [], set()
    for _ in range(bone_count):
        bone_name = reader.string()
        parent = reader.unpack('h')[0]
        if not bone_name or bone_name in names:
            reader.fail('missing or duplicate skeleton bone identifier')
        if not -1 <= parent < bone_count or parent == len(bones):
            reader.fail('invalid skeleton parent index')
        names.add(bone_name)
        reader.values('f', 9)  # Bind transforms are irrelevant to identity, but validate them.
        bones.append({'name': bone_name, 'parentIndex': parent})
    done = set()
    for start in range(bone_count):
        visiting, current = set(), start
        while current >= 0 and current not in done:
            if current in visiting:
                reader.fail('cycle in skeleton hierarchy')
            visiting.add(current)
            current = bones[current]['parentIndex']
        done.update(visiting)
    result.update(name=name, index=_index(index), fullGuid=None, materialReference=material,
                  boneCount=bone_count, meshCount=mesh_count, socketCount=socket_count,
                  bones=bones, unparsedBytes=reader.remaining,
                  sourceFields={'index': 'teResourceGUID.Index(OverwatchModel.GUID)',
                                'bones.name': 'skeleton.IDs; synthetic negative array index for cloth bones'})
    return result


def read_entity(path):
    reader, result = _start(path, 'owentity', ((2, 0), (2, 1)))
    name, model, effect = reader.string(), reader.string(), reader.string()
    index, model_index, effect_index, count = reader.unpack('IIIi')
    reader.count(count, 4096, 'child entity count')
    children = []
    for _ in range(count):
        reference = reader.string()
        hardpoint, identifier, hardpoint_index, identifier_index = reader.unpack('QQII')
        attachment = reader.string()
        children.append({'reference': reference, 'hardpoint': decode_guid(hardpoint),
                         'identifier': decode_guid(identifier), 'hardpointIndex': _index(hardpoint_index),
                         'identifierIndex': _index(identifier_index), 'attachment': attachment})
    look, relative = (reader.string(), reader.string()) if result['version'] == '2.1' else ('', '../../')
    result.update(name=name, index=_index(index), modelIndex=_index(model_index),
                  effectIndex=_index(effect_index), fullGuid=None, modelReference=model,
                  effectReference=effect, children=children, materialLookReference=look,
                  relativeRoot=relative, sourceFields={
                      'index': 'teResourceGUID.Index(Entity.m_GUID)',
                      'modelReference': 'Info.m_models[Entity.m_modelGUID].GetName()',
                      'effectReference': 'Info.m_effects[Entity.m_effectGUID].GetName()',
                      'children.reference': 'Info.m_entities[child.m_defGUID].GetName()',
                      'children.hardpoint': 'child.m_hardpointGUID',
                      'children.identifier': 'child.m_identifier'})
    return _complete(reader, result)


def _playback(reader):
    absent = reader.unpack('B')[0]
    if absent not in (0, 1):
        reader.fail('invalid playback time-presence flag')
    start, end = _float(reader, 'playback start'), _float(reader, 'playback end')
    return {'timeInfoAbsent': bool(absent), 'start': start, 'end': end,
            'hardpointReference': reader.string()}


def _read_effect(reader):
    result = _header(reader, 'oweffect', ((1, 2),))
    result['index'] = _index(reader.unpack('I')[0])
    result['fullGuid'] = None
    result['duration'] = _float(reader, 'effect duration', 0)
    kinds = ('DMCE', 'CECE', 'NECE', 'RPCE', 'FECE', 'OSCE', 'SVCE')
    counts = dict(zip(kinds, reader.unpack('7i')))
    for kind, count in counts.items():
        reader.count(count, MAX_EFFECT_RECORDS, kind + ' count')
    if sum(counts.values()) > MAX_EFFECT_RECORDS:
        reader.fail('effect exceeds evidence record limit')
    result.update(counts=counts, dmce=[], cece=[], nece=[], rpce=[], svce=[])
    for _ in range(counts['DMCE']):
        playback = _playback(reader)
        animation, material, model = reader.unpack('QQQ')
        result['dmce'].append({'playback': playback, 'animation': decode_guid(animation),
                               'material': decode_guid(material), 'model': decode_guid(model),
                               'modelReference': reader.string(), 'animationReference': reader.string()})
    for _ in range(counts['CECE']):
        playback = _playback(reader)
        action, animation, identifier, index = reader.unpack('BQQI')
        result['cece'].append({'playback': playback, 'action': action,
                               'animation': decode_guid(animation), 'identifier': decode_guid(identifier),
                               'identifierIndex': _index(index), 'animationReference': reader.string()})
    for _ in range(counts['NECE']):
        playback = _playback(reader)
        entity, identifier_index = reader.unpack('QI')
        result['nece'].append({'playback': playback, 'entity': decode_guid(entity),
                               'identifierIndex': _index(identifier_index), 'entityReference': reader.string()})
    for _ in range(counts['RPCE']):
        playback = _playback(reader)
        model, material = reader.unpack('QQ')
        result['rpce'].append({'playback': playback, 'model': decode_guid(model),
                               'material': decode_guid(material), 'modelReference': reader.string()})
    # The verified writer emits FECE/OSCE counts, but no corresponding payload loops.
    for kind in ('FECE', 'OSCE'):
        if counts[kind]:
            result['warnings'].append(f'{kind} count is {counts[kind]}, but the v1.2 writer omits its records.')
    remaining_budget = MAX_EFFECT_RECORDS - sum(counts.values())
    for _ in range(counts['SVCE']):
        playback = _playback(reader)
        stimulus_index, line_count = reader.unpack('Ii')
        reader.count(line_count, remaining_budget, 'voice line count')
        remaining_budget -= line_count
        lines = []
        for _ in range(line_count):
            sound_count = reader.unpack('i')[0]
            reader.count(sound_count, remaining_budget, 'voice sound count')
            remaining_budget -= sound_count
            lines.append([reader.string() for _ in range(sound_count)])
        result['svce'].append({'playback': playback, 'voiceStimulusIndex': _index(stimulus_index),
                               'soundReferences': lines})
    result['sourceFields'] = {
        'index': 'teResourceGUID.Index(EffectInfo.Effect.GUID)',
        'dmce.animation': 'EffectParser.DMCEInfo.Animation', 'dmce.model': 'EffectParser.DMCEInfo.Model',
        'dmce.material': 'EffectParser.DMCEInfo.Material', 'cece.action': 'EffectParser.CECEInfo.Action',
        'cece.animation': 'EffectParser.CECEInfo.Animation', 'cece.identifier': 'EffectParser.CECEInfo.Identifier',
        'nece.entity': 'EffectParser.NECEInfo.GUID', 'nece.identifierIndex': 'teResourceGUID.Index(NECEInfo.Identifier)'}
    return result


def read_effect(path):
    reader = _Reader.file(Path(path))
    return _complete(reader, _read_effect(reader))


def read_companion(path):
    reader, result = _start(path, 'owanim', ((1, 0),))
    index = reader.unpack('I')[0]
    fps = _float(reader, 'animation frame rate', 0)
    if not 0 < fps <= 1000:
        reader.fail('invalid animation frame rate')
    kind = reader.unpack('i')[0]
    if kind not in (0, 1):
        reader.fail(f'unsupported animation companion kind {kind}')
    result.update(animationIndex=_index(index), fullGuid=None, fps=fps, kind=kind,
                  clipReference=None, modelReference=None, effectReference=None, effect=None,
                  sourceFields={'animationIndex': 'teResourceGUID.Index(Animation.m_GUID)',
                                'kind': 'OverwatchAnimationEffect.OWAnimType'})
    if kind == 0:
        result['clipReference'], result['modelReference'] = reader.string(), reader.string()
        result['effect'] = _read_effect(reader)
    else:
        result['effectReference'] = reader.string()
    return _complete(reader, result)
