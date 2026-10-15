// Package exportpose is the Go port of owpreview/export_pose.py: the NumPy
// counterpart of web/pose.js, used for deterministic sampled exports (FBX and
// native Cinema 4D payloads).
//
// Python evaluates everything in NumPy float64; so does this package, in the
// same operation order. NumPy hands matrix products and vector norms to
// OpenBLAS; Mul, Normalize and the evaluator reproduce the summation order
// and fused multiply-adds of OpenBLAS's x86-64 (Haswell and later) kernels,
// so products are bit-identical there. np.linalg.inv (LAPACK) and the C
// library's asin/atan2 are not reproduced bit for bit: those results agree
// with Python to ~1e-15 relative, well within the 1e-9 contract.
//
// Matrices are row-major like NumPy arrays (m[row][col]). Asset/clip data
// uses column-major Three.js order (formats.Mat4); FromColumnMajor and
// (Mat4).ColumnMajor convert, like `np.asarray(v).reshape(4, 4).T` and
// `m.T.reshape(-1)`.
//
// # Python → Go map
//
//	normalize        Normalize
//	multiply         Multiply
//	rotation         Rotation
//	compose          Compose
//	euler_xyz        EulerXYZ
//	decompose        Decompose
//	PoseEvaluator    Evaluator (NewEvaluator, Evaluate)
//	np.linalg.inv    (Mat4).Inverse
package exportpose

import (
	"errors"
	"math"
	"sort"

	"avolition/gocore/internal/formats"
)

// Mat3 is a row-major 3x3 matrix.
type Mat3 [3][3]float64

// Mat4 is a row-major 4x4 matrix.
type Mat4 [4][4]float64

// Identity4 is np.eye(4).
var Identity4 = Mat4{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}}

// FromColumnMajor is np.asarray(values).reshape(4, 4).T.
func FromColumnMajor(values formats.Mat4) Mat4 {
	var m Mat4
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			m[row][col] = values[col*4+row]
		}
	}
	return m
}

// ColumnMajor is m.T.reshape(-1).tolist().
func (m *Mat4) ColumnMajor() formats.Mat4 {
	var out formats.Mat4
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			out[col*4+row] = m[row][col]
		}
	}
	return out
}

// Mul is m @ n. Like OpenBLAS's dgemm kernels that NumPy uses, every element
// is a chain of fused multiply-adds starting from +0.
func (m *Mat4) Mul(n *Mat4) Mat4 {
	var out Mat4
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			sum := math.FMA(m[i][0], n[0][j], 0)
			sum = math.FMA(m[i][1], n[1][j], sum)
			sum = math.FMA(m[i][2], n[2][j], sum)
			out[i][j] = math.FMA(m[i][3], n[3][j], sum)
		}
	}
	return out
}

// apply3 is (m @ np.r_[v, 1.])[:3]: OpenBLAS's dgemv order for a 4x4
// row-major matrix, (a0*v0 + a2*v2) + (a1*v1 + a3*v3) without fusion.
func (m *Mat4) apply3(v [3]float64) [3]float64 {
	var out [3]float64
	for row := 0; row < 3; row++ {
		out[row] = (float64(m[row][0]*v[0]) + float64(m[row][2]*v[2])) + (float64(m[row][1]*v[1]) + m[row][3])
	}
	return out
}

// mulVec is m @ v for a 3x3 matrix, in OpenBLAS's dgemv order.
func (m *Mat3) mulVec(v [3]float64) [3]float64 {
	var out [3]float64
	for row := 0; row < 3; row++ {
		out[row] = math.FMA(m[row][2], v[2], math.FMA(m[row][0], v[0], float64(m[row][1]*v[1])))
	}
	return out
}

// Upper3 is m[:3, :3].
func (m *Mat4) Upper3() Mat3 {
	return Mat3{{m[0][0], m[0][1], m[0][2]}, {m[1][0], m[1][1], m[1][2]}, {m[2][0], m[2][1], m[2][2]}}
}

// ErrSingular is NumPy's LinAlgError for np.linalg.inv of a singular matrix.
var ErrSingular = errors.New("Singular matrix")

// Inverse is np.linalg.inv: Gaussian elimination with partial pivoting
// (LAPACK dgesv's strategy). It fails with ErrSingular for an exactly
// singular matrix.
func (m *Mat4) Inverse() (Mat4, error) {
	a := *m
	inv := Identity4
	for col := 0; col < 4; col++ {
		pivot := col
		for row := col + 1; row < 4; row++ {
			if math.Abs(a[row][col]) > math.Abs(a[pivot][col]) {
				pivot = row
			}
		}
		if a[pivot][col] == 0 {
			return Mat4{}, ErrSingular
		}
		a[col], a[pivot] = a[pivot], a[col]
		inv[col], inv[pivot] = inv[pivot], inv[col]
		d := a[col][col]
		for row := 0; row < 4; row++ {
			if row == col {
				continue
			}
			f := a[row][col] / d
			if f == 0 {
				continue
			}
			for k := 0; k < 4; k++ {
				a[row][k] -= float64(f * a[col][k])
				inv[row][k] -= float64(f * inv[col][k])
			}
		}
	}
	for row := 0; row < 4; row++ {
		d := a[row][row]
		for k := 0; k < 4; k++ {
			inv[row][k] /= d
		}
	}
	return inv, nil
}

