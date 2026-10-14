"""NumPy counterpart of web/pose.js, used for deterministic sampled exports."""
from bisect import bisect_right
import math
import numpy as np


def normalize(q):
    q = np.asarray(q, dtype=float)
    length = np.linalg.norm(q)
    return q / length if length else np.array([0., 0., 0., 1.])


def multiply(a, b):
    ax, ay, az, aw = a; bx, by, bz, bw = b
    return normalize([aw*bx+ax*bw+ay*bz-az*by, aw*by-ax*bz+ay*bw+az*bx,
                      aw*bz+ax*by-ay*bx+az*bw, aw*bw-ax*bx-ay*by-az*bz])


def rotation(q):
    x, y, z, w = normalize(q)
    return np.array([[1-2*(y*y+z*z), 2*(x*y-z*w), 2*(x*z+y*w)],
                     [2*(x*y+z*w), 1-2*(x*x+z*z), 2*(y*z-x*w)],
                     [2*(x*z-y*w), 2*(y*z+x*w), 1-2*(x*x+y*y)]])


def compose(p, q, s):
    m = np.eye(4); m[:3, :3] = rotation(q) @ np.diag(s); m[:3, 3] = p
    return m


def euler_xyz(matrix, previous=None):
    """FBX/Blender XYZ Euler angles, radians, with nearest equivalent branch."""
    m = np.asarray(matrix)[:3, :3]
    y = math.asin(float(np.clip(-m[2, 0], -1, 1)))
    if abs(math.cos(y)) > 1e-8:
        x, z = math.atan2(m[2, 1], m[2, 2]), math.atan2(m[1, 0], m[0, 0])
    else:
        x, z = math.atan2(-m[1, 2], m[1, 1]), 0.
    a = np.array([x, y, z])
    if previous is not None:
        candidates = [a, np.array([x + math.pi, math.pi - y, z + math.pi])]
        candidates = [v + 2*math.pi*np.round((previous-v)/(2*math.pi)) for v in candidates]
        a = min(candidates, key=lambda v: np.linalg.norm(v-previous))
    return a


def decompose(matrix):
    m = np.asarray(matrix, dtype=float)
    s = np.linalg.norm(m[:3, :3], axis=0)
    if np.linalg.det(m[:3, :3]) < 0: s[0] *= -1
    if np.any(abs(s) < 1e-12): raise ValueError('Cannot export a singular attachment transform.')
    r = m[:3, :3] / s
    if not np.allclose(r.T @ r, np.eye(3), atol=1e-5):
        raise ValueError('Cannot export an attachment containing shear as FBX TRS.')
    return m[:3, 3].copy(), euler_xyz(r), s


class PoseEvaluator:
    def __init__(self, bones, clip_bones=()):
        self.bones = bones
        self.parents = [int(b.get('parent', -1)) for b in bones]
        children = [[] for _ in bones]
        self.order = []
        for i, p in enumerate(self.parents):
            if p < -1 or p >= len(bones) or p == i: raise ValueError('Invalid skeleton parent.')
            (self.order if p == -1 else children[p]).append(i)
        for i in self.order: self.order.extend(children[i])
        if len(self.order) != len(bones): raise ValueError('Skeleton contains a parent cycle.')
        by_name = {b['name']: b for b in clip_bones}
        self.rest_p = [np.array(b.get('pos', [0, 0, 0]), dtype=float) for b in bones]
        self.rest_q = [normalize(b.get('rot', [0, 0, 0, 1])) for b in bones]
        self.tracks = []
        for i, bone in enumerate(bones):
            clip = by_name.get(bone['name'], {})
            tracks = {}
            for name, n in [('positions', 3), ('rotations', 4), ('scales', 3)]:
                track = clip.get(name)
                if not track or not track.get('frames'): continue
                values = np.asarray(track['values'], dtype=float).reshape((-1, n))
                if len(values) != len(track['frames']) or not np.isfinite(values).all():
                    raise ValueError('Invalid animation track.')
                keys = {}
                for frame, value in zip(track['frames'], values):
                    if n == 4:
                        value = normalize(value)
                        if np.dot(self.rest_q[i], value) < 0: value = -value
                    keys[frame] = value
                frames = sorted(keys)
                tracks[name] = (frames, np.array([keys[f] for f in frames]))
            self.tracks.append(tracks)

    def _sample(self, i, name, frame, default):
        track = self.tracks[i].get(name)
        if track is None: return np.array(default, dtype=float), False
        frames, values = track
        idx = bisect_right(frames, frame)
        if idx == 0: v = values[0].copy()
        elif idx == len(frames): v = values[-1].copy()
        else:
            t = (frame-frames[idx-1])/(frames[idx]-frames[idx-1])
            v = values[idx-1]*(1-t) + values[idx]*t
        return (normalize(v) if name == 'rotations' else v), True

    def evaluate(self, frame, lock_root=False):
        positions, quaternions, scales = [None]*len(self.bones), [None]*len(self.bones), [None]*len(self.bones)
        world, world_q = [None]*len(self.bones), [None]*len(self.bones)
        for i in self.order:
            p, has_p = self._sample(i, 'positions', frame, self.rest_p[i])
            q, _ = self._sample(i, 'rotations', frame, self.rest_q[i])
            s, _ = self._sample(i, 'scales', frame, [1, 1, 1])
            s = np.where(abs(s) < 1e-6, np.where(s < 0, -1e-6, 1e-6), s)
            parent = self.parents[i]
            if parent < 0:
                if has_p: p = rotation(self.rest_q[i]) @ p + self.rest_p[i]
                if lock_root: p[[0, 2]] = self.rest_p[i][[0, 2]]
                wp, wq = p, q
            else:
                wp = (world[parent] @ np.r_[p, 1.])[:3]
                wq = multiply(world_q[parent], q)
            positions[i], quaternions[i], scales[i] = p, q, s
            world_q[i], world[i] = wq, compose(wp, wq, s)
        return {'positions': positions, 'quaternions': quaternions, 'scales': scales, 'world': world}
