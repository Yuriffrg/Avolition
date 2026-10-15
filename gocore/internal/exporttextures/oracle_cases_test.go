package exporttextures

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/formats"
	ft "avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pypath"
)

// TestOracleBakeEdgeCases covers every branch of bake_materials with
// handcrafted descriptors: resampling in each direction, RGBA masks, factor
// extremes, missing, external, unreadable and unsupported sources.
func TestOracleBakeEdgeCases(t *testing.T) {
	dir := pypath.Resolve(t.TempDir())
	textures := ordered.New[*formats.Texture]()
	add := func(id, name, channel, transform, input string) {
		textures.Set(id, &formats.Texture{Path: filepath.Join(dir, name), Channel: channel, Transform: transform, InputColorSpace: input})
	}
	for i, size := range sizes {
		noise(t, dir, fmt.Sprintf("t%d.png", i), size[0], size[1], uint64(100+i))
		name := fmt.Sprintf("t%d.png", i)
		add(fmt.Sprintf("rgb%d", i), name, "rgb", "none", "srgb")
		add(fmt.Sprintf("r%d", i), name, "r", "none", "linear")
		add(fmt.Sprintf("rs%d", i), name, "r", "none", "srgb")
		add(fmt.Sprintf("g%d", i), name, "g", "invert", "linear")
		add(fmt.Sprintf("a%d", i), name, "a", "none", "linear")
		add(fmt.Sprintf("lum%d", i), name, "luminance", "none", "srgb")
		add(fmt.Sprintf("met%d", i), name, "r", "ow-metalness", "linear")
		add(fmt.Sprintf("nrm%d", i), name, "rgb", "ow-normal", "linear")
		add(fmt.Sprintf("rgba%d", i), name, "rgba", "none", "linear")
	}
	ft.Write(t, dir, "broken.png", []byte("not an image"))
	ft.Write(t, dir, "note.txt", []byte("text"))
	if err := os.MkdirAll(filepath.Join(dir, "folder.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := noise(t, t.TempDir(), "outside.png", 4, 4, 9)
	add("broken", "broken.png", "rgb", "none", "linear")
	add("note", "note.txt", "rgb", "none", "linear")
	add("folder", "folder.png", "rgb", "none", "linear")
	add("missing", "missing.png", "rgb", "none", "linear")
	add("badchannel", "t0.png", "xyz", "none", "linear")
	add("badtransform", "t0.png", "rgb", "invert", "linear")
	textures.Set("outside", &formats.Texture{Path: outside, Channel: "rgb", Transform: "none", InputColorSpace: "linear"})

	ptr := func(v float64) *float64 { return &v }
	yes, no := true, false
	type binding struct {
		id         string
		uv         int
		repeat     [2]float64
		colorSpace string
	}
	type m = map[string]binding
	var specs []*formats.Material
	material := func(name string, maps m, edit func(*formats.Material)) {
		out := &formats.Material{Name: name, SourcePath: filepath.Join(dir, name+".owmat"),
			BaseColor: [3]float64{.62, .62, .62}, Roughness: .7, Opacity: 1,
			AlphaMode: "OPAQUE", AlphaCutoff: .5, DoubleSided: true, Maps: ordered.New[*formats.MaterialMap]()}
		for _, role := range []string{"normal", "ao", "alpha", "emissive", "metalness", "roughness", "baseColor"} {
			if v, ok := maps[role]; ok {
				out.Maps.Set(role, &formats.MaterialMap{TextureID: v.id, UVIndex: v.uv, Repeat: v.repeat, ColorSpace: v.colorSpace})
			}
		}
		if edit != nil {
			edit(out)
		}
		specs = append(specs, out)
	}
	one := [2]float64{1, 1}
	for i := range sizes {
		j, k := (i+1)%len(sizes), (i+2)%len(sizes)
		id := func(kind string, n int) string { return fmt.Sprintf("%s%d", kind, n) }
		material(fmt.Sprintf("base-ao-%d", i), m{"baseColor": {id("rgb", i), 0, one, "srgb"}, "ao": {id("r", j), 0, one, "linear"}},
			func(x *formats.Material) { x.BaseColor = [3]float64{1, .5, .25} })
		material(fmt.Sprintf("base-rgba-ao-%d", i), m{"baseColor": {id("rgb", j), 0, one, "linear"}, "ao": {id("rgba", k), 0, one, "linear"}}, nil)
		material(fmt.Sprintf("ao-only-%d", i), m{"ao": {id("rs", i), 1, [2]float64{2, 2}, "linear"}},
			func(x *formats.Material) { x.BaseColor = [3]float64{-1, 3, .9} })
		material(fmt.Sprintf("mask-%d", i), m{"baseColor": {id("rgb", i), 0, one, "srgb"}, "ao": {id("r", k), 0, one, "linear"},
			"emissive": {id("lum", j), 0, one, "linear"}}, func(x *formats.Material) {
			x.Emissive, x.EmissiveIntensity, x.EmissiveMode, x.EmissiveUsesAO = [3]float64{.5, 2, 1}, ptr(16), "baseColorMask", &yes
		})
		material(fmt.Sprintf("mask-noao-%d", i), m{"baseColor": {id("rgb", j), 0, one, "srgb"}, "ao": {id("r", i), 1, one, "linear"},
			"emissive": {id("r", k), 0, one, "linear"}}, func(x *formats.Material) {
			x.Emissive, x.EmissiveIntensity, x.EmissiveMode, x.EmissiveUsesAO = [3]float64{1, 1, 1}, ptr(3), "baseColorMask", &no
		})
		material(fmt.Sprintf("mask-aouv-%d", i), m{"baseColor": {id("rgb", j), 0, one, "srgb"}, "ao": {id("r", i), 1, one, "linear"},
			"emissive": {id("r", k), 0, one, "linear"}}, func(x *formats.Material) {
			x.Emissive, x.EmissiveMode, x.EmissiveUsesAO = [3]float64{1, 1, 1}, "baseColorMask", &yes
		})
		material(fmt.Sprintf("mask-nobase-%d", i), m{"emissive": {id("a", i), 0, one, "linear"}}, func(x *formats.Material) {
			x.Emissive, x.EmissiveIntensity, x.EmissiveMode = [3]float64{3, .1, 0}, ptr(-2), "baseColorMask"
		})
		material(fmt.Sprintf("color-emission-%d", i), m{"emissive": {id("rgb", i), 0, one, "srgb"},
			"baseColor": {id("rgb", j), 0, [2]float64{2, 1}, "srgb"}},
			func(x *formats.Material) { x.Emissive = [3]float64{4, 1, .5} })
		material(fmt.Sprintf("pbr-%d", i), m{"roughness": {id("g", i), 0, one, "linear"}, "metalness": {id("met", j), 0, one, "linear"},
			"normal": {id("nrm", k), 0, one, "linear"}, "alpha": {id("a", j), 1, one, "linear"}},
			func(x *formats.Material) {
				x.Roughness, x.Metalness = []float64{.37, 0, 2.5}[i%3], []float64{-1, 1e300, .5}[i%3]
			})
	}
	material("failures", m{"baseColor": {"broken", 0, one, "srgb"}, "normal": {"note", 0, one, "linear"},
		"roughness": {"folder", 0, one, "linear"}, "metalness": {"missing", 0, one, "linear"}, "alpha": {"outside", 0, one, "linear"},
		"ao": {"unregistered", 0, one, "linear"}, "emissive": {"badchannel", 0, one, "linear"}}, nil)
	material("bad-transform", m{"baseColor": {"badtransform", 0, one, "srgb"}}, nil)
	material("no-maps", m{}, nil)
	material("huge-factor", m{"baseColor": {"rgb0", 0, one, "srgb"}}, func(x *formats.Material) { x.BaseColor = [3]float64{1e39, 0, 1} })

	asset := &formats.Asset{Path: filepath.Join(dir, "Entities", "x.owentity"), TextureRoot: dir, Textures: textures,
		Warnings: []string{"a", "b", "a"}}
	for i := 0; i < len(specs); i += 4 {
		materials := ordered.New[*formats.Material]()
		for k := i; k < min(len(specs), i+4); k++ {
			materials.Set(fmt.Sprint(k), specs[k])
		}
		asset.Models = append(asset.Models, &formats.Model{ID: fmt.Sprintf("m%d", i), Name: fmt.Sprintf("Model %d", i), Materials: materials})
	}
	// A repeated appearance and an equal copy under another source path.
	materials := ordered.New[*formats.Material]()
	materials.Set("1", specs[0])
	copied := specs[1].Clone()
	copied.SourcePath = filepath.Join(dir, "elsewhere.owmat")
	materials.Set("2", copied)
	asset.Models = append(asset.Models, &formats.Model{ID: "repeat", Name: "Repeat", Materials: materials})
	c := compareBake(t, asset, pyAsset(t, asset), "")
	t.Logf("%d materials, %d files; Python %.2fs, Go %.2fs", len(specs), c.files, c.pythonSeconds, c.goSeconds)
	_ = math.Inf
}

func TestOracleBakeMaterialAssets(t *testing.T) {
	for _, path := range []string{"tests/material-assets/Entities/MaterialRobot/MaterialRobot.owentity"} {
		full := filepath.Join(repoRoot(), filepath.FromSlash(path))
		asset, err := formats.LoadAsset(full, "")
		if err != nil {
			t.Fatal(err)
		}
		// Python loads the asset itself: an end-to-end comparison.
		compareBake(t, asset, full, "MaterialRobot")
	}
}

// TestOracleBakeBenchmark compares a real character (2048² textures) when
// AVOLITION_BAKE_ASSET names its entity: pixels, descriptors and timings.
func TestOracleBakeBenchmark(t *testing.T) {
	path := os.Getenv("AVOLITION_BAKE_ASSET")
	if path == "" || testing.Short() {
		t.Skip("set AVOLITION_BAKE_ASSET to an .owentity to compare a full character bake")
	}
	asset, err := formats.LoadAsset(path, "")
	if err != nil {
		t.Fatal(err)
	}
	c := compareBake(t, asset, path, "Bench")
	t.Logf("%d files; Python %.2fs, Go %.2fs", c.files, c.pythonSeconds, c.goSeconds)
}
