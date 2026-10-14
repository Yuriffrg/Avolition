package formats

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"avolition/gocore/internal/pypath"
)

// CheckPath is _path(path, extension): the expanded, resolved path when it
// has the (lower-case) extension and is an existing file.
func CheckPath(path, extension string) (string, error) {
	path = pypath.Resolve(path)
	if strings.ToLower(pypath.Suffix(path)) != extension {
		return "", formatError("Select an " + extension + " file: " + pypath.Name(path))
	}
	if !pypath.IsFile(path) {
		return "", notFound(path)
	}
	return path, nil
}

var trackNames = [3]string{"positions", "rotations", "scales"}
var trackComponents = [3]uint32{3, 4, 3}

func readClipHeader(r *Reader, size int64) (*ClipHeader, error) {
	raw, err := r.Take(16) // 'HHIfI'
	if err != nil {
		return nil, err
	}
	major := binary.LittleEndian.Uint16(raw)
	minor := binary.LittleEndian.Uint16(raw[2:])
	boneCount := binary.LittleEndian.Uint32(raw[4:])
	fps := float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[8:])))
	duration := binary.LittleEndian.Uint32(raw[12:])
	if err := CheckVersion(r, major, minor, "animation clip"); err != nil {
		return nil, err
	}
	if err := r.Count(int64(boneCount), 65536, "animation bone count"); err != nil {
		return nil, err
	}
	if math.IsNaN(fps) || math.IsInf(fps, 0) || !(0 < fps && fps <= 1000) {
		return nil, r.Fail("invalid frame rate " + PythonFloat(fps))
	}
	if err := r.Count(int64(duration), 100_000_000, "animation duration"); err != nil {
		return nil, err
	}
	if duration == 0 {
		return nil, r.Fail("animation frame count must include frame zero (minimum 1)")
	}
	// The writer stores InfoTableSize = floor(FPS * Duration) + 1, a frame
	// count including frame zero. The final sample is therefore count - 1.
	lastFrame := max(0, int64(duration)-1)
	return &ClipHeader{
		Name: pypath.Stem(r.Path), Path: r.Path, FPS: fps, DurationFrames: int64(duration),
		LastFrame: lastFrame, DurationSeconds: float64(lastFrame) / fps, BoneCount: int(boneCount),
		Size: size, Version: strconv.Itoa(int(major)) + "." + strconv.Itoa(int(minor)),
	}, nil
}

// ReadClipHeader reads 16 bytes only, so indexing many clips does not parse
// keyframes (read_clip_header).
func ReadClipHeader(path string) (*ClipHeader, error) {
	path, err := CheckPath(path, ".owanimclip")
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, PythonOSError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, PythonOSError(err)
	}
	var buffer [16]byte
	n, err := io.ReadFull(file, buffer[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, PythonOSError(err)
	}
	return readClipHeader(NewReader(path, buffer[:n]), info.Size())
}

