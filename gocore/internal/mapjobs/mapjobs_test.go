package mapjobs

// The MapExportJobs behaviour exercised by tests/test_map_jobs.py, without
// BrowserAPI (its request plumbing belongs to the API port).

import (
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"
)

func ptr(v int) *int { return &v }

func TestIdleSnapshot(t *testing.T) {
	jobs := New()
	idle, err := jobs.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(idle)
	want := `{"id":null,"state":"Idle","busy":false,"message":"","completed":null,"total":null,"result":null,"error":null,"elapsedSeconds":0}`
	if string(data) != want {
		t.Fatal(string(data))
	}
	unknown := "unknown"
	if _, err := jobs.Snapshot(&unknown); err == nil || err.Error() != "This map export job is no longer available." {
		t.Fatal(err)
	}
}

func TestProgressResultAndOwnedSnapshots(t *testing.T) {
	jobs := New()
	entered, finish := make(chan struct{}), make(chan struct{})
	started, err := jobs.Start(func(progress Progress) (any, error) {
		progress("Building Cinema 4D scene…", ptr(3), ptr(12))
		close(entered)
		<-finish
		return map[string]any{"path": "/out/Map.c4d", "warnings": []string{"Test native export only"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.State != "Running" || !started.Busy || started.Message != "Loading map…" || started.ID == nil ||
		!regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(*started.ID) {
		t.Fatalf("%+v", started)
	}
	<-entered
	running, err := jobs.Snapshot(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.Message != "Building Cinema 4D scene…" || *running.Completed != 3 || *running.Total != 12 ||
		string(running.Result) != "null" || running.Error != nil || running.ElapsedSeconds < 0 {
		t.Fatalf("%+v", running)
	}
	// Duplicate guard for both entry points.
	for _, attempt := range []func() error{
		func() error { _, err := jobs.Start(nil); return err },
		func() error { _, err := jobs.Run(nil); return err },
	} {
		if err := attempt(); err == nil || err.Error() != "A map export is already running." {
			t.Fatal(err)
		}
	}
	close(finish)
	jobs.Wait()
	final, _ := jobs.Snapshot(started.ID)
	if final.State != "Completed" || final.Busy || final.Message != "Map export completed." || final.Error != nil {
		t.Fatalf("%+v", final)
	}
	var result map[string]any
	json.Unmarshal(final.Result, &result)
	if result["path"] != "/out/Map.c4d" {
		t.Fatal(string(final.Result))
	}
	final.Result[2] = 'X'
	again, _ := jobs.Snapshot(nil)
	if string(again.Result) == string(final.Result) {
		t.Fatal("snapshot result is shared")
	}
	time.Sleep(5 * time.Millisecond)
	later, _ := jobs.Snapshot(nil)
	if later.ElapsedSeconds != again.ElapsedSeconds {
		t.Fatal("elapsed time keeps running after the job ended")
	}
}

func TestFailedJobIsObservableAndALaterExportCanComplete(t *testing.T) {
	jobs := New()
	started, _ := jobs.Start(func(Progress) (any, error) { return nil, errors.New("Broken map fixture") })
	jobs.Wait()
	failed, _ := jobs.Snapshot(started.ID)
	if failed.State != "Failed" || *failed.Error != "Broken map fixture" || failed.Message != "Broken map fixture" ||
		string(failed.Result) != "null" {
		t.Fatalf("%+v", failed)
	}
	recovered, err := jobs.Start(func(Progress) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatal(err)
	}
	jobs.Wait()
	if *recovered.ID == *started.ID {
		t.Fatal("same id")
	}
	if _, err := jobs.Snapshot(started.ID); err == nil {
		t.Fatal("old id still available")
	}
	final, _ := jobs.Snapshot(recovered.ID)
	if final.State != "Completed" || final.Error != nil || string(final.Result) != `"ok"` {
		t.Fatalf("%+v", final)
	}
}

type emptyError struct{}

func (*emptyError) Error() string { return "" }

func TestErrorTextFallsBackToTypeNameAndPanicsFail(t *testing.T) {
	jobs := New()
	if _, err := jobs.Run(func(Progress) (any, error) { return nil, &emptyError{} }); err == nil {
		t.Fatal("error not propagated")
	}
	failed, _ := jobs.Snapshot(nil)
	if *failed.Error != "emptyError" {
		t.Fatal(*failed.Error)
	}
	jobs.Start(func(Progress) (any, error) { panic("native crash") })
	jobs.Wait()
	failed, _ = jobs.Snapshot(nil)
	if failed.State != "Failed" || *failed.Error != "native crash" {
		t.Fatalf("%+v", failed)
	}
}

func TestRunIsSynchronousAndShared(t *testing.T) {
	jobs := New()
	result, err := jobs.Run(func(progress Progress) (any, error) {
		progress("Working", nil, nil)
		snapshot, _ := jobs.Snapshot(nil)
		if !snapshot.Busy || snapshot.Message != "Working" || snapshot.Completed != nil {
			t.Errorf("%+v", snapshot)
		}
		return 7, nil
	})
	if err != nil || result != 7 {
		t.Fatal(result, err)
	}
	final, _ := jobs.Snapshot(nil)
	if final.State != "Completed" || string(final.Result) != "7" {
		t.Fatalf("%+v", final)
	}
}

func TestCloseRefusesNewExportsWithoutJoiningAHeldWorker(t *testing.T) {
	jobs := New()
	release := make(chan struct{})
	started, _ := jobs.Start(func(Progress) (any, error) { <-release; return nil, nil })
	closed := make(chan struct{})
	go func() { jobs.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for the worker")
	}
	if _, err := jobs.Start(nil); err == nil || err.Error() != "The application is closing. Start the map export after reopening it." {
		t.Fatal(err)
	}
	close(release)
	jobs.Wait()
	if final, _ := jobs.Snapshot(started.ID); final.State != "Completed" {
		t.Fatalf("%+v", final)
	}
}

func TestConcurrentStatusReads(t *testing.T) {
	jobs := New()
	release := make(chan struct{})
	jobs.Start(func(progress Progress) (any, error) {
		for i := range 1000 {
			progress("Step", ptr(i), ptr(1000))
		}
		<-release
		return nil, nil
	})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				jobs.Snapshot(nil)
			}
		}()
	}
	wg.Wait()
	close(release)
	jobs.Wait()
}
