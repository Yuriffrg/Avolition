package c4dbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFeatureCapableBridgeWinsBeforeHeartbeat(t *testing.T) {
	queue := t.TempDir()
	for _, row := range []struct {
		instance                                          string
		age                                               float64
		octane, maps                                      bool
		motion, skeleton, timing, exact, gameplay, height any
	}{
		{"updated", 2, true, true, true, true, true, true, true, true},
		{"absolute-height-079", .1, true, true, true, true, true, true, true, nil},
		{"combined-motion-078", .1, true, true, true, true, true, nil, nil, nil},
		{"separate-motion-077", .1, true, true, true, nil, true, nil, nil, nil},
		{"old", .1, true, true, nil, nil, nil, nil, nil, nil},
		{"partial", .2, true, true, true, true, false, true, false, false},
		{"invalid-flags", .3, true, true, true, "true", 1, "true", 1, "true"},
		{"no-octane", 0, false, true, true, true, true, true, true, true},
		{"no-maps", 0, true, false, true, true, true, true, true, true},
	} {
		mustWrite(t, filepath.Join(queue, "status-"+row.instance+".json"), obj("protocol", 1, "instance", row.instance,
			"heartbeat", Float(100-row.age), "octane", row.octane, "mapInstances", row.maps, "motionClips", row.motion, "skeletonMotionClips", row.motion,
			"combinedMotionClips", row.skeleton, "mapTiming24", row.timing, "exactFrameTimes", row.exact,
			"optimizedGameplay1P", row.gameplay, "povHeightZero", row.height))
	}
	bridge := NewClient(queue)
	bridge.now = func() float64 { return 100 }
	if got := bridge.Status().Instance(); got != "updated" {
		t.Fatalf("Selected %s instead of updated bridge", got)
	}
	// Newer heartbeats still win between equally capable bridges.
	mustWrite(t, filepath.Join(queue, "status-newest.json"), obj("protocol", 1, "instance", "newest", "heartbeat", Float(99),
		"octane", true, "mapInstances", true, "motionClips", true, "skeletonMotionClips", true, "combinedMotionClips", true,
		"mapTiming24", true, "exactFrameTimes", true, "optimizedGameplay1P", true, "povHeightZero", true))
	if got := bridge.Status().Instance(); got != "newest" {
		t.Fatalf("Selected %s instead of newest capable bridge", got)
	}
}

func TestMotionDispatchTargetsFeatureCapableInstance(t *testing.T) {
	root := t.TempDir()
	queue := filepath.Join(root, "queue")
	os.Mkdir(queue, 0o755)
	for _, row := range []struct {
		instance string
		age      float64
		updated  bool
	}{{"updated", 2, true}, {"old", .1, false}} {
		mustWrite(t, filepath.Join(queue, "status-"+row.instance+".json"), obj("protocol", 1, "instance", row.instance,
			"heartbeat", Float(100-row.age), "octane", true, "mapInstances", true, "motionClips", true, "skeletonMotionClips", true,
			"combinedMotionClips", true, "mapTiming24", true, "exactFrameTimes", true, "optimizedGameplay1P", true, "povHeightZero", row.updated))
	}
	bridge := NewClient(queue)
	bridge.now = func() float64 { return 100 }
	var target string
	bridge.sleep = func(_ float64) {
		jobs, err := os.ReadDir(filepath.Join(queue, "jobs"))
		if err != nil || len(jobs) != 1 {
			t.Fatalf("jobs: %v %v", jobs, err)
		}
		job := filepath.Join(queue, "jobs", jobs[0].Name())
		raw, err := ReadJSON(filepath.Join(job, "request.json"), 1024)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			Instance string `json:"instance"`
		}
		if err = json.Unmarshal(raw, &request); err != nil {
			t.Fatal(err)
		}
		target = request.Instance
		if err = os.WriteFile(filepath.Join(job, "scene.c4d"), []byte("simulated native scene"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(job, "result.json"), obj("ok", true, "data", obj()))
	}
	_, err := bridge.Export(obj("motionClip", true, "gameplay1P", true), filepath.Join(root, "out.c4d"), filepath.Join(root, "tex"), ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if target != "updated" {
		t.Fatalf("Job targeted %s instead of capable instance", target)
	}
}
