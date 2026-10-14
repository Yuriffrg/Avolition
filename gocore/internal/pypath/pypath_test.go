package pypath

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"avolition/gocore/internal/oracle"
)

func TestMatchesPathlib(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cases are POSIX paths")
	}
	dir := t.TempDir()
	// real/ with a symlink back up and a link to a sibling: resolve must
	// follow links before applying "..", unlike a lexical clean.
	must(t, os.MkdirAll(filepath.Join(dir, "real", "deep", "er"), 0o755))
	must(t, os.Symlink(filepath.Join(dir, "real", "deep", "er"), filepath.Join(dir, "link")))
	must(t, os.Symlink("real/deep", filepath.Join(dir, "rel")))
	must(t, os.Symlink("loop", filepath.Join(dir, "loop")))
	names := []string{"", ".", "..", "a", "a.", ".a", "a.b.", "a.b", "a..b", "...", "x/..", "a//b/./c/",
		"/", "/a/b.owmdl", "dir/file.tar.gz", "~", "~/x"}
	pure := `
from pathlib import PurePath, Path
result = []
for name in args[0]:
    p = PurePath(name)
    result.append([p.name, p.stem, p.suffix, str(p.parent), str(p), [str(x) for x in p.parents]])
`
	var want [][]any
	must(t, json.Unmarshal(oracle.Exec(t, pure, names).MustValue(t), &want))
	for i, name := range names {
		parents := []any{}
		for _, parent := range Parents(name) {
			parents = append(parents, parent)
		}
		got := []any{Name(name), Stem(name), Suffix(name), Parent(name), Clean(name), parents}
		a, _ := json.Marshal(want[i])
		b, _ := json.Marshal(got)
		if string(a) != string(b) {
			t.Errorf("%q: pathlib %s, pypath %s", name, a, b)
		}
	}
	joins := [][]string{{"/a/b", "../c"}, {"/a/b", "/x"}, {"a", ""}, {"/a/", "b//c"}, {".", "x"}}
	var joined []string
	must(t, json.Unmarshal(oracle.Exec(t, "from pathlib import PurePath\nresult = [str(PurePath(a) / b) for a, b in args[0]]", joins).MustValue(t), &joined))
	for i, pair := range joins {
		if got := Join(pair[0], pair[1]); got != joined[i] {
			t.Errorf("Join%q = %q, want %q", pair, got, joined[i])
		}
	}
	resolves := []string{
		filepath.Join(dir, "link", "..", "x"), filepath.Join(dir, "rel", "..", "deep", "er"),
		filepath.Join(dir, "missing", "..", "y"), filepath.Join(dir, "loop", "z"), "relative/../thing", "~/a",
		filepath.Join(dir, "real", ".", "deep", "", "er", "..", ".."),
	}
	var resolved []string
	must(t, json.Unmarshal(oracle.Exec(t, "from pathlib import Path\nresult = [str(Path(p).expanduser().resolve()) for p in args[0]]", resolves).MustValue(t), &resolved))
	for i, p := range resolves {
		if got := Resolve(p); got != resolved[i] {
			t.Errorf("Resolve(%q) = %q, want %q", p, got, resolved[i])
		}
	}
	if !IsRelativeTo("/a/b/c", "/a/b") || IsRelativeTo("/a/bc", "/a/b") || !IsRelativeTo("/a", "/a") {
		t.Error("IsRelativeTo")
	}
	if got := WithSuffix("/a/b.png", ".dds"); got != "/a/b.dds" {
		t.Errorf("WithSuffix = %q", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
