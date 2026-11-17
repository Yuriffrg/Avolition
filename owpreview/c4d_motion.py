"""Native Motion System construction for a detached exported document.

Cinema stores Motion Sources as ordinary object hierarchies in the document's
NLA branch, and layers/clips in NLA branches on a Motion System tag. This follows
Maxon's examples at developers.maxon.net/forum/topic/16392 and /forum/post/55975.
No commands, dialogs, active-document changes or expression baking are used.
"""

# The public Python SDK exposes Tmotionsystem and Omotionclip, but no symbol for
# the layer type or these source range fields (Maxon's documented sample IDs).
MOTION_LAYER = 465003001
SOURCE_START = 465003056
SOURCE_END = 465003057


def _children(first):
    while first is not None:
        yield first
        first = first.GetNext()


def _tree(root):
    yield root
    for child in _children(root.GetDown()):
        yield from _tree(child)


def _tracks(obj):
    return _children(obj.GetFirstCTrack())


def _motion_head(c4d, owner):
    # Layers expose two NLAbase branches: the plugin's Motion Layer comes
    # first; BaseList2D.GetNLARoot() addresses the separate Animation Layer.
    # Inserting a clip into that generic branch creates a non-playing clip.
    # Select the plugin branch by its stable ID/order rather than localized name.
    for branch in owner.GetBranchInfo(c4d.GETBRANCHINFO_NONE):
        if branch['id'] == c4d.NLAbase and branch.get('head') is not None:
            return branch['head']
    raise RuntimeError('Cinema 4D did not create the native Motion System branch.')


def _included(c4d, track):
    """Position, Rotation and Parameter enabled; Scale and PLA disabled."""
    return not is_scale_track(c4d, track) and track.GetType() != c4d.CTpla


def is_scale_track(c4d, track):
    param = track.GetDescriptionID()[0].id
    scale = {c4d.ID_BASEOBJECT_REL_SCALE, c4d.ID_BASEOBJECT_ABS_SCALE,
             c4d.ID_BASEOBJECT_FROZEN_SCALE}
    return param in scale


def _source_tree(c4d, original, members):
    # Cloning individual objects excludes expression/weight tags and unrelated
    # branches while retaining the hierarchy/type identity used by NLA mapping.
    source = original.GetClone(c4d.COPYFLAGS_NO_HIERARCHY | c4d.COPYFLAGS_NO_BRANCHES)
    if source is None:
        raise RuntimeError('Cinema 4D could not copy a Motion Source object.')
    for track in _tracks(original):
        if _included(c4d, track):
            copied = track.GetClone(c4d.COPYFLAGS_NONE)
            if copied is None:
                raise RuntimeError('Cinema 4D could not copy a Motion Source track.')
            source.InsertTrackSorted(copied)
    for child in _children(original.GetDown()):
        # Keep the paths connecting animated skeletons, but not mesh branches
        # or unrelated attachments. A single source drives the complete setup.
        if child in members:
            _source_tree(c4d, child, members).InsertUnderLast(source)
    return source