// Normalize is normalize(q): unit length, or (0, 0, 0, 1) for a zero vector.
// np.linalg.norm of a vector is sqrt(ddot): fused, in order.
func Normalize(q [4]float64) [4]float64 {
	length := math.Sqrt(math.FMA(q[3], q[3], math.FMA(q[2], q[2], math.FMA(q[1], q[1], float64(q[0]*q[0])))))
	if length == 0 {
		return [4]float64{0, 0, 0, 1}
	}
	return [4]float64{q[0] / length, q[1] / length, q[2] / length, q[3] / length}
}

// Multiply is multiply(a, b): the normalized Hamilton product of XYZW
// quaternions.
func Multiply(a, b [4]float64) [4]float64 {
	ax, ay, az, aw := a[0], a[1], a[2], a[3]
	bx, by, bz, bw := b[0], b[1], b[2], b[3]
	return Normalize([4]float64{
		float64(aw*bx) + float64(ax*bw) + float64(ay*bz) - float64(az*by),
		float64(aw*by) - float64(ax*bz) + float64(ay*bw) + float64(az*bx),
		float64(aw*bz) + float64(ax*by) - float64(ay*bx) + float64(az*bw),
		float64(aw*bw) - float64(ax*bx) - float64(ay*by) - float64(az*bz),
	})
}

// Rotation is rotation(q): the 3x3 rotation matrix of a (normalized) XYZW
// quaternion.
func Rotation(q [4]float64) Mat3 {
	n := Normalize(q)
	x, y, z, w := n[0], n[1], n[2], n[3]
	return Mat3{
		{1 - 2*(float64(y*y)+float64(z*z)), 2 * (float64(x*y) - float64(z*w)), 2 * (float64(x*z) + float64(y*w))},
		{2 * (float64(x*y) + float64(z*w)), 1 - 2*(float64(x*x)+float64(z*z)), 2 * (float64(y*z) - float64(x*w))},
		{2 * (float64(x*z) - float64(y*w)), 2 * (float64(y*z) + float64(x*w)), 1 - 2*(float64(x*x)+float64(y*y))},
	}
}

// Compose is compose(p, q, s): translation p, rotation q, scale s.
func Compose(p [3]float64, q [4]float64, s [3]float64) Mat4 {
	r := Rotation(q)
	m := Identity4
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			// rotation(q) @ np.diag(s) through dgemm: an FMA chain from +0
			// over the diagonal's zeros, so a -0 product becomes +0.
			sum := 0.0
			for k := 0; k < 3; k++ {
				d := 0.0
				if k == j {
					d = s[j]
				}
				sum = math.FMA(r[i][k], d, sum)
			}
			m[i][j] = sum
		}
		m[i][3] = p[i]
	}
	return m
}

func clip1(v float64) float64 {
	if v < -1 {
		return -1
	}
	if v > 1 {
		return 1
	}
	return v
}

// EulerXYZ is euler_xyz(matrix, previous): FBX/Blender XYZ Euler angles in
// radians. With previous, the equivalent branch nearest to it is chosen.
func EulerXYZ(m Mat3, previous *[3]float64) [3]float64 {
	y := asin(clip1(-m[2][0]))
	var x, z float64
	if math.Abs(math.Cos(y)) > 1e-8 {
		x, z = atan2(m[2][1], m[2][2]), atan2(m[1][0], m[0][0])
	} else {
		x, z = atan2(-m[1][2], m[1][1]), 0
	}
	a := [3]float64{x, y, z}
	if previous == nil {
		return a
	}
	candidates := [2][3]float64{a, {x + math.Pi, math.Pi - y, z + math.Pi}}
	best, bestNorm := 0, 0.0
	for c := range candidates {
		v := &candidates[c]
		for i := 0; i < 3; i++ {
			v[i] = v[i] + float64(twoPi*math.RoundToEven((previous[i]-v[i])/twoPi))
		}
		d0, d1, d2 := v[0]-previous[0], v[1]-previous[1], v[2]-previous[2]
		norm := math.Sqrt(math.FMA(d2, d2, math.FMA(d1, d1, float64(d0*d0))))
		if c == 0 || norm < bestNorm {
			best, bestNorm = c, norm
		}
	}
	return candidates[best]
}

var twoPi = 2 * math.Pi

