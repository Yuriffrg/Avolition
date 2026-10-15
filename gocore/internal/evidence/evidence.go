// Package evidence reads exported identity/reference evidence without
// changing preview behavior. It is the Go port of
// owpreview/animation_evidence_formats.py.
//
// Schemas follow OWLib's OverwatchModel, SaveLogic.Entity and SaveLogic.Effect
// writers. These are exported indexes/references, not recovered animation
// names. Missing full GUIDs are never reconstructed from uint32 indexes or
// filenames. All full GUIDs returned to JavaScript are hexadecimal strings.
//
// # Python → Go map
//
//	PARSER_VERSION     ParserVersion (a variable so tests can change it)
//	MAX_EFFECT_RECORDS MaxEffectRecords
//	decode_guid        DecodeGUID (uint64), ParseGUID (string), DecodeGUIDValue (any)
//	read_model         ReadModel
//	read_entity        ReadEntity
//	read_effect        ReadEffect
//	read_companion     ReadCompanion
//
// json.Marshal of the results produces the Python dicts key for key. Floats
// read from the file are Python floats (pyjson.Float: the float64 widening of
// the stored float32, written with Python's repr) because these results are
// persisted in the analysis inventory, which must stay byte-compatible with
// the files Python writes. Errors are *formats.FormatError (or *formats.OSError)
// whose text is Python's str(exception).
package evidence

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pyjson"
	"avolition/gocore/internal/pypath"
)

// ParserVersion is part of the analysis cache token; changing it invalidates
// cached inventories.
var ParserVersion = "1"

// MaxEffectRecords bounds the records of one effect.
const MaxEffectRecords = 100_000

// GUID is decode_guid's result.
type GUID struct {
	FullGUID string `json:"fullGuid"`
	LongKey  string `json:"longKey"`
	Type     string `json:"type"`
	Index    string `json:"index"`
	Engine   string `json:"engine"`
	IsNull   bool   `json:"isNull"`
	Display  string `json:"display"`
}

// DecodeGUID decodes an exact ulong using teResourceGUID's masks and
// reversed type bits.
func DecodeGUID(value uint64) GUID {
	mangled := uint16(value>>48) & 0xFFF
	resourceType := uint64(bits.Reverse16(mangled)>>4) + 1
	result := GUID{
		FullGUID: fmt.Sprintf("%016X", value),
		LongKey:  fmt.Sprintf("%012X", value&0xFFFFFFFFFFFF),
		Type:     fmt.Sprintf("%03X", resourceType),
		Index:    fmt.Sprintf("%08X", value&0xFFFFFFFF),
		Engine:   fmt.Sprintf("%X", value>>60),
		IsNull:   value == 0,
	}
	result.Display = result.LongKey + "." + result.Type
	return result
}

var (
	errGUIDDigits = errors.New("A full GUID requires exactly 16 hexadecimal digits.")
	errGUIDValue  = errors.New("GUID must be an unsigned 64-bit integer or full hexadecimal string.")
)

// ParseGUID is decode_guid for string input: a full 16-digit hexadecimal
// GUID (optional "0x" prefix), never OWLib's shortened "LongKey.Type"
// display notation.
func ParseGUID(text string) (GUID, error) {
	clean := text
	if len(text) >= 2 && strings.EqualFold(text[:2], "0x") {
		clean = text[2:]
	}
	if len(clean) != 16 {
		return GUID{}, errGUIDDigits
	}
	value, err := strconv.ParseUint(clean, 16, 64)
	if err != nil {
		return GUID{}, errGUIDDigits
	}
	return DecodeGUID(value), nil
}

// DecodeGUIDValue is decode_guid for a dynamic (JSON-decoded) value: a
// string, or an integer in [0, 2**64). Booleans and floats are rejected like
// in Python. Integers may be any Go integer type or a json.Number without a
// fraction or exponent.
func DecodeGUIDValue(value any) (GUID, error) {
	switch v := value.(type) {
	case string:
		return ParseGUID(v)
	case uint64:
		return DecodeGUID(v), nil
	case uint:
		return DecodeGUID(uint64(v)), nil
	case uint32:
		return DecodeGUID(uint64(v)), nil
	case int:
		return decodeSigned(int64(v))
	case int64:
		return decodeSigned(v)
	case int32:
		return decodeSigned(int64(v))
	case interface{ String() string }: // json.Number
		text := v.String()
		if strings.ContainsAny(text, ".eE") {
			return GUID{}, errGUIDValue
		}
		if parsed, err := strconv.ParseUint(text, 10, 64); err == nil {
			return DecodeGUID(parsed), nil
		}
		return GUID{}, errGUIDValue
	}
	return GUID{}, errGUIDValue
}

