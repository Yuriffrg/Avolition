package exportpose_test

// Ports of the PoseEvaluator/euler tests in tests/test_fbx.py, plus oracle
// comparisons with owpreview/export_pose.py.

import (
	"encoding/json"
	"math"
	"math/rand"
	"testing"

	"avolition/gocore/internal/exportpose"
	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
)

func assertMatrix(t *testing.T, label string, got exportpose.Mat4, want []float64, atol float64) {
	t.Helper()
	var values formats.Mat4
	copy(values[:], want)
	exporttest.AssertClose(t, label, got, exportpose.FromColumnMajor(values), atol)
}

func TestPoseMatchesActualBlenderOracle(t *testing.T) {
	fixture := exporttest.LoadPlayback(t)
	ev, err := exportpose.NewEvaluator(fixture.Bones, fixture.Tracks)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range fixture.Samples {
		pose := ev.Evaluate(sample.Frame, false)
		for i, bone := range fixture.Bones {
			assertMatrix(t, bone.Name, pose.World[i], sample.Matrices[bone.Name], fixture.Tolerance)
		}
	}
}

func TestRestResetAndRootLock(t *testing.T) {
	fixture := exporttest.LoadPlayback(t)
	ev, err := exportpose.NewEvaluator(fixture.Bones, fixture.Tracks)
	if err != nil {
		t.Fatal(err)
	}
	unlocked := ev.Evaluate(10, false).World[0]
	locked := ev.Evaluate(10, true).World[0]
	want := [3]float64{fixture.Bones[0].Pos[0], unlocked[1][3], fixture.Bones[0].Pos[2]}
	for i := 0; i < 3; i++ {
		if math.Abs(locked[i][3]-want[i]) > 1e-7*math.Abs(want[i]) {
			t.Fatalf("locked root %v, want %v", locked, want)
		}
	}
	rest, err := exportpose.NewEvaluator(fixture.Bones, nil)
	if err != nil {
		t.Fatal(err)
	}
	pose := rest.Evaluate(0, false)
	for i, bone := range fixture.Bones {
		assertMatrix(t, bone.Name, pose.World[i], fixture.RestMatrices[bone.Name], fixture.Tolerance)
	}
}

func diag(v [3]float64) exportpose.Mat4 {
	return exportpose.Mat4{{v[0], 0, 0, 0}, {0, v[1], 0, 0}, {0, 0, v[2], 0}, {0, 0, 0, 1}}
}

func TestCompensationIsExact(t *testing.T) {
	fixture := exporttest.LoadPlayback(t)
	ev, err := exportpose.NewEvaluator(fixture.Bones, fixture.Tracks)
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []float64{0, 5, 10, 15, 20} {
		p := ev.Evaluate(frame, false)
		assembled := make([]exportpose.Mat4, len(fixture.Bones))
		for _, i := range ev.Order {
			parent := ev.Parents[i]
			matrix := exportpose.Compose(p.Positions[i], p.Quaternions[i], p.Scales[i])
			if parent >= 0 {
				s := p.Scales[parent]
				inverse := diag([3]float64{1 / s[0], 1 / s[1], 1 / s[2]})
				scaled := [3]float64{p.Positions[i][0] * s[0], p.Positions[i][1] * s[1], p.Positions[i][2] * s[2]}
				local := exportpose.Compose(scaled, p.Quaternions[i], p.Scales[i])
				step := assembled[parent].Mul(&inverse)
				matrix = step.Mul(&local)
			}
			assembled[i] = matrix
			exporttest.AssertClose(t, "compensation", matrix, p.World[i], 1e-10)
		}
	}
}

func rotation4(m exportpose.Mat3) exportpose.Mat4 {
	return exportpose.Mat4{{m[0][0], m[0][1], m[0][2], 0}, {m[1][0], m[1][1], m[1][2], 0}, {m[2][0], m[2][1], m[2][2], 0}, {0, 0, 0, 1}}
}

func TestEulerRoundtripAndContinuity(t *testing.T) {
	var previous *[3]float64
	for degrees := 0; degrees <= 720; degrees += 5 {
		q := formats.EulerXYZ([3]float64{0.3, 0.8, float64(degrees) * math.Pi / 180})
		e := exportpose.EulerXYZ(exportpose.Rotation(q), previous)
		exporttest.AssertClose(t, "roundtrip", rotation4(exportpose.Rotation(formats.EulerXYZ(e))), rotation4(exportpose.Rotation(q)), 1e-10)
		if previous != nil {
			for i := range e {
				if math.Abs(e[i]-previous[i]) >= 0.2 {
					t.Fatalf("discontinuous Euler at %d degrees: %v after %v", degrees, e, *previous)
				}
			}
		}
		previous = &e
	}
}