// Python error messages.
const (
	errSingularAttachment = "Cannot export a singular attachment transform."
	errShear              = "Cannot export an attachment containing shear as FBX TRS."
)

// det3 is the determinant's sign-carrying value of a 3x3 matrix.
func det3(m *Mat3) float64 {
	return float64(m[0][0]*(float64(m[1][1]*m[2][2])-float64(m[1][2]*m[2][1]))) -
		float64(m[0][1]*(float64(m[1][0]*m[2][2])-float64(m[1][2]*m[2][0]))) +
		float64(m[0][2]*(float64(m[1][0]*m[2][1])-float64(m[1][1]*m[2][0])))
}

// Decompose is decompose(matrix): translation, XYZ Euler radians and scale
// (a negative determinant flips the X scale). Singular matrices and shear
// fail with Python's messages.
func Decompose(m *Mat4) (p, r, s [3]float64, err error) {
	upper := m.Upper3()
	for j := 0; j < 3; j++ {
		s[j] = math.Sqrt(float64(upper[0][j]*upper[0][j]) + float64(upper[1][j]*upper[1][j]) + float64(upper[2][j]*upper[2][j]))
	}
	if det3(&upper) < 0 {
		s[0] *= -1
	}
	for _, v := range s {
		if math.Abs(v) < 1e-12 {
			return p, r, s, errors.New(errSingularAttachment)
		}
	}
	var rot Mat3
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			rot[i][j] = upper[i][j] / s[j]
		}
	}
	// np.allclose(r.T @ r, np.eye(3), atol=1e-5): |a - b| <= atol + rtol*|b|
	// with the default rtol of 1e-5. NaN is never close.
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			v := float64(rot[0][i]*rot[0][j]) + float64(rot[1][i]*rot[1][j]) + float64(rot[2][i]*rot[2][j])
			want := 0.0
			if i == j {
				want = 1
			}
			if !(math.Abs(v-want) <= 1e-5+1e-5*math.Abs(want)) {
				return p, r, s, errors.New(errShear)
			}
		}
	}
	return [3]float64{m[0][3], m[1][3], m[2][3]}, EulerXYZ(rot, nil), s, nil
}

type track struct {
	frames []float64 // sorted unique source frames
	values [][4]float64
}

// Evaluator is PoseEvaluator: it samples a clip on a skeleton with the
// preview's parent-local child pose and rest-basis root rules.
type Evaluator struct {
	Bones   []formats.Bone
	Parents []int
	// Order lists bones so that every parent precedes its children (roots in
	// index order, then breadth first).
	Order []int

	restP  [][3]float64
	restQ  [][4]float64
	tracks [][3]*track // positions, rotations, scales per bone
}

// Pose is evaluate()'s result: local positions, quaternions and scales and
// world matrices, indexed by bone.
type Pose struct {
	Positions   [][3]float64
	Quaternions [][4]float64
	Scales      [][3]float64
	World       []Mat4
}

var trackComponents = [3]int{3, 4, 3}

// NewEvaluator is PoseEvaluator(bones, clip_bones). Pass nil clipBones for
// the rest pose.
func NewEvaluator(bones []formats.Bone, clipBones []formats.ClipBone) (*Evaluator, error) {
	n := len(bones)
	e := &Evaluator{Bones: bones, Parents: make([]int, n), Order: make([]int, 0, n)}
	children := make([][]int, n)
	for i := range bones {
		p := bones[i].Parent
		e.Parents[i] = p
		if p < -1 || p >= n || p == i {
			return nil, errors.New("Invalid skeleton parent.")
		}
		if p == -1 {
			e.Order = append(e.Order, i)
		} else {
			children[p] = append(children[p], i)
		}
	}
	for k := 0; k < len(e.Order); k++ {
		e.Order = append(e.Order, children[e.Order[k]]...)
	}
	if len(e.Order) != n {
		return nil, errors.New("Skeleton contains a parent cycle.")
	}
	byName := make(map[string]*formats.ClipBone, len(clipBones))
	for i := range clipBones {
		byName[clipBones[i].Name] = &clipBones[i]
	}
	e.restP = make([][3]float64, n)
	e.restQ = make([][4]float64, n)
	e.tracks = make([][3]*track, n)
	for i := range bones {
		e.restP[i] = bones[i].Pos
		e.restQ[i] = Normalize(bones[i].Rot)
	}
	for i := range bones {
		clip := byName[bones[i].Name]
		if clip == nil {
			continue
		}
		for kind, source := range [3]*formats.Track{clip.Positions, clip.Rotations, clip.Scales} {
			if source == nil || len(source.Frames) == 0 {
				continue
			}
			parsed, err := parseTrack(source, trackComponents[kind], e.restQ[i])
			if err != nil {
				return nil, err
			}
			e.tracks[i][kind] = parsed
		}
	}
	return e, nil
}

