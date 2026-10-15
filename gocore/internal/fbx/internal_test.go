package fbx

import (
	"testing"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
)

// Port of test_unbaked_mask_cannot_make_constant_white_emission.
func TestUnbakedMaskCannotMakeConstantWhiteEmission(t *testing.T) {
	intensity := 16.0
	maps := ordered.New[*formats.MaterialMap]()
	maps.Set("emissive", &formats.MaterialMap{RelativePath: "tex/mask.png"})
	for _, descriptor := range []*formats.Material{
		{EmissiveMode: "baseColorMask", EmissiveIntensity: &intensity},
		{UnbakedMaps: []string{"emissive"}, EmissiveIntensity: &intensity, Maps: maps},
	} {
		s := newScene()
		s.material(descriptor, "Material")
		material := s.objects.children[0]
		var props *node
		for _, c := range material.children {
			if c.name == "Properties70" {
				props = c
			}
		}
		var strength *node
		for _, p := range props.children {
			if string(p.props[0].str) == "EmissiveFactor" {
				strength = p
			}
		}
		if last := strength.props[len(strength.props)-1]; last.kind != 'D' || last.num != 0 {
			t.Fatal(last)
		}
		for _, n := range s.objects.children {
			if n.name == "Texture" {
				t.Fatal("texture written for an unbaked map")
			}
		}
	}
}