func decodeSigned(value int64) (GUID, error) {
	if value < 0 {
		return GUID{}, errGUIDValue
	}
	return DecodeGUID(uint64(value)), nil
}

// Header holds the keys every reader starts with.
type Header struct {
	Path     string   `json:"path"`
	Version  string   `json:"version"`
	Warnings []string `json:"warnings"`
}

func index32(value uint64) string { return fmt.Sprintf("%08X", value) }

func start(path, magic string, versions [][2]uint16) (*formats.Reader, Header, error) {
	r, err := formats.ReadFile(pypath.Clean(path))
	if err != nil {
		return nil, Header{}, err
	}
	header, err := readHeader(r, magic, versions)
	return r, header, err
}

func readHeader(r *formats.Reader, magic string, versions [][2]uint16) (Header, error) {
	if magic != "" {
		identifier, err := r.String()
		if err != nil {
			return Header{}, err
		}
		if identifier != magic {
			return Header{}, r.Fail("invalid " + magic + " identifier")
		}
	}
	fields, err := r.Unpack("HH")
	if err != nil {
		return Header{}, err
	}
	major, minor := uint16(fields.Uint(0)), uint16(fields.Uint(1))
	supported := false
	for _, version := range versions {
		supported = supported || version == [2]uint16{major, minor}
	}
	version := strconv.Itoa(int(major)) + "." + strconv.Itoa(int(minor))
	if !supported {
		kind := magic
		if kind == "" {
			kind = "model"
		}
		return Header{}, r.Fail("unsupported " + kind + " evidence version " + version)
	}
	return Header{Path: r.Path, Version: version, Warnings: []string{}}, nil
}

func readFloat(r *formats.Reader, label string, minimum *float64) (pyjson.Float, error) {
	fields, err := r.Unpack("f")
	if err != nil {
		return 0, err
	}
	value := fields.Float(0)
	if math.IsNaN(value) || math.IsInf(value, 0) || (minimum != nil && value < *minimum) {
		return 0, r.Fail("invalid " + label)
	}
	return pyjson.Float(value), nil
}

var zero = 0.0

func complete(r *formats.Reader, warnings *[]string) int {
	remaining := r.Remaining()
	if remaining > 0 {
		*warnings = append(*warnings, strconv.Itoa(remaining)+" trailing bytes are not interpreted as evidence.")
	}
	return remaining
}

func sourceFields(pairs ...string) *ordered.Map[string] {
	m := ordered.New[string]()
	for i := 0; i < len(pairs); i += 2 {
		m.Set(pairs[i], pairs[i+1])
	}
	return m
}

// Bone is one skeleton record of a model.
type Bone struct {
	Name        string `json:"name"`
	ParentIndex int    `json:"parentIndex"`
}

// Model is read_model's result.
type Model struct {
	Header
	Name              string               `json:"name"`
	Index             string               `json:"index"`
	FullGUID          *string              `json:"fullGuid"`
	MaterialReference string               `json:"materialReference"`
	BoneCount         int                  `json:"boneCount"`
	MeshCount         uint32               `json:"meshCount"`
	SocketCount       uint32               `json:"socketCount"`
	Bones             []Bone               `json:"bones"`
	UnparsedBytes     int                  `json:"unparsedBytes"`
	SourceFields      *ordered.Map[string] `json:"sourceFields"`
}

var modelSourceFields = sourceFields(
	"index", "teResourceGUID.Index(OverwatchModel.GUID)",
	"bones.name", "skeleton.IDs; synthetic negative array index for cloth bones")

