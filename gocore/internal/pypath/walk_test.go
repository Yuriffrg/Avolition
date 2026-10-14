package pypath

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWalkPreOrderSkipsSymlinkedDirectories(t *testing.T) {
	root := Resolve(t.TempDir())
	for _, dir := range []string{"a/b", "c"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"top.txt", "a/one.txt", "a/b/two.txt", "c/three.txt"} {
		if err := os.WriteFile(filepath.Join(root, file), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "a"), filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	visited := map[string][]string{}
	var order []string
	Walk(root, nil, func(dir string, dirs *[]string, files []string) bool {
		order = append(order, dir)
		names := append(append([]string{}, *dirs...), files...)
		slices.Sort(names)
		visited[dir] = names
		if dir == root {
			// Pruning, as os.walk callers do with subdirs[:] = ...
			*dirs = slices.DeleteFunc(*dirs, func(name string) bool { return name == "c" })
		}
		return true
	})
	if len(order) != 3 || order[0] != root {
		t.Fatalf("order %v", order)
	}
	// The symlinked directory is listed as a directory but not entered.
	if !slices.Equal(visited[root], []string{"a", "c", "link", "top.txt"}) {
		t.Fatalf("root entries %v", visited[root])
	}
	if _, entered := visited[filepath.Join(root, "link")]; entered {
		t.Fatal("followed a directory symlink")
	}
	if _, entered := visited[filepath.Join(root, "c")]; entered {
		t.Fatal("pruned directory visited")
	}
	var errs []error
	Walk(filepath.Join(root, "missing"), func(err error) { errs = append(errs, err) }, func(string, *[]string, []string) bool {
		t.Fatal("visited a missing directory")
		return true
	})
	if len(errs) != 1 {
		t.Fatalf("errors %v", errs)
	}
}

func TestResolveCacheMatchesResolve(t *testing.T) {
	root := Resolve(t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "real/sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "alias")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	var cache ResolveCache
	for _, p := range []string{
		filepath.Join(root, "alias/sub/x.txt"), filepath.Join(root, "alias/../alias/sub"),
		filepath.Join(root, "missing/../real"), root, filepath.Join(root, "alias/sub/x.txt"),
	} {
		if got, want := cache.Resolve(p), Resolve(p); got != want {
			t.Errorf("Resolve(%q): cache %q, direct %q", p, got, want)
		}
	}
}