def create_motion_clips(c4d, doc, name, duration, rigs):
    """Move a combined setup into one named source and one native Motion Clip.

    Each rig is an explicit (skeleton_null, owned_joints) pair constructed by the
    scene builder. The outer animated skeleton owns the tag; nested weapon rigs
    and their connector nodes share its source without nested Motion tags.
    """
    roots = [(root, [root, *joints]) for root, joints in rigs
             if any(_included(c4d, track) for obj in [root, *joints] for track in _tracks(obj))]
    if not roots:
        return dict(motionClipCount=0, motionSourceNames=[], motionTrackCount=0)

    def ancestors(obj):
        while obj is not None:
            yield obj
            obj = obj.GetUp()

    root = next((candidate for candidate, _ in roots
                 if all(candidate in list(ancestors(other)) for other, _ in roots)), None)
    if root is None:
        # Disjoint rigs need one dedicated owner; do not put a Motion tag on
        # their entity root. Move only topmost skeleton branches, keeping any
        # nested weapon hierarchy intact and preserving each world transform.
        topmost = [candidate for candidate, _ in roots
                   if not any(other != candidate and other in list(ancestors(candidate))
                              for other, _ in roots)]
        common = next((candidate for candidate in ancestors(topmost[0].GetUp())
                       if all(candidate in list(ancestors(other)) for other in topmost)), None)
        matrices = [(obj, obj.GetMg()) for obj in topmost]
        root = c4d.BaseObject(c4d.Onull)
        if root is None:
            raise RuntimeError('Cinema 4D could not create the shared Joints parent.')
        root.SetName('Joints')
        if common is not None:
            root.InsertUnderLast(common)
        else:
            doc.InsertObject(root)
        for obj, matrix in matrices:
            obj.Remove()
            obj.InsertUnderLast(root)
            obj.SetMg(matrix)
    members = []
    for _, owned in roots:
        for obj in owned:
            for node in ancestors(obj):
                if node not in members:
                    members.append(node)
                if node == root:
                    break
    moved = 0
    # The scene builder passes an exact frame/fps BaseTime. Converting it to
    # float seconds and back would turn frame 49 at 30 FPS into frame 48.99.
    end = duration if isinstance(duration, c4d.BaseTime) else c4d.BaseTime(duration)
    source = _source_tree(c4d, root, members)
    source.SetName(name)
    source[SOURCE_START] = c4d.BaseTime(0)
    source[SOURCE_END] = end
    source.InsertUnderLast(doc.GetNLARoot())

    tag = c4d.BaseTag(c4d.Tmotionsystem)
    if tag is None:
        raise RuntimeError('Cinema 4D could not create a Motion System tag.')
    root.InsertTag(tag)
    layer = c4d.BaseList2D(MOTION_LAYER)
    clip = c4d.BaseList2D(c4d.Omotionclip)
    if layer is None or clip is None:
        raise RuntimeError('Cinema 4D could not create a Motion Clip.')
    layer.SetName('Layer 0')
    layer.InsertUnderLast(_motion_head(c4d, tag))
    layer[c4d.ID_MT_LAYER_VALUE] = 1.0
    layer[c4d.ID_MT_LAYER_MODE] = c4d.ID_MT_LAYER_MODE_MIX
    clip.SetName(name)
    clip.InsertUnderLast(_motion_head(c4d, layer))
    clip[c4d.ID_MT_CLIP_SOURCE] = source
    clip[c4d.ID_MT_CLIP_START] = c4d.BaseTime(0)
    clip[c4d.ID_MT_CLIP_END] = end
    # Cinema's default Timing spline is a Bezier identity. Its numerical
    # inversion shifts source samples off their keys, which becomes visible on
    # fast finger/attachment motion. Linear knots retain the exact identity
    # mapping, including 60 FPS source frames, without changing any key times.
    timing = c4d.SplineData()
    if not timing.MakeLinearSplineLinear(2):
        raise RuntimeError('Cinema 4D could not create the Motion Clip timing spline.')
    clip[c4d.ID_MT_CLIP_TIMWARPWND] = timing
    for obj in members:
        for track in list(_tracks(obj)):
            if _included(c4d, track):
                track.Remove()
                moved += 1
    return dict(motionClipCount=1, motionSourceNames=[name], motionTrackCount=moved)


def motion_snapshot(c4d, doc, frame_count):
    """Validate native clip links, source keys and ranges after save/reload."""
    sources = []
    head = doc.GetNLARoot(False)
    for source in _children(head.GetDown() if head else None):
        count = 0
        hierarchy = []
        for obj in _tree(source):
            tracks = []
            for track in _tracks(obj):
                curve = track.GetCurve()
                if curve is None or curve.GetKeyCount() != frame_count or not _included(c4d, track):
                    raise RuntimeError('Saved Motion Source did not retain the included animation keys.')
                desc = track.GetDescriptionID()
                tracks.append(tuple(desc[i].id for i in range(desc.GetDepth())))
                count += 1
            hierarchy.append((obj.GetName(), obj.GetType(), tracks))
        sources.append(dict(name=source.GetName(), count=count, hierarchy=hierarchy,
                            start=source[SOURCE_START].Get(), end=source[SOURCE_END].Get()))
    clips = []
    def positioned_tree(first, parent=()):
        for index, obj in enumerate(_children(first)):
            path = parent + (index,)
            yield path, obj
            yield from positioned_tree(obj.GetDown(), path)

    for owner_path, root in positioned_tree(doc.GetFirstObject()):
        if any(is_scale_track(c4d, track) for track in _tracks(root)):
            raise RuntimeError('Saved Cinema 4D scene contains unexpected scale animation tracks.')
        tag = root.GetFirstTag()
        while tag is not None:
            if tag.GetType() == c4d.Tmotionsystem:
                head = _motion_head(c4d, tag)
                for layer in _children(head.GetDown() if head else None):
                    clip_head = _motion_head(c4d, layer)
                    for clip in _children(clip_head.GetDown() if clip_head else None):
                        source = clip[c4d.ID_MT_CLIP_SOURCE]
                        if source is None or not any(source.GetName() == item['name'] for item in sources):
                            raise RuntimeError('Saved Motion Clip has lost its Motion Source.')
                        clips.append(dict(name=clip.GetName(), source=source.GetName(),
                                          layer=layer.GetName(),
                                          owner=owner_path, ownerName=root.GetName(), ownerType=root.GetType(),
                                          start=clip[c4d.ID_MT_CLIP_START].Get(),
                                          end=clip[c4d.ID_MT_CLIP_END].Get(),
                                          weight=layer[c4d.ID_MT_LAYER_VALUE],
                                          mode=layer[c4d.ID_MT_LAYER_MODE]))
            tag = tag.GetNext()
    return dict(sources=sources, clips=clips)