// ReadModel reads the header and skeleton only; geometry is outside this
// evidence parser.
func ReadModel(path string) (*Model, error) {
	r, header, err := start(path, "", [][2]uint16{{2, 0}})
	if err != nil {
		return nil, err
	}
	material, err := r.String()
	if err != nil {
		return nil, err
	}
	name, err := r.String()
	if err != nil {
		return nil, err
	}
	fields, err := r.Unpack("IHII")
	if err != nil {
		return nil, err
	}
	boneCount := int(fields.Uint(1))
	meshCount, socketCount := fields.Uint(2), fields.Uint(3)
	if err := r.Count(int64(meshCount), 16384, "mesh count"); err != nil {
		return nil, err
	}
	if err := r.Count(int64(socketCount), 65536, "socket count"); err != nil {
		return nil, err
	}
	bones := make([]Bone, 0, boneCount)
	names := make(map[string]struct{}, boneCount)
	for i := 0; i < boneCount; i++ {
		boneName, err := r.String()
		if err != nil {
			return nil, err
		}
		parentField, err := r.Unpack("h")
		if err != nil {
			return nil, err
		}
		parent := int(parentField.Int(0))
		if _, duplicate := names[boneName]; boneName == "" || duplicate {
			return nil, r.Fail("missing or duplicate skeleton bone identifier")
		}
		if parent < -1 || parent >= boneCount || parent == len(bones) {
			return nil, r.Fail("invalid skeleton parent index")
		}
		names[boneName] = struct{}{}
		// Bind transforms are irrelevant to identity, but validate them.
		if _, err := r.Float32s(9); err != nil {
			return nil, err
		}
		bones = append(bones, Bone{Name: boneName, ParentIndex: parent})
	}
	done := make([]bool, boneCount)
	visiting := make([]bool, boneCount)
	var chain []int
	for startBone := 0; startBone < boneCount; startBone++ {
		chain = chain[:0]
		current := startBone
		for current >= 0 && !done[current] {
			if visiting[current] {
				return nil, r.Fail("cycle in skeleton hierarchy")
			}
			visiting[current] = true
			chain = append(chain, current)
			current = bones[current].ParentIndex
		}
		for _, visited := range chain {
			visiting[visited] = false
			done[visited] = true
		}
	}
	return &Model{
		Header: header, Name: name, Index: index32(fields.Uint(0)), MaterialReference: material,
		BoneCount: boneCount, MeshCount: uint32(meshCount), SocketCount: uint32(socketCount),
		Bones: bones, UnparsedBytes: r.Remaining(), SourceFields: modelSourceFields,
	}, nil
}

// EntityChild is one child reference of an entity.
type EntityChild struct {
	Reference       string `json:"reference"`
	Hardpoint       GUID   `json:"hardpoint"`
	Identifier      GUID   `json:"identifier"`
	HardpointIndex  string `json:"hardpointIndex"`
	IdentifierIndex string `json:"identifierIndex"`
	Attachment      string `json:"attachment"`
}

// Entity is read_entity's result.
type Entity struct {
	Header
	Name                  string               `json:"name"`
	Index                 string               `json:"index"`
	ModelIndex            string               `json:"modelIndex"`
	EffectIndex           string               `json:"effectIndex"`
	FullGUID              *string              `json:"fullGuid"`
	ModelReference        string               `json:"modelReference"`
	EffectReference       string               `json:"effectReference"`
	Children              []EntityChild        `json:"children"`
	MaterialLookReference string               `json:"materialLookReference"`
	RelativeRoot          string               `json:"relativeRoot"`
	SourceFields          *ordered.Map[string] `json:"sourceFields"`
	TrailingBytes         int                  `json:"trailingBytes"`
}

var entitySourceFields = sourceFields(
	"index", "teResourceGUID.Index(Entity.m_GUID)",
	"modelReference", "Info.m_models[Entity.m_modelGUID].GetName()",
	"effectReference", "Info.m_effects[Entity.m_effectGUID].GetName()",
	"children.reference", "Info.m_entities[child.m_defGUID].GetName()",
	"children.hardpoint", "child.m_hardpointGUID",
	"children.identifier", "child.m_identifier")

// ReadEntity reads an .owentity's identity evidence.
func ReadEntity(path string) (*Entity, error) {
	r, header, err := start(path, "owentity", [][2]uint16{{2, 0}, {2, 1}})
	if err != nil {
		return nil, err
	}
	var strs [3]string
	for i := range strs {
		if strs[i], err = r.String(); err != nil {
			return nil, err
		}
	}
	fields, err := r.Unpack("IIIi")
	if err != nil {
		return nil, err
	}
	count := fields.Int(3)
	if err := r.Count(count, 4096, "child entity count"); err != nil {
		return nil, err
	}
	children := make([]EntityChild, 0, count)
	for i := int64(0); i < count; i++ {
		reference, err := r.String()
		if err != nil {
			return nil, err
		}
		child, err := r.Unpack("QQII")
		if err != nil {
			return nil, err
		}
		attachment, err := r.String()
		if err != nil {
			return nil, err
		}
		children = append(children, EntityChild{
			Reference: reference, Hardpoint: DecodeGUID(child.Uint(0)), Identifier: DecodeGUID(child.Uint(1)),
			HardpointIndex: index32(child.Uint(2)), IdentifierIndex: index32(child.Uint(3)), Attachment: attachment,
		})
	}
	look, relative := "", formats.DefaultRelative
	if header.Version == "2.1" {
		if look, err = r.String(); err != nil {
			return nil, err
		}
		if relative, err = r.String(); err != nil {
			return nil, err
		}
	}
	entity := &Entity{
		Header: header, Name: strs[0], Index: index32(fields.Uint(0)), ModelIndex: index32(fields.Uint(1)),
		EffectIndex: index32(fields.Uint(2)), ModelReference: strs[1], EffectReference: strs[2],
		Children: children, MaterialLookReference: look, RelativeRoot: relative, SourceFields: entitySourceFields,
	}
	entity.TrailingBytes = complete(r, &entity.Warnings)
	return entity, nil
}

