// Package evidencetest builds mock OWLib evidence fixtures (the helpers of
// tests/test_animation_evidence_formats.py); no real game data is needed.
package evidencetest

import (
	"math/bits"

	ft "avolition/gocore/internal/formats/formatstest"
)

// GUID is guid(index, resource_type, engine, upper_key).
func GUID(index uint32, resourceType uint16, engine uint64, upperKey uint16) uint64 {
	reverse := uint64(bits.Reverse16(resourceType-1) >> 4)
	return engine<<60 | reverse<<48 | uint64(upperKey)<<32 | uint64(index)
}

// Playback is playback(absent).
func Playback(absent bool) []byte {
	flag := 0
	if absent {
		flag = 1
	}
	return append(ft.Pack("Bff", flag, 0, 0), ft.String("null")...)
}

// EffectHeader is effect_header(counts, version).
func EffectHeader(counts [7]int32, version [2]uint16) []byte {
	blob := ft.String("oweffect")
	values := []any{version[0], version[1], 0xBEEF, 2.0}
	for _, c := range counts {
		values = append(values, c)
	}
	return append(blob, ft.Pack("HHIf7i", values...)...)
}

// DefaultEffectHeader is effect_header() with no records.
func DefaultEffectHeader() []byte { return EffectHeader([7]int32{}, [2]uint16{1, 2}) }

// Cat concatenates byte slices.
func Cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
