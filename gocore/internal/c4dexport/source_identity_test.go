package c4dexport_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"avolition/gocore/internal/c4dexport"
	"avolition/gocore/internal/exporttest"
	"avolition/gocore/internal/oracle"
)

func TestSourceIdentityUsesOriginalEntityPathOnEitherPlatform(t *testing.T) {
	cases := []struct {
		path     string
		gameplay bool
		hero     string
	}{
		{`C:\Exports\Heroes\Widowmaker\Skin\Classic\Entities\Gameplay1P\Gameplay1P.owentity`, true, "Widowmaker"},
		{"/Exports/Heroes/Lúcio/Entities/Gameplay1P/Gameplay1P.owentity", true, "Lúcio"},
		{"//NAS/export/Heroes/士兵/Entities/Gameplay3P/Gameplay3P.owentity", false, "士兵"},
		{"/Exports/Gameplay1P.owmdl", false, ""},
		{"/Exports/Gameplay1P_Reload.owentity", false, ""},
		{"/Exports/gameplay1p.OWENTITY", true, ""},
		{"/NotHeroes/Widowmaker/Gameplay1P.owentity", true, ""},
		{"/Heroes/outer/Exports/HEROES/Tracer/Entities/Gameplay1P.owentity", true, "Tracer"},
		{"/Heroes/../Gameplay1P.owentity", true, ""},
		{"/Heroes/Gameplay1P.owentity", true, ""},
		{"", false, ""},
	}
	var paths []string
	for _, c := range cases {
		paths = append(paths, c.path)
		gameplay, hero := c4dexport.SourceIdentity(c.path)
		if gameplay != c.gameplay || hero != c.hero {
			t.Errorf("%q: got (%v, %q), want (%v, %q)", c.path, gameplay, hero, c.gameplay, c.hero)
		}
	}
	raw := oracle.Exec(t, `
from owpreview.c4d_export import source_identity
result = [source_identity(path) for path in args[0]]
`, paths).MustValue(t)
	var results [][]any
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		if !reflect.DeepEqual(results[i], []any{c.gameplay, c.hero}) {
			t.Fatalf("Python identity: %v", results[i])
		}
	}
}

func TestPayloadSourceMetadataIgnoresRenamesAndKeepsAnimationFPS(t *testing.T) {
	asset, clip := exporttest.SelectedFixture(t)
	asset.Path = `C:\Export\Heroes\Widowmaker\Entities\Gameplay1P\Gameplay1P.owentity`
	asset.Name = "User renamed character"
	for i := range asset.ExportHierarchy {
		asset.ExportHierarchy[i].Name = "User renamed object"
	}
	var cases []oracleCase
	for _, fps := range []float64{24, 30, 60, 29.97, 59.94} {
		clipCopy := *clip
		clipCopy.FPS = fps
		opts := c4dexport.DefaultPayloadOptions()
		opts.ClipName = "Reload"
		c := payloadCase(t, "original path with source FPS", asset, &clipCopy, nil, opts)
		cases = append(cases, c)
		value, err := c.run()
		if err != nil {
			t.Fatal(err)
		}
		payload := value.(*c4dexport.Payload)
		if !payload.Gameplay1P || payload.HeroName != "Widowmaker" || payload.Animation.FPS != fps {
			t.Fatal("Source metadata or animation FPS lost", payload.Gameplay1P, payload.HeroName, payload.Animation.FPS)
		}
		if !reflect.DeepEqual(payload.RequiredBridgeCapabilities(), []string{"combinedMotionClips", "exactFrameTimes", "optimizedGameplay1P", "povHeightZero"}) {
			t.Fatal("Gameplay1P export did not require its native features")
		}
	}
	setAsset, list := exporttest.AssignedFixture(t)
	setAsset.Path = asset.Path
	setOpts := c4dexport.DefaultSetOptions()
	combined := setCase(t, "combined source FPS", setAsset, assignments(list), nil, setOpts)
	cases = append(cases, combined)
	value, err := combined.run()
	if err != nil {
		t.Fatal(err)
	}
	payload := value.(*c4dexport.Payload)
	if !payload.Gameplay1P || payload.HeroName != "Widowmaker" || payload.Animation.FPS != 30 {
		t.Fatal("Combined export should retain the highest assigned source FPS")
	}
	runCases(t, cases) // Also exercises optimized JSON metadata against Python.
	asset.Path = "/Exports/Heroes/Widowmaker/Entities/Gameplay3P.owentity"
	asset.Name = "Gameplay1P"
	for i := range asset.ExportHierarchy {
		asset.ExportHierarchy[i].Name = "Gameplay1P"
	}
	ordinary, err := c4dexport.MakePayload(asset, clip, nil, c4dexport.DefaultPayloadOptions())
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Gameplay1P || !reflect.DeepEqual(ordinary.RequiredBridgeCapabilities(), []string{"combinedMotionClips", "exactFrameTimes"}) {
		t.Fatal("Renaming a non-first-person source activated optimization")
	}
}

func TestMixed30And60FPSKeepsClipSecondsAndHighestSourceRate(t *testing.T) {
	asset, list := exporttest.AssignedFixture(t)
	asset.Path = "/Exports/Heroes/Widowmaker/Entities/Gameplay1P.owentity"
	list[0].Clip.FPS, list[1].Clip.FPS = 30, 60
	opts := c4dexport.DefaultSetOptions()
	item := setCase(t, "mixed 30/60 FPS", asset, assignments(list), nil, opts)
	value, err := item.run()
	if err != nil {
		t.Fatal(err)
	}
	animation := value.(*c4dexport.Payload).Animation
	if animation.FPS != 60 || animation.LastFrame != 48 || *animation.DurationSeconds != .8 {
		t.Fatal("Mixed clips were retimed", animation.FPS, animation.LastFrame, animation.DurationSeconds)
	}
	if animation.Assignments[0].FPS != 30 || animation.Assignments[1].FPS != 60 || animation.Assignments[1].DurationSeconds != .25 {
		t.Fatal("Individual clip timing changed", animation.Assignments)
	}
	runCases(t, []oracleCase{item})
}
