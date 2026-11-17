package c4dbridge

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStagerRequestUsesReviewedInstanceAndRequiresNoSceneFile(t *testing.T) {
	queue := t.TempDir()
	bridge := NewClient(queue)
	for _, id := range []string{"reviewed", "newest"} {
		age := 0.
		if id == "reviewed" {
			age = 1
		}
		mustWrite(t, filepath.Join(queue, "status-"+id+".json"), obj("protocol", 1, "instance", id, "heartbeat", Float(now()-age), "octane", true, "stagerRig", true))
	}
	if bridge.Status().Instance() != "newest" {
		t.Fatal("fixture status order")
	}
	bridge.sleep = func(float64) {
		job := filepath.Join(queue, "jobs", listDir(t, filepath.Join(queue, "jobs"))[0])
		request, err := ReadJSON(filepath.Join(job, "request.json"), 65536)
		if err != nil || !strings.Contains(string(request), `"instance":"reviewed"`) {
			t.Fatalf("wrong instance: %s %v", request, err)
		}
		mustWrite(t, filepath.Join(job, "result.json"), obj("ok", true, "data", obj("created", true)))
	}
	raw, err := bridge.Export(obj("operation", "createStagerRig"), "", "", ExportOptions{Instance: "reviewed", Stager: true})
	if err != nil || string(raw) != `{"created":true}` {
		t.Fatalf("result %s %v", raw, err)
	}
	_, err = bridge.Export(obj("operation", "createStagerRig"), "", "", ExportOptions{Instance: "gone", Stager: true})
	if err == nil || !strings.Contains(err.Error(), "Review the destination") {
		t.Fatal(err)
	}
	if len(listDir(t, filepath.Join(queue, "jobs"))) != 0 {
		t.Fatal("unexpected surviving job")
	}
}

func TestOldBridgeCannotReceiveStagerJobs(t *testing.T) {
	queue := t.TempDir()
	b := NewClient(queue)
	b.statusFunc = fakeStatus(true)
	_, err := b.Export(obj("operation", "stagerDestination"), "", "", ExportOptions{Stager: true})
	if err == nil || !strings.Contains(err.Error(), "0.7.14") || exists(filepath.Join(queue, "jobs")) {
		t.Fatal(err)
	}
}
