package formats

import "math"

// Mat4 is a column-major 4x4 matrix (Three.js Matrix4.fromArray order).
type Mat4 [16]float64

// Identity is Python's IDENTITY.
var Identity = Mat4{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}

// The helpers below reproduce Python's evaluation order: sum() starts from
// integer 0 (so -0.0 terms become +0.0) and adds left to right. Products are
// wrapped in float64() so no platform fuses them into FMA instructions.

// NormalizeQuaternion is _normalize_quaternion: unit length, or the identity
// quaternion (0, 0, 0, 1) when the length is below 1e-12.
func NormalizeQuaternion(values [4]float64) [4]float64 {
	sum := 0.0
	for _, v := range values {
		sum += float64(v * v)
	}
	length := math.Sqrt(sum)
	if length < 1e-12 {
		return [4]float64{0, 0, 0, 1}
	}
	return [4]float64{values[0] / length, values[1] / length, values[2] / length, values[3] / length}
}

// EulerXYZ converts Blender XYZ Euler radians (Rz @ Ry @ Rx) to an XYZW
// quaternion (_euler_xyz).
func EulerXYZ(values [3]float64) [4]float64 {
	x, y, z := values[0]*0.5, values[1]*0.5, values[2]*0.5
	cx, sx, cy, sy, cz, sz := math.Cos(x), math.Sin(x), math.Cos(y), math.Sin(y), math.Cos(z), math.Sin(z)
	return NormalizeQuaternion([4]float64{
		float64(float64(sx*cy)*cz) - float64(float64(cx*sy)*sz),
		float64(float64(cx*sy)*cz) + float64(float64(sx*cy)*sz),
		float64(float64(cx*cy)*sz) - float64(float64(sx*sy)*cz),
		float64(float64(cx*cy)*cz) + float64(float64(sx*sy)*sz),
	})
}

// Compose is _matrix(position, rotation): a rigid transform from a
// translation and an XYZW quaternion.
func Compose(position [3]float64, rotation [4]float64) Mat4 {
	x, y, z, w := rotation[0], rotation[1], rotation[2], rotation[3]
	return Mat4{
		1 - 2*(float64(y*y)+float64(z*z)), 2 * (float64(x*y) + float64(z*w)), 2 * (float64(x*z) - float64(y*w)), 0,
		2 * (float64(x*y) - float64(z*w)), 1 - 2*(float64(x*x)+float64(z*z)), 2 * (float64(y*z) + float64(x*w)), 0,
		2 * (float64(x*z) + float64(y*w)), 2 * (float64(y*z) - float64(x*w)), 1 - 2*(float64(x*x)+float64(y*y)), 0,
		position[0], position[1], position[2], 1,
	}
}

// Multiply is _multiply(a, b) = a @ b for column-major matrices.
func Multiply(a, b *Mat4) Mat4 {
	var out Mat4
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			sum := 0.0
			for k := 0; k < 4; k++ {
				sum += float64(a[k*4+row] * b[col*4+k])
			}
			out[col*4+row] = sum
		}
	}
	return out
}

// InverseRigid is _inverse_rigid: the inverse of a rotation+translation.
func InverseRigid(m *Mat4) Mat4 {
	inverse := Mat4{
		m[0], m[4], m[8], 0,
		m[1], m[5], m[9], 0,
		m[2], m[6], m[10], 0,
		0, 0, 0, 1,
	}
	for row := 0; row < 3; row++ {
		sum := 0.0
		for k := 0; k < 3; k++ {
			sum += float64(inverse[k*4+row] * m[12+k])
		}
		inverse[12+row] = -sum
	}
	return inverse
}

// BoneGlobals is _bone_globals: model-space rest matrices for bones in any
// parent order, without recursion. A cycle fails with r's position.
func BoneGlobals(bones []Bone, r *Reader) ([]Mat4, error) {
	result := make([]Mat4, len(bones))
	done := make([]bool, len(bones))
	visiting := make([]bool, len(bones))
	var trail []int
	for start := range bones {
		trail = trail[:0]
		index := start
		for index != -1 && !done[index] {
			if visiting[index] {
				return nil, r.Fail("cycle in bone hierarchy")
			}
			visiting[index] = true
			trail = append(trail, index)
			index = bones[index].Parent
		}
		for _, i := range trail {
			visiting[i] = false
		}
		parent := Identity
		if index != -1 {
			parent = result[index]
		}
		for i := len(trail) - 1; i >= 0; i-- {
			bone := &bones[trail[i]]
			local := Compose(bone.Pos, bone.Rot)
			parent = Multiply(&parent, &local)
			result[trail[i]] = parent
			done[trail[i]] = true
		}
	}
	return result, nil
}

// Skin is _skin: per vertex, merge duplicate joints among the valid weighted
// influences, keep the four heaviest (stable on ties) and normalize them.
// A vertex without any valid weight is bound fully to joint 0 and counted as
// an orphan (a zero-weight Three.js vertex would collapse to the origin).
func Skin(indices []uint16, weights []float32, boneCount, vertexCount, slots int) ([]uint16, []float64, int) {
	outIndices := make([]uint16, vertexCount*4)
	outWeights := make([]float64, vertexCount*4)
	orphans := 0
	var joints [32]uint16
	var sums [32]float64
	for vertex := 0; vertex < vertexCount; vertex++ {
		n := 0
		base := vertex * slots
		for entry := base; entry < base+slots; entry++ {
			index, weight := indices[entry], weights[entry]
			if int(index) >= boneCount || !(weight > 0) {
				continue
			}
			found := false
			for j := 0; j < n; j++ {
				if joints[j] == index {
					sums[j] += float64(weight)
					found = true
					break
				}
			}
			if !found {
				joints[n], sums[n] = index, 0.0+float64(weight)
				n++
			}
		}
		// Stable descending insertion sort (Python's sorted(reverse=True)).
		for i := 1; i < n; i++ {
			j, s := joints[i], sums[i]
			k := i
			for k > 0 && sums[k-1] < s {
				joints[k], sums[k] = joints[k-1], sums[k-1]
				k--
			}
			joints[k], sums[k] = j, s
		}
		if n > 4 {
			n = 4
		}
		total := 0.0
		for i := 0; i < n; i++ {
			total += sums[i]
		}
		out := vertex * 4
		if total == 0 {
			outIndices[out], outWeights[out] = 0, 1.0
			orphans++
			continue
		}
		for i := 0; i < n; i++ {
			outIndices[out+i] = joints[i]
			outWeights[out+i] = sums[i] / total
		}
	}
	return outIndices, outWeights, orphans
}