func TestEvaluatorRejectsInvalidSkeletonsAndTracks(t *testing.T) {
	bone := func(parent int) formats.Bone {
		return formats.Bone{Name: "b", Parent: parent, Rot: [4]float64{0, 0, 0, 1}}
	}
	for _, c := range []struct {
		bones []formats.Bone
		want  string
	}{
		{[]formats.Bone{bone(-2)}, "Invalid skeleton parent."},
		{[]formats.Bone{bone(1)}, "Invalid skeleton parent."},
		{[]formats.Bone{bone(0)}, "Invalid skeleton parent."},
		{[]formats.Bone{bone(1), bone(0)}, "Skeleton contains a parent cycle."},
	} {
		if _, err := exportpose.NewEvaluator(c.bones, nil); err == nil || err.Error() != c.want {
			t.Errorf("%v: got %v, want %q", c.bones, err, c.want)
		}
	}
	bones := []formats.Bone{bone(-1)}
	for _, c := range []struct {
		track formats.Track
		want  string
	}{
		{formats.Track{Frames: []uint32{0, 1}, Values: []float64{1, 2, 3}}, "Invalid animation track."},
		{formats.Track{Frames: []uint32{0}, Values: []float64{math.NaN(), 2, 3}}, "Invalid animation track."},
		{formats.Track{Frames: []uint32{0}, Values: []float64{1, 2, 3, 4, 5}}, "cannot reshape array of size 5 into shape (3)"},
	} {
		track := c.track
		clip := []formats.ClipBone{{Name: "b", Positions: &track}}
		if _, err := exportpose.NewEvaluator(bones, clip); err == nil || err.Error() != c.want {
			t.Errorf("%v: got %v, want %q", c.track, err, c.want)
		}
	}
}

func TestDecomposeErrors(t *testing.T) {
	singular := exportpose.Identity4
	singular[1][1] = 0
	if _, _, _, err := exportpose.Decompose(&singular); err == nil || err.Error() != "Cannot export a singular attachment transform." {
		t.Fatalf("singular: %v", err)
	}
	shear := exportpose.Identity4
	shear[0][1] = .4
	if _, _, _, err := exportpose.Decompose(&shear); err == nil || err.Error() != "Cannot export an attachment containing shear as FBX TRS." {
		t.Fatalf("shear: %v", err)
	}
	mirrored := diag([3]float64{1, -2, 3})
	_, _, s, err := exportpose.Decompose(&mirrored)
	if err != nil || s != [3]float64{-1, 2, 3} {
		t.Fatalf("mirrored: %v %v", s, err)
	}
}

func TestInverse(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for n := 0; n < 100; n++ {
		var m exportpose.Mat4
		for i := 0; i < 4; i++ {
			for j := 0; j < 4; j++ {
				m[i][j] = rng.Float64()*2 - 1
			}
		}
		inverse, err := m.Inverse()
		if err != nil {
			t.Fatal(err)
		}
		product := m.Mul(&inverse)
		exporttest.AssertClose(t, "m @ inv(m)", product, exportpose.Identity4, 1e-9)
	}
	var zero exportpose.Mat4
	if _, err := zero.Inverse(); err != exportpose.ErrSingular {
		t.Fatal(err)
	}
}

// --- Oracle comparisons -----------------------------------------------------

const evaluatePython = `
import numpy as np
from owpreview.export_pose import PoseEvaluator
bones, clip_bones, frames = args
ev = PoseEvaluator(bones, clip_bones)
out = []
for frame, lock in frames:
    pose = ev.evaluate(frame, lock_root=lock)
    out.append({'positions': pose['positions'], 'quaternions': pose['quaternions'],
                'scales': pose['scales'], 'world': [w.T.reshape(-1) for w in pose['world']]})
result = {'order': ev.order, 'poses': out}
`

func poseJSON(ev *exportpose.Evaluator, frames [][2]any) []byte {
	type out struct {
		Positions   [][3]float64   `json:"positions"`
		Quaternions [][4]float64   `json:"quaternions"`
		Scales      [][3]float64   `json:"scales"`
		World       []formats.Mat4 `json:"world"`
	}
	var poses []out
	for _, f := range frames {
		pose := ev.Evaluate(f[0].(float64), f[1].(bool))
		world := make([]formats.Mat4, len(pose.World))
		for i := range pose.World {
			world[i] = pose.World[i].ColumnMajor()
		}
		poses = append(poses, out{pose.Positions, pose.Quaternions, pose.Scales, world})
	}
	data, _ := json.Marshal(map[string]any{"order": ev.Order, "poses": poses})
	return data
}

