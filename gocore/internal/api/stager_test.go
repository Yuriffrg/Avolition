package api

import (
	"encoding/json"
	"strings"
	"testing"

	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/formats/formatstest"
	"avolition/gocore/internal/mapexport"
)

type stagerTestBridge struct {
	*mockBridge
	job map[string]any
}

func (b *stagerTestBridge) MapBridge() mapexport.Bridge { return stagerTestMailbox{b} }

type stagerTestMailbox struct{ bridge *stagerTestBridge }

func (m stagerTestMailbox) Status() *c4dbridge.Status {
	status := c4dbridge.NewStatus(nil)
	status.Set("instance", "cinema")
	return status
}

func (m stagerTestMailbox) Export(payload any, destination, textureFolder string, options c4dbridge.ExportOptions) (json.RawMessage, error) {
	if !options.Stager || options.Instance != "cinema" || destination != "" {
		panic("incorrect Stager transport")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var job map[string]any
	if err = json.Unmarshal(data, &job); err != nil {
		return nil, err
	}
	m.bridge.job = job
	if job["operation"] == "stagerDestination" {
		return json.Marshal(map[string]any{"documentId": "reviewed", "documentName": "Detached.c4d", "cameras": []any{map[string]any{"id": "cam", "name": "POV"}}})
	}
	return json.Marshal(map[string]any{"documentId": job["documentId"], "sourceCount": len(l(job["actions"]))})
}

func TestStagerMixedSetupsShareOneAssetAndKeepPerActionTiming(t *testing.T) {
	f := newQueueFixture(t)
	b := &stagerTestBridge{mockBridge: f.bridge}
	f.api.bridge = b
	first := f.save(t, with(f.entry, "motionClipName", "Idle", "assignments", l(f.entry["assignments"])[:1]))
	// Idle is arms-only at 30 fps. Reload includes a simultaneous 60 fps weapon.
	gun := formatstest.Clip{Bones: []formatstest.ClipBone{{Name: "bone_0002", Tracks: []formatstest.Track{{Kind: 0, Keys: []formatstest.Key{formatstest.K(0, 0, 2, 0), formatstest.K(6, 0, 4, 0)}}}}}, Duration: 7, FPS: 60, Version: [2]uint16{2, 0}}
	writeFile(t, f.gun, gun.Bytes())
	second := f.save(t, with(f.entry, "motionClipName", "Reload"))
	ids := []any{first["id"], second["id"]}
	review := okMap(t, call(t, f.api, "stager_rig_destination", ids))
	if review["documentName"] != "Detached.c4d" || review["instance"] != "cinema" || len(l(review["rootMotionObjects"])) == 0 || review["motionTargetName"] == "" {
		t.Fatal(review)
	}
	req := map[string]any{"documentId": "reviewed", "kind": "pov", "cameraId": "cam", "instance": "cinema", "actions": []any{map[string]any{"id": ids[0], "role": "idle"}, map[string]any{"id": ids[1], "role": "reload", "binding": "KeyR", "edge": "press"}}}
	reviewedID := m(b.job["rigIds"])["pov"]
	var previousRig, previousGeometry, previousSource any
	for i := 0; i < 2; i++ {
		out := okMap(t, call(t, f.api, "create_stager_rig", req))
		if out["sourceCount"] != float64(2) {
			t.Fatal(out)
		}
		job := b.job
		if job["rigId"] != reviewedID || job["operation"] != "createStagerRig" || len(l(m(job["asset"])["models"])) != 3 || job["rootMotionId"] != "" || job["cameraId"] != "cam" {
			t.Fatal("missing geometry or target")
		}
		actions := l(job["actions"])
		idle, reload := m(actions[0]), m(actions[1])
		if idle["id"] != ids[0] || idle["name"] != "Idle" || reload["name"] != "Reload" || reload["binding"] != "KeyR" || reload["asset"] != nil {
			t.Fatal("bad action metadata")
		}
		firstAnim, secondAnim := m(idle["animation"]), m(reload["animation"])
		if firstAnim["fps"] != float64(30) || firstAnim["lastFrame"] != float64(2) || secondAnim["fps"] != float64(60) || secondAnim["lastFrame"] != float64(6) {
			t.Fatal("changed action timing")
		}
		if len(l(secondAnim["assignments"])) != 2 || len(l(firstAnim["assignments"])) != 1 || reload["fingerprint"] == idle["fingerprint"] {
			t.Fatal("lost unique action composition")
		}
		if i > 0 && (job["rigId"] != previousRig || job["rigFingerprint"] != previousGeometry || reload["fingerprint"] != previousSource) {
			t.Fatal("re-import identity changed")
		}
		previousRig, previousGeometry, previousSource = job["rigId"], job["rigFingerprint"], reload["fingerprint"]
	}
}

func TestStagerRejectsMixedEntitiesAndDuplicateRoles(t *testing.T) {
	f := newQueueFixture(t)
	id := f.save(t, nil)["id"]
	other := f.save(t, with(f.entry, "entityPath", strings.Replace(f.entity, "Main.owentity", "Weapon.owentity", 1), "assignments", l(f.entry["assignments"])[:1]))["id"]
	msg := failed(t, call(t, f.api, "stager_rig_destination", []any{id, other}))
	if !strings.Contains(msg, "same entity") {
		t.Fatal(msg)
	}
	req := map[string]any{"instance": "cinema", "documentId": "detached", "kind": "actor", "actions": []any{map[string]any{"id": id, "role": "idle"}, map[string]any{"id": other, "role": "idle"}}}
	msg = failed(t, call(t, f.api, "create_stager_rig", req))
	if !strings.Contains(msg, "only once") {
		t.Fatal(msg)
	}
}
