// Package maptest builds OWLib .owmap files for tests. It ports the fixture
// builders of tests/test_map_formats.py (record_bytes, map_bytes,
// write_fixture) plus a large synthetic map library for benchmarks.
package maptest

import (
	"math"
	"testing"

	ft "avolition/gocore/internal/formats/formatstest"
)

// Record is record_bytes' arguments.
type Record struct {
	Position [3]float64
	Scale    [3]float64
	Rotation [4]float64
}

// NewRecord returns record_bytes' defaults: origin, unit scale, identity.
func NewRecord() Record {
	return Record{Scale: [3]float64{1, 1, 1}, Rotation: [4]float64{0, 0, 0, 1}}
}

// At returns the default record at position.
func At(x, y, z float64) Record {
	r := NewRecord()
	r.Position = [3]float64{x, y, z}
	return r
}

// Bytes is record_bytes(...).
func (r Record) Bytes() []byte {
	return ft.Pack("10f", r.Position[0], r.Position[1], r.Position[2], r.Scale[0], r.Scale[1], r.Scale[2],
		r.Rotation[0], r.Rotation[1], r.Rotation[2], r.Rotation[3])
}

// RecordBytes is record_bytes() with the defaults.
func RecordBytes() []byte { return NewRecord().Bytes() }

// Group is one material group of an object: look reference and records.
type Group struct {
	Look    string
	Records [][]byte
}

// Object is one map object: model reference and groups.
type Object struct {
	Model  string
	Groups []Group
}

// Detail is one map detail: model, look and record.
type Detail struct {
	Model, Look string
	Record      []byte
}

// Sound is one sound emitter: position and files.
type Sound struct {
	Position [3]float64
	Files    []string
}

// Map is map_bytes' arguments. Start from NewMap for the defaults.
type Map struct {
	Objects []Object
	Details []Detail
	Lights  [][]byte
	Sounds  []Sound
	Version [2]uint16
	Name    string
}

// NewMap returns map_bytes' defaults: empty version 2.1 "Fixture map".
func NewMap() Map { return Map{Version: [2]uint16{2, 1}, Name: "Fixture map"} }

// Bytes is map_bytes(...).
func (m Map) Bytes() []byte {
	blob := ft.Pack("HH", m.Version[0], m.Version[1])
	blob = append(blob, ft.String(m.Name)...)
	blob = append(blob, ft.Pack("III", len(m.Objects), len(m.Details), len(m.Lights))...)
	for _, object := range m.Objects {
		blob = append(blob, ft.String(object.Model)...)
		blob = append(blob, ft.Pack("I", len(object.Groups))...)
		for _, group := range object.Groups {
			blob = append(blob, ft.String(group.Look)...)
			blob = append(blob, ft.Pack("I", len(group.Records))...)
			for _, record := range group.Records {
				blob = append(blob, record...)
			}
		}
	}
	for _, detail := range m.Details {
		blob = append(blob, ft.String(detail.Model)...)
		blob = append(blob, ft.String(detail.Look)...)
		blob = append(blob, detail.Record...)
	}
	for _, light := range m.Lights {
		blob = append(blob, light...)
	}
	blob = append(blob, ft.Pack("I", len(m.Sounds))...)
	for _, sound := range m.Sounds {
		blob = append(blob, ft.Pack("3fi", sound.Position[0], sound.Position[1], sound.Position[2], len(sound.Files))...)
		for _, file := range sound.Files {
			blob = append(blob, ft.String(file)...)
		}
	}
	return blob
}

// Details returns map_bytes(details=...) with the defaults.
func Details(details ...Detail) []byte {
	m := NewMap()
	m.Details = details
	return m.Bytes()
}

// WriteFixture is write_fixture(root): two appearances of one triangle plus
// an attached entity detail, all parsed by the real loaders. It returns the
// map's path (root/Example.owmap).
func WriteFixture(t testing.TB, root string) string {
	t.Helper()
	write := func(name string, data []byte) string { return ft.Write(t, root, name, data) }
	prop := ft.NewModel()
	prop.Bones, prop.Slots, prop.Name = nil, 0, "Prop"
	write("Models/Prop/Prop.owmdl", prop.Bytes())
	write("Models/Prop/ModelLooks/Blue.owmat", ft.LookBytes(ft.LookEntry{Key: 4000, Reference: "../Materials/Blue.owmat"}))
	write("Models/Prop/ModelLooks/Red.owmat", ft.LookBytes(ft.LookEntry{Key: 4000, Reference: "../Materials/Red.owmat"}))
	write("Models/Prop/Materials/Blue.owmat", ft.MaterialBytes(nil, nil, 11))
	write("Models/Prop/Materials/Red.owmat", ft.MaterialBytes(nil, nil, 12))
	body := ft.NewModel()
	body.Name = "Body"
	body.Sockets = []ft.Socket{{Name: "socket", Bone: "bone_0002", Position: [3]float64{2, 4, 0}, Rotation: [4]float64{0, 0, 0, 1}}}
	write("Body.owmdl", body.Bytes())
	child := ft.NewModel()
	child.Bones, child.Slots, child.Name = nil, 0, "Child"
	write("Child.owmdl", child.Bytes())
	write("Child.owentity", ft.Entity{Name: "Child", Model: "Child.owmdl", Minor: 1, Relative: "."}.Bytes())
	write("Body.owentity", ft.Entity{Name: "Body", Model: "Body.owmdl", Minor: 1, Relative: ".",
		Children: []ft.Child{{Reference: "Child.owentity", Attachment: "socket"}}}.Bytes())
	red := Record{Position: [3]float64{-1, 2, 3}, Scale: [3]float64{-2, 3, 4},
		Rotation: [4]float64{0, 0, math.Sqrt(.5), math.Sqrt(.5)}}
	detail := Record{Position: [3]float64{100, 200, 300}, Scale: [3]float64{2, 3, 4}, Rotation: [4]float64{0, 0, 0, 1}}
	m := NewMap()
	m.Objects = []Object{{Model: "Models/Prop/Prop.owmdl", Groups: []Group{
		{Look: "Models/Prop/ModelLooks/Blue.owmat", Records: [][]byte{At(10, 20, 30).Bytes(), At(40, 50, 60).Bytes()}},
		{Look: "Models/Prop/ModelLooks/Red.owmat", Records: [][]byte{red.Bytes()}},
	}}}
	m.Details = []Detail{{Model: "Body.owentity", Look: "null", Record: detail.Bytes()}}
	return write("Example.owmap", m.Bytes())
}