// Playback is one effect record's timing.
type Playback struct {
	TimeInfoAbsent     bool         `json:"timeInfoAbsent"`
	Start              pyjson.Float `json:"start"`
	End                pyjson.Float `json:"end"`
	HardpointReference string       `json:"hardpointReference"`
}

func readPlayback(r *formats.Reader) (Playback, error) {
	absent, err := r.Unpack("B")
	if err != nil {
		return Playback{}, err
	}
	if absent.Uint(0) > 1 {
		return Playback{}, r.Fail("invalid playback time-presence flag")
	}
	startTime, err := readFloat(r, "playback start", nil)
	if err != nil {
		return Playback{}, err
	}
	end, err := readFloat(r, "playback end", nil)
	if err != nil {
		return Playback{}, err
	}
	hardpoint, err := r.String()
	if err != nil {
		return Playback{}, err
	}
	return Playback{TimeInfoAbsent: absent.Uint(0) == 1, Start: startTime, End: end, HardpointReference: hardpoint}, nil
}

// Counts are an effect's declared record counts.
type Counts struct {
	DMCE int `json:"DMCE"`
	CECE int `json:"CECE"`
	NECE int `json:"NECE"`
	RPCE int `json:"RPCE"`
	FECE int `json:"FECE"`
	OSCE int `json:"OSCE"`
	SVCE int `json:"SVCE"`
}

// DMCE is an animation/model record.
type DMCE struct {
	Playback           Playback `json:"playback"`
	Animation          GUID     `json:"animation"`
	Material           GUID     `json:"material"`
	Model              GUID     `json:"model"`
	ModelReference     string   `json:"modelReference"`
	AnimationReference string   `json:"animationReference"`
}

// CECE is an entity-control record.
type CECE struct {
	Playback           Playback `json:"playback"`
	Action             int      `json:"action"`
	Animation          GUID     `json:"animation"`
	Identifier         GUID     `json:"identifier"`
	IdentifierIndex    string   `json:"identifierIndex"`
	AnimationReference string   `json:"animationReference"`
}

// NECE is a child entity record.
type NECE struct {
	Playback        Playback `json:"playback"`
	Entity          GUID     `json:"entity"`
	IdentifierIndex string   `json:"identifierIndex"`
	EntityReference string   `json:"entityReference"`
}

// RPCE is a model record.
type RPCE struct {
	Playback       Playback `json:"playback"`
	Model          GUID     `json:"model"`
	Material       GUID     `json:"material"`
	ModelReference string   `json:"modelReference"`
}

// SVCE is a voice record.
type SVCE struct {
	Playback           Playback   `json:"playback"`
	VoiceStimulusIndex string     `json:"voiceStimulusIndex"`
	SoundReferences    [][]string `json:"soundReferences"`
}

// Effect is read_effect's result (and the embedded effect of a companion,
// which has no trailingBytes key).
type Effect struct {
	Header
	Index         string               `json:"index"`
	FullGUID      *string              `json:"fullGuid"`
	Duration      pyjson.Float         `json:"duration"`
	Counts        Counts               `json:"counts"`
	DMCE          []DMCE               `json:"dmce"`
	CECE          []CECE               `json:"cece"`
	NECE          []NECE               `json:"nece"`
	RPCE          []RPCE               `json:"rpce"`
	SVCE          []SVCE               `json:"svce"`
	SourceFields  *ordered.Map[string] `json:"sourceFields"`
	TrailingBytes *int                 `json:"trailingBytes,omitempty"`
}