func TestEvaluatorMatchesPython(t *testing.T) {
	fixture := exporttest.LoadPlayback(t)
	// Add an unsorted, duplicated and sign-flipped rotation track and tiny
	// scales to exercise every branch.
	tracks := append([]formats.ClipBone(nil), fixture.Tracks...)
	tracks[1].Rotations = &formats.Track{Frames: []uint32{12, 3, 12, 0}, Values: []float64{
		0, 0, 0, -2, 0.1, 0.2, 0.3, 0.9, 0, 0.7, 0, -0.7, 0.5, 0.5, 0.5, 0.5}}
	tracks[2].Scales = &formats.Track{Frames: []uint32{0, 20}, Values: []float64{1e-8, -1e-9, 1, -2, 0.5, 3}}
	frames := [][2]any{{0.0, false}, {0.5, false}, {3.0, true}, {7.25, false}, {12.0, true}, {19.999, false}, {25.0, false}, {-1.0, true}}
	for _, c := range []struct {
		label  string
		tracks []formats.ClipBone
	}{{"oracle", fixture.Tracks}, {"edge cases", tracks}, {"rest", nil}} {
		ev, err := exportpose.NewEvaluator(fixture.Bones, c.tracks)
		if err != nil {
			t.Fatal(err)
		}
		clip := c.tracks
		if clip == nil {
			clip = []formats.ClipBone{}
		}
		want := oracle.Exec(t, evaluatePython, fixture.Bones, clip, frames).MustValue(t)
		jsoncmp.Equal(t, want, json.RawMessage(poseJSON(ev, frames)), jsoncmp.Options{RelTol: 1e-12, AbsTol: 1e-12})
	}
}

const decomposePython = `
import numpy as np
from owpreview.export_pose import decompose, euler_xyz
out = []
for values, previous in args[0]:
    m = np.asarray(values, dtype=float).reshape(4, 4).T
    entry = {}
    try:
        p, r, s = decompose(m)
        entry['decompose'] = [p, r, s]
    except ValueError as error:
        entry['error'] = str(error)
    entry['euler'] = euler_xyz(m, None if previous is None else np.asarray(previous))
    out.append(entry)
result = out
`

func TestDecomposeAndEulerMatchPython(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var cases [][2]any
	var matrices []formats.Mat4
	var previous []*[3]float64
	add := func(m exportpose.Mat4, prev *[3]float64) {
		values := m.ColumnMajor()
		matrices = append(matrices, values)
		previous = append(previous, prev)
		cases = append(cases, [2]any{values, prev})
	}
	for n := 0; n < 200; n++ {
		q := [4]float64{rng.NormFloat64(), rng.NormFloat64(), rng.NormFloat64(), rng.NormFloat64()}
		s := [3]float64{rng.Float64()*3 + .01, rng.Float64()*3 + .01, rng.Float64()*3 + .01}
		if n%5 == 0 {
			s[n%3] = -s[n%3]
		}
		m := exportpose.Compose([3]float64{rng.NormFloat64(), rng.NormFloat64(), rng.NormFloat64()}, q, s)
		var prev *[3]float64
		if n%2 == 0 {
			prev = &[3]float64{rng.NormFloat64() * 10, rng.NormFloat64() * 10, rng.NormFloat64() * 10}
		}
		if n%17 == 0 {
			m[0][1] += .3 // shear
		}
		if n%23 == 0 {
			m[0][0], m[1][0], m[2][0] = 0, 0, 0 // singular
		}
		add(m, prev)
	}
	// Gimbal lock: Y = +-90 degrees.
	add(exportpose.Compose([3]float64{}, formats.EulerXYZ([3]float64{0.3, math.Pi / 2, 0.2}), [3]float64{1, 1, 1}), nil)
	add(exportpose.Compose([3]float64{}, formats.EulerXYZ([3]float64{0.3, -math.Pi / 2, 0.2}), [3]float64{1, 1, 1}), &[3]float64{7, 1, -9})
	want := oracle.Exec(t, decomposePython, cases).MustValue(t)
	var got []map[string]any
	for i, values := range matrices {
		m := exportpose.FromColumnMajor(values)
		entry := map[string]any{}
		p, r, s, err := exportpose.Decompose(&m)
		if err != nil {
			entry["error"] = err.Error()
		} else {
			entry["decompose"] = [][3]float64{p, r, s}
		}
		entry["euler"] = exportpose.EulerXYZ(m.Upper3(), previous[i])
		got = append(got, entry)
	}
	jsoncmp.Equal(t, want, got, jsoncmp.Options{RelTol: 1e-9, AbsTol: 1e-9})
}

func BenchmarkEvaluate(b *testing.B) {
	fixture := exporttest.LoadPlayback(b)
	ev, err := exportpose.NewEvaluator(fixture.Bones, fixture.Tracks)
	if err != nil {
		b.Fatal(err)
	}
	pose := ev.NewPose()
	for i := 0; i < b.N; i++ {
		ev.EvaluateInto(pose, float64(i%20)+.5, false)
	}
}