func parseTrack(source *formats.Track, n int, restQ [4]float64) (*track, error) {
	if len(source.Values)%n != 0 {
		return nil, errors.New("cannot reshape array of size " + itoa(len(source.Values)) + " into shape (" + itoa(n) + ")")
	}
	count := len(source.Values) / n
	if count != len(source.Frames) {
		return nil, errors.New("Invalid animation track.")
	}
	for _, v := range source.Values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, errors.New("Invalid animation track.")
		}
	}
	// keys[frame] = value: a repeated frame keeps its last value but its
	// first position; the result is sorted by frame anyway.
	last := make(map[uint32]int, count)
	for k, frame := range source.Frames {
		last[frame] = k
	}
	frames := make([]uint32, 0, len(last))
	for frame := range last {
		frames = append(frames, frame)
	}
	sort.Slice(frames, func(a, b int) bool { return frames[a] < frames[b] })
	t := &track{frames: make([]float64, len(frames)), values: make([][4]float64, len(frames))}
	for k, frame := range frames {
		index := last[frame]
		var value [4]float64
		copy(value[:n], source.Values[index*n:index*n+n])
		if n == 4 {
			value = Normalize(value)
			dot := float64(restQ[0]*value[0]) + float64(restQ[1]*value[1]) + float64(restQ[2]*value[2]) + float64(restQ[3]*value[3])
			if dot < 0 {
				value = [4]float64{-value[0], -value[1], -value[2], -value[3]}
			}
		}
		t.frames[k] = float64(frame)
		t.values[k] = value
	}
	return t, nil
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	negative := v < 0
	if negative {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sample is _sample: (value, found). Rotations are normalized (nlerp).
func (e *Evaluator) sample(i, kind int, frame float64, def [4]float64) ([4]float64, bool) {
	t := e.tracks[i][kind]
	if t == nil {
		return def, false
	}
	// bisect_right(frames, frame)
	idx := sort.Search(len(t.frames), func(k int) bool { return t.frames[k] > frame })
	var v [4]float64
	switch {
	case idx == 0:
		v = t.values[0]
	case idx == len(t.frames):
		v = t.values[len(t.values)-1]
	default:
		f := (frame - t.frames[idx-1]) / (t.frames[idx] - t.frames[idx-1])
		a, b := &t.values[idx-1], &t.values[idx]
		for c := 0; c < trackComponents[kind]; c++ {
			v[c] = float64(a[c]*(1-f)) + float64(b[c]*f)
		}
	}
	if kind == 1 {
		v = Normalize(v)
	}
	return v, true
}

// NewPose allocates a pose for this skeleton, for EvaluateInto.
func (e *Evaluator) NewPose() *Pose {
	n := len(e.Bones)
	return &Pose{Positions: make([][3]float64, n), Quaternions: make([][4]float64, n),
		Scales: make([][3]float64, n), World: make([]Mat4, n)}
}

// Evaluate is evaluate(frame, lock_root). frame may be fractional.
func (e *Evaluator) Evaluate(frame float64, lockRoot bool) *Pose {
	pose := e.NewPose()
	e.EvaluateInto(pose, frame, lockRoot)
	return pose
}

// EvaluateInto is Evaluate writing into a pose from NewPose, reusing its buffers.
func (e *Evaluator) EvaluateInto(pose *Pose, frame float64, lockRoot bool) {
	worldQ := make([][4]float64, len(e.Bones))
	for _, i := range e.Order {
		rp := e.restP[i]
		pv, hasP := e.sample(i, 0, frame, [4]float64{rp[0], rp[1], rp[2]})
		q, _ := e.sample(i, 1, frame, e.restQ[i])
		sv, _ := e.sample(i, 2, frame, [4]float64{1, 1, 1})
		p := [3]float64{pv[0], pv[1], pv[2]}
		s := [3]float64{sv[0], sv[1], sv[2]}
		for c := range s {
			if math.Abs(s[c]) < 1e-6 {
				if s[c] < 0 {
					s[c] = -1e-6
				} else {
					s[c] = 1e-6
				}
			}
		}
		parent := e.Parents[i]
		var wp [3]float64
		var wq [4]float64
		if parent < 0 {
			if hasP {
				r := Rotation(e.restQ[i])
				rotated := r.mulVec(p)
				p = [3]float64{rotated[0] + rp[0], rotated[1] + rp[1], rotated[2] + rp[2]}
			}
			if lockRoot {
				p[0], p[2] = rp[0], rp[2]
			}
			wp, wq = p, q
		} else {
			wp = pose.World[parent].apply3(p)
			wq = Multiply(worldQ[parent], q)
		}
		pose.Positions[i], pose.Quaternions[i], pose.Scales[i] = p, q, s
		worldQ[i] = wq
		pose.World[i] = Compose(wp, wq, s)
	}
}