var effectSourceFields = sourceFields(
	"index", "teResourceGUID.Index(EffectInfo.Effect.GUID)",
	"dmce.animation", "EffectParser.DMCEInfo.Animation", "dmce.model", "EffectParser.DMCEInfo.Model",
	"dmce.material", "EffectParser.DMCEInfo.Material", "cece.action", "EffectParser.CECEInfo.Action",
	"cece.animation", "EffectParser.CECEInfo.Animation", "cece.identifier", "EffectParser.CECEInfo.Identifier",
	"nece.entity", "EffectParser.NECEInfo.GUID", "nece.identifierIndex", "teResourceGUID.Index(NECEInfo.Identifier)")

func readEffect(r *formats.Reader) (*Effect, error) {
	header, err := readHeader(r, "oweffect", [][2]uint16{{1, 2}})
	if err != nil {
		return nil, err
	}
	effect := &Effect{Header: header}
	indexField, err := r.Unpack("I")
	if err != nil {
		return nil, err
	}
	effect.Index = index32(indexField.Uint(0))
	if effect.Duration, err = readFloat(r, "effect duration", &zero); err != nil {
		return nil, err
	}
	countFields, err := r.Unpack("7i")
	if err != nil {
		return nil, err
	}
	kinds := [7]string{"DMCE", "CECE", "NECE", "RPCE", "FECE", "OSCE", "SVCE"}
	var counts [7]int
	total := 0
	for i, kind := range kinds {
		count := countFields.Int(i)
		if err := r.Count(count, MaxEffectRecords, kind+" count"); err != nil {
			return nil, err
		}
		counts[i] = int(count)
		total += int(count)
	}
	if total > MaxEffectRecords {
		return nil, r.Fail("effect exceeds evidence record limit")
	}
	effect.Counts = Counts{counts[0], counts[1], counts[2], counts[3], counts[4], counts[5], counts[6]}
	effect.DMCE = make([]DMCE, 0, counts[0])
	effect.CECE = make([]CECE, 0, counts[1])
	effect.NECE = make([]NECE, 0, counts[2])
	effect.RPCE = make([]RPCE, 0, counts[3])
	effect.SVCE = make([]SVCE, 0, counts[6])
	for i := 0; i < counts[0]; i++ {
		playback, err := readPlayback(r)
		if err != nil {
			return nil, err
		}
		f, err := r.Unpack("QQQ")
		if err != nil {
			return nil, err
		}
		modelReference, err := r.String()
		if err != nil {
			return nil, err
		}
		animationReference, err := r.String()
		if err != nil {
			return nil, err
		}
		effect.DMCE = append(effect.DMCE, DMCE{Playback: playback, Animation: DecodeGUID(f.Uint(0)),
			Material: DecodeGUID(f.Uint(1)), Model: DecodeGUID(f.Uint(2)),
			ModelReference: modelReference, AnimationReference: animationReference})
	}
	for i := 0; i < counts[1]; i++ {
		playback, err := readPlayback(r)
		if err != nil {
			return nil, err
		}
		f, err := r.Unpack("BQQI")
		if err != nil {
			return nil, err
		}
		animationReference, err := r.String()
		if err != nil {
			return nil, err
		}
		effect.CECE = append(effect.CECE, CECE{Playback: playback, Action: int(f.Uint(0)),
			Animation: DecodeGUID(f.Uint(1)), Identifier: DecodeGUID(f.Uint(2)),
			IdentifierIndex: index32(f.Uint(3)), AnimationReference: animationReference})
	}
	for i := 0; i < counts[2]; i++ {
		playback, err := readPlayback(r)
		if err != nil {
			return nil, err
		}
		f, err := r.Unpack("QI")
		if err != nil {
			return nil, err
		}
		entityReference, err := r.String()
		if err != nil {
			return nil, err
		}
		effect.NECE = append(effect.NECE, NECE{Playback: playback, Entity: DecodeGUID(f.Uint(0)),
			IdentifierIndex: index32(f.Uint(1)), EntityReference: entityReference})
	}
	for i := 0; i < counts[3]; i++ {
		playback, err := readPlayback(r)
		if err != nil {
			return nil, err
		}
		f, err := r.Unpack("QQ")
		if err != nil {
			return nil, err
		}
		modelReference, err := r.String()
		if err != nil {
			return nil, err
		}
		effect.RPCE = append(effect.RPCE, RPCE{Playback: playback, Model: DecodeGUID(f.Uint(0)),
			Material: DecodeGUID(f.Uint(1)), ModelReference: modelReference})
	}
	// The verified writer emits FECE/OSCE counts, but no corresponding payload loops.
	for _, kind := range []int{4, 5} {
		if counts[kind] != 0 {
			effect.Warnings = append(effect.Warnings, fmt.Sprintf("%s count is %d, but the v1.2 writer omits its records.", kinds[kind], counts[kind]))
		}
	}
	budget := MaxEffectRecords - total
	for i := 0; i < counts[6]; i++ {
		playback, err := readPlayback(r)
		if err != nil {
			return nil, err
		}
		f, err := r.Unpack("Ii")
		if err != nil {
			return nil, err
		}
		lineCount := f.Int(1)
		if err := r.Count(lineCount, int64(budget), "voice line count"); err != nil {
			return nil, err
		}
		budget -= int(lineCount)
		lines := make([][]string, 0, lineCount)
		for l := int64(0); l < lineCount; l++ {
			soundField, err := r.Unpack("i")
			if err != nil {
				return nil, err
			}
			soundCount := soundField.Int(0)
			if err := r.Count(soundCount, int64(budget), "voice sound count"); err != nil {
				return nil, err
			}
			budget -= int(soundCount)
			sounds := make([]string, 0, soundCount)
			for s := int64(0); s < soundCount; s++ {
				sound, err := r.String()
				if err != nil {
					return nil, err
				}
				sounds = append(sounds, sound)
			}
			lines = append(lines, sounds)
		}
		effect.SVCE = append(effect.SVCE, SVCE{Playback: playback, VoiceStimulusIndex: index32(f.Uint(0)), SoundReferences: lines})
	}
	effect.SourceFields = effectSourceFields
	return effect, nil
}

