package bonenames

import (
	"encoding/json"
	"testing"

	"avolition/gocore/internal/oracle"
)

func TestMatchesPython(t *testing.T) {
	var want map[string]string
	raw := oracle.Exec(t, "from owpreview.bone_names import boneDict\nresult = boneDict").MustValue(t)
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(Names) {
		t.Fatalf("Python has %d names, Go %d", len(want), len(Names))
	}
	for key, value := range want {
		if BoneName(key) != value {
			t.Errorf("BoneName(%q) = %q, want %q", key, BoneName(key), value)
		}
	}
	if BoneName("bone_FFFF") != "bone_FFFF" {
		t.Error("unknown names must pass through")
	}
}
