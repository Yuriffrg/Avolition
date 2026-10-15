package formats_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
)

func TestCloneIsDeepAndEqual(t *testing.T) {
	root := filepath.Join("..", "..", "..", "tests", "demo-assets")
	asset, err := formats.LoadAsset(filepath.Join(root, "Entities/PreviewRobot/PreviewRobot.owentity"), root)
	if err != nil {
		t.Fatal(err)
	}
	maps := ordered.New[*formats.MaterialMap]()
	maps.Set("baseColor", &formats.MaterialMap{TextureID: "t"})
	materials := ordered.New[*formats.Material]()
	materials.Set("0", &formats.Material{Name: "m", Maps: maps})
	asset.Models[0].Materials = materials
	original, _ := json.Marshal(asset)
	clone := asset.Clone()
	copied, _ := json.Marshal(clone)
	if string(original) != string(copied) {
		t.Fatal("clone differs")
	}
	model := clone.Models[0]
	model.Meshes[0].Positions[0] = 99
	model.Bones[0].Name = "changed"
	m, _ := model.Materials.Get("0")
	m.Name = "changed"
	mapping, _ := m.Maps.Get("baseColor")
	mapping.TextureID = "changed"
	*clone.ExportHierarchy[1].ParentID = "changed"
	clone.Warnings = append(clone.Warnings, "new")
	after, _ := json.Marshal(asset)
	if string(original) != string(after) {
		t.Fatal("the clone shares writable state with the original")
	}
}