// ReadClipBindingMetadata reads exact bone-record names without decoding or
// allocating keyframes (read_clip_binding_metadata).
//
// Matching follows LoadClip: every named bone record counts, including
// records with no populated channels. Structural corruption is rejected;
// frame numbers and numeric key values are deliberately not decoded here.
// Playback's full parser still validates those values when a clip is loaded.
func ReadClipBindingMetadata(path string) (*ClipBindingMetadata, error) {
	path, err := CheckPath(path, ".owanimclip")
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, PythonOSError(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, PythonOSError(err)
	}
	size := info.Size()
	if size > MaxFileBytes {
		return nil, formatError(pypath.Name(path) + ": file exceeds the 512 MB preview limit")
	}
	r := NewStreamReader(path, file, size, StreamOptions{
		ShortReadMessage:   "file changed or was truncated while reading animation metadata",
		AdvanceOnShortRead: true,
	})
	header, err := readClipHeader(r, size)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, header.BoneCount)
	seen := make(map[string]struct{}, header.BoneCount)
	warnings := []string{}
	for i := 0; i < header.BoneCount; i++ {
		name, err := r.String()
		if err != nil {
			return nil, err
		}
		trackCount, err := r.U32()
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[name]; name == "" || duplicate {
			return nil, r.Fail("missing or duplicate animation bone: " + PythonRepr(name))
		}
		seen[name] = struct{}{}
		names = append(names, name)
		if err := r.Count(int64(trackCount), 16, "track count"); err != nil {
			return nil, err
		}
		var known [3]bool
		for t := uint32(0); t < trackCount; t++ {
			kind, keyCount, components, err := readTrackHeader(r)
			if err != nil {
				return nil, err
			}
			if kind < 3 {
				if components != trackComponents[kind] {
					return nil, r.Failf("%s track needs %d components, has %d", trackNames[kind], trackComponents[kind], components)
				}
				if known[kind] {
					return nil, r.Fail("duplicate " + trackNames[kind] + " track for " + name)
				}
				known[kind] = true
			} else {
				warnings = append(warnings, name+": skipped unknown track type "+strconv.FormatUint(uint64(kind), 10)+".")
			}
			bytes := int(keyCount) * (4 + int(components)*4)
			if bytes > r.Remaining() {
				return nil, r.Fail("truncated keyframe array: requires " + Commas(bytes) + " bytes")
			}
			if err := r.Skip(bytes); err != nil {
				return nil, err
			}
		}
	}
	if info, err := file.Stat(); err != nil {
		return nil, PythonOSError(err)
	} else if info.Size() != size {
		return nil, r.Fail("file changed while reading animation metadata; scan again")
	}
	if r.Remaining() > 0 {
		warnings = append(warnings, "Ignored "+Commas(r.Remaining())+" trailing bytes from clip version "+header.Version+".")
	}
	return &ClipBindingMetadata{Path: path, BoneNames: names, BoneCount: header.BoneCount, Warnings: warnings}, nil
}

func readTrackHeader(r *Reader) (kind, keyCount, components uint32, err error) {
	raw, err := r.Take(12) // 'III'
	if err != nil {
		return 0, 0, 0, err
	}
	kind = binary.LittleEndian.Uint32(raw)
	keyCount = binary.LittleEndian.Uint32(raw[4:])
	components = binary.LittleEndian.Uint32(raw[8:])
	if err := r.Count(int64(keyCount), 10_000_000, "keyframe count"); err != nil {
		return 0, 0, 0, err
	}
	if err := r.Count(int64(components), 16, "keyframe components"); err != nil {
		return 0, 0, 0, err
	}
	return kind, keyCount, components, nil
}

