package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Consecutive load_texture calls run in parallel; every other request still
// sees run_stdio's serial order (here: a release after the loads makes the
// next texture request fail, exactly as in Python).
func TestStdioParallelTextureLoadsKeepSerialSemantics(t *testing.T) {
	a, _ := stdioAPI(t)
	material := filepath.Join(repo(t), "tests", "material-assets", "Entities", "MaterialRobot", "MaterialRobot.owentity")
	var input strings.Builder
	write := func(r map[string]any) {
		line, _ := json.Marshal(r)
		input.Write(line)
		input.WriteByte('\n')
	}
	asset := okMap(t, call(t, a, "load_entity", material))
	var ids []string
	for id := range m(asset["textures"]) {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) < 3 {
		t.Fatalf("expected several textures, got %d", len(ids))
	}
	write(request("entity", "load_entity", material))
	want := map[string]bool{}
	for round := 0; round < 3; round++ {
		for i, id := range ids {
			key := `"t` + string(rune('a'+round)) + string(rune('a'+i)) + `"`
			want[key] = true
			write(request(key[1:len(key)-1], "load_texture", id, 64))
		}
	}
	write(request("other", "load_entity", filepath.Join(demoAssets(t), "Entities", "PreviewRobot", "PreviewRobot.owentity")))
	write(request("stale", "load_texture", ids[0], 64))

	var out bytes.Buffer
	if err := a.ServeStdio(strings.NewReader(input.String()), &out); err != nil {
		t.Fatal(err)
	}
	replies := map[string]map[string]any{}
	var order []string
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(nil, 64<<20)
	for scanner.Scan() {
		var reply struct {
			ID     json.RawMessage
			Result map[string]any
		}
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			t.Fatalf("unparseable reply: %v", err)
		}
		replies[string(reply.ID)] = reply.Result
		order = append(order, string(reply.ID))
	}
	if len(replies) != len(want)+3 {
		t.Fatalf("got %d replies, want %d", len(replies), len(want)+3)
	}
	for key := range want {
		if replies[key]["ok"] != true || !strings.HasPrefix(m(replies[key]["data"])["dataUri"].(string), "data:image/png;base64,") {
			t.Fatalf("%s: %v", key, replies[key])
		}
	}
	// load_entity of another asset replaced the animation textures before the
	// last request, so it must fail, and it must be answered after "other".
	if replies[`"stale"`]["ok"] != false || order[len(order)-1] != `"stale"` || order[len(order)-2] != `"other"` {
		t.Fatalf("serial order broken: last replies %v, stale=%v", order[len(order)-2:], replies[`"stale"`])
	}
	if order[0] != `"entity"` {
		t.Fatalf("first reply %s, want the entity load", order[0])
	}
}