// ReadEffect reads an .oweffect.
func ReadEffect(path string) (*Effect, error) {
	r, err := formats.ReadFile(pypath.Clean(path))
	if err != nil {
		return nil, err
	}
	effect, err := readEffect(r)
	if err != nil {
		return nil, err
	}
	trailing := complete(r, &effect.Warnings)
	effect.TrailingBytes = &trailing
	return effect, nil
}

// Companion is read_companion's result (an .owanim file).
type Companion struct {
	Header
	AnimationIndex  string               `json:"animationIndex"`
	FullGUID        *string              `json:"fullGuid"`
	FPS             pyjson.Float         `json:"fps"`
	Kind            int                  `json:"kind"`
	ClipReference   *string              `json:"clipReference"`
	ModelReference  *string              `json:"modelReference"`
	EffectReference *string              `json:"effectReference"`
	Effect          *Effect              `json:"effect"`
	SourceFields    *ordered.Map[string] `json:"sourceFields"`
	TrailingBytes   int                  `json:"trailingBytes"`
}

var companionSourceFields = sourceFields(
	"animationIndex", "teResourceGUID.Index(Animation.m_GUID)",
	"kind", "OverwatchAnimationEffect.OWAnimType")

// ReadCompanion reads an .owanim animation companion.
func ReadCompanion(path string) (*Companion, error) {
	r, header, err := start(path, "owanim", [][2]uint16{{1, 0}})
	if err != nil {
		return nil, err
	}
	indexField, err := r.Unpack("I")
	if err != nil {
		return nil, err
	}
	fps, err := readFloat(r, "animation frame rate", &zero)
	if err != nil {
		return nil, err
	}
	if !(0 < fps && fps <= 1000) {
		return nil, r.Fail("invalid animation frame rate")
	}
	kindField, err := r.Unpack("i")
	if err != nil {
		return nil, err
	}
	kind := kindField.Int(0)
	if kind != 0 && kind != 1 {
		return nil, r.Fail("unsupported animation companion kind " + strconv.FormatInt(kind, 10))
	}
	companion := &Companion{Header: header, AnimationIndex: index32(indexField.Uint(0)), FPS: fps, Kind: int(kind),
		SourceFields: companionSourceFields}
	if kind == 0 {
		clip, err := r.String()
		if err != nil {
			return nil, err
		}
		model, err := r.String()
		if err != nil {
			return nil, err
		}
		companion.ClipReference, companion.ModelReference = &clip, &model
		if companion.Effect, err = readEffect(r); err != nil {
			return nil, err
		}
	} else {
		effect, err := r.String()
		if err != nil {
			return nil, err
		}
		companion.EffectReference = &effect
	}
	companion.TrailingBytes = complete(r, &companion.Warnings)
	return companion, nil
}