// LoadClip returns sorted raw per-bone tracks for the Blender-compatible
// evaluator (load_clip).
func LoadClip(path string) (*Clip, error) {
	path, err := CheckPath(path, ".owanimclip")
	if err != nil {
		return nil, err
	}
	r, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	header, err := readClipHeader(r, int64(r.Size()))
	if err != nil {
		return nil, err
	}
	clip := &Clip{ClipHeader: *header, Bones: make([]ClipBone, 0, header.BoneCount), Warnings: []string{}}
	names := make(map[string]struct{}, header.BoneCount)
	maxFrame := uint32(0)
	var order []int
	for i := 0; i < header.BoneCount; i++ {
		name, err := r.String()
		if err != nil {
			return nil, err
		}
		trackCount, err := r.U32()
		if err != nil {
			return nil, err
		}
		if _, duplicate := names[name]; name == "" || duplicate {
			return nil, r.Fail("missing or duplicate animation bone: " + PythonRepr(name))
		}
		names[name] = struct{}{}
		if err := r.Count(int64(trackCount), 16, "track count"); err != nil {
			return nil, err
		}
		bone := ClipBone{Name: name}
		for t := uint32(0); t < trackCount; t++ {
			kind, keyCount, components, err := readTrackHeader(r)
			if err != nil {
				return nil, err
			}
			if kind < 3 && components != trackComponents[kind] {
				return nil, r.Failf("%s track needs %d components, has %d", trackNames[kind], trackComponents[kind], components)
			}
			size := int(keyCount) * (4 + int(components)*4)
			if size > r.Remaining() {
				return nil, r.Fail("truncated keyframe array: requires " + Commas(size) + " bytes")
			}
			if kind >= 3 {
				if _, err := r.Take(size); err != nil {
					return nil, err
				}
				clip.Warnings = append(clip.Warnings, name+": skipped unknown track type "+strconv.FormatUint(uint64(kind), 10)+".")
				continue
			}
			slot := [3]**Track{&bone.Positions, &bone.Rotations, &bone.Scales}[kind]
			if *slot != nil {
				return nil, r.Fail("duplicate " + trackNames[kind] + " track for " + name)
			}
			track, last, err := readTrack(r, int(keyCount), int(components), kind == 1, &order)
			if err != nil {
				return nil, err
			}
			maxFrame = max(maxFrame, last)
			*slot = track
		}
		clip.Bones = append(clip.Bones, bone)
	}
	if int64(maxFrame) > clip.LastFrame {
		clip.Warnings = append(clip.Warnings, "Keyframes extend past header duration; timeline expanded to frame "+
			strconv.FormatUint(uint64(maxFrame), 10)+".")
		clip.DurationFrames = int64(maxFrame) + 1
		clip.LastFrame = int64(maxFrame)
		clip.DurationSeconds = float64(maxFrame) / clip.FPS
	}
	if r.Remaining() > 0 {
		clip.Warnings = append(clip.Warnings, "Ignored "+Commas(r.Remaining())+" trailing bytes from clip version "+clip.Version+".")
	}
	return clip, nil
}

// readTrack decodes keyCount (frame, values) records. A repeated frame keeps
// the last value (Python dict assignment); frames are returned sorted.
func readTrack(r *Reader, keyCount, components int, rotation bool, order *[]int) (*Track, uint32, error) {
	frames := make([]uint32, keyCount)
	values := make([]float64, keyCount*components)
	maxFrame := uint32(0)
	sorted := true
	for i := 0; i < keyCount; i++ {
		frame, err := r.U32()
		if err != nil {
			return nil, 0, err
		}
		if err := r.Count(int64(frame), 100_000_000, "keyframe frame number"); err != nil {
			return nil, 0, err
		}
		raw, err := r.Take(components * 4)
		if err != nil {
			return nil, 0, err
		}
		value := values[i*components : (i+1)*components]
		finite := true
		for c := range value {
			bits := binary.LittleEndian.Uint32(raw[c*4:])
			finite = finite && finite32(bits)
			value[c] = float64(math.Float32frombits(bits))
		}
		if !finite {
			return nil, 0, r.Fail("non-finite numeric value")
		}
		if rotation {
			q := NormalizeQuaternion([4]float64{value[0], value[1], value[2], value[3]})
			copy(value, q[:])
		}
		frames[i] = frame
		if i > 0 && frame <= frames[i-1] {
			sorted = false
		}
		maxFrame = max(maxFrame, frame)
	}
	if sorted {
		return &Track{Frames: frames, Values: values}, maxFrame, nil
	}
	// Last write wins for duplicate frames, then sort by frame.
	last := make(map[uint32]int, keyCount)
	for i, frame := range frames {
		last[frame] = i
	}
	idx := (*order)[:0]
	for i, frame := range frames {
		if last[frame] == i {
			idx = append(idx, i)
		}
	}
	slices.SortFunc(idx, func(a, b int) int { return int(frames[a]) - int(frames[b]) })
	*order = idx
	outFrames := make([]uint32, len(idx))
	outValues := make([]float64, len(idx)*components)
	for j, i := range idx {
		outFrames[j] = frames[i]
		copy(outValues[j*components:], values[i*components:(i+1)*components])
	}
	return &Track{Frames: outFrames, Values: outValues}, maxFrame, nil
}
