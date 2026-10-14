package toolchain

// Port of tests/test_toolchain_runtime.py: mocked official release/download
// tests; no real game extraction is implied.

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

type entry struct {
	name  string
	value []byte
	mode  uint32 // external Unix mode; 0 for a plain entry
}

func defaultEntries() []entry {
	runtimeConfig, _ := json.Marshal(map[string]any{"runtimeOptions": map[string]any{"includedFrameworks": []any{map[string]any{"name": "Microsoft.NETCore.App"}}}})
	deps := []byte(`{"runtimeTarget": {"name": "mock/windows"}, "targets": {"mock/windows": {"DataTool/1": {"runtime": {"DataTool.dll": {}}, "native": {"runtimes/win-x64/native/dependency.dll": {}}}}}}`)
	return []entry{
		{name: "Toolchain/DataTool.exe", value: []byte("MZmock executable")},
		{name: "Toolchain/DataTool.dll", value: []byte("managed companion")},
		{name: "Toolchain/DataTool.runtimeconfig.json", value: runtimeConfig},
		{name: "Toolchain/DataTool.deps.json", value: deps},
		{name: "Toolchain/hostfxr.dll", value: []byte("host")},
		{name: "Toolchain/hostpolicy.dll", value: []byte("policy")},
		{name: "Toolchain/coreclr.dll", value: []byte("runtime")},
		{name: "Toolchain/runtimes/win-x64/native/dependency.dll", value: []byte("native companion")},
		{name: "Toolchain/help/HOW_TO_USE_A_COMMAND_LINE.txt", value: []byte("Tutorial")},
	}
}

func archive(t testing.TB, entries []entry) []byte {
	t.Helper()
	if entries == nil {
		entries = defaultEntries()
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
		if item.mode != 0 {
			header.CreatorVersion = 3 << 8
			header.ExternalAttrs = item.mode << 16
		}
		w, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(item.value)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func release(blob []byte, tag string, assetID int, digest bool) map[string]any {
	var digestValue any
	if digest {
		sum := sha256.Sum256(blob)
		digestValue = "sha256:" + hex.EncodeToString(sum[:])
	}
	return map[string]any{
		"id": 90, "tag_name": tag, "published_at": "2026-07-11T18:12:13Z",
		"assets": []any{
			map[string]any{"name": "source-code.zip", "browser_download_url": "https://invalid.example/source.zip"},
			map[string]any{"id": assetID, "name": AssetName, "size": len(blob),
				"updated_at": "2026-07-12T19:13:14Z", "digest": digestValue,
				"browser_download_url": "https://github.com/overtools/OWLib/releases/download/" + tag + "/" + AssetName},
		},
	}
}

func asset(r map[string]any) map[string]any { return r["assets"].([]any)[1].(map[string]any) }

type mockNetwork struct {
	mu          sync.Mutex
	blob        []byte
	release     map[string]any
	requests    []string
	headers     []http.Header
	err         error
	downloadErr error
}

func newNetwork(t testing.TB, blob []byte) *mockNetwork {
	if blob == nil {
		blob = archive(t, nil)
	}
	return &mockNetwork{blob: blob, release: release(blob, "v1.2.3", 123, true)}
}

func (n *mockNetwork) open(url string, header http.Header) (io.ReadCloser, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.requests = append(n.requests, url)
	n.headers = append(n.headers, header)
	if url == ReleaseURL {
		if n.err != nil {
			return nil, n.err
		}
		data, _ := json.Marshal(n.release)
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	if n.downloadErr != nil {
		return nil, n.downloadErr
	}
	return io.NopCloser(bytes.NewReader(n.blob)), nil
}

type fixture struct {
	root    string
	network *mockNetwork
	manager *Manager
}

func setup(t *testing.T) *fixture {
	root := filepath.Join(t.TempDir(), "Whitehound with spaces 漢字", "toolchain")
	network := newNetwork(t, nil)
	return &fixture{root: root, network: network, manager: NewManager(root, Options{Opener: network.open})}
}

func (f *fixture) install(t *testing.T) Snapshot {
	t.Helper()
	if _, err := f.manager.InstallAsync(); err != nil {
		t.Fatal(err)
	}
	result := f.manager.Wait(5 * time.Second)
	if result.Busy {
		t.Fatal("still busy")
	}
	if result.Error != nil {
		t.Fatalf("install failed: %s", *result.Error)
	}
	return result
}

func get(p *Package, key string) any {
	value, _ := p.Get(key)
	return value
}

func errText(s Snapshot) string {
	if s.Error == nil {
		return ""
	}
	return *s.Error
}

func asJSON(t testing.TB, value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFreshStatusDoesNotCreateDirectoriesOrFakeDates(t *testing.T) {
	f := setup(t)
	value := f.manager.Snapshot()
	if value.Installed != nil || value.Latest != nil || value.UpdateAvailable != nil || value.Executable != nil {
		t.Fatalf("%+v", value)
	}
	if pycompat.Exists(f.root) {
		t.Fatal("root created")
	}
	data := asJSON(t, value)
	for _, key := range []string{`"installed":null`, `"latest":null`, `"updateAvailable":null`, `"executable":null`, `"state":"ready"`} {
		if !strings.Contains(data, key) {
			t.Fatalf("%s missing in %s", key, data)
		}
	}
}

func TestCheckOnlyAndExplicitInstallKeepInstalledMetadataDistinct(t *testing.T) {
	f := setup(t)
	f.manager.CheckAsync()
	checked := f.manager.Wait(5 * time.Second)
	if checked.Installed != nil || get(checked.Latest, "version") != "v1.2.3" || checked.UpdateAvailable == nil || !*checked.UpdateAvailable {
		t.Fatalf("%s", asJSON(t, checked))
	}
	if pycompat.Exists(f.root) {
		t.Fatal("check created the root")
	}
	if got := f.network.headers[0]; got.Get("User-Agent") != UserAgent || got.Get("Accept") != "application/vnd.github+json" || got.Get("X-GitHub-Api-Version") != "2022-11-28" {
		t.Fatalf("headers %v", got)
	}
	installed := f.install(t).Installed
	if get(installed, "releaseDate") != "2026-07-11T18:12:13Z" || get(installed, "packageUpdatedAt") != "2026-07-12T19:13:14Z" {
		t.Fatal(asJSON(t, installed))
	}
	installedAt, _ := get(installed, "installedAt").(string)
	if installedAt == "" || !strings.HasSuffix(installedAt, "Z") || installedAt == get(installed, "releaseDate") {
		t.Fatalf("installedAt %q", installedAt)
	}
	if get(installed, "digestVerified") != true {
		t.Fatal("digest not verified")
	}
	f.network.release = release(f.network.blob, "v2.0", 456, true)
	f.manager.CheckAsync()
	value := f.manager.Wait(5 * time.Second)
	if asJSON(t, value.Installed) != asJSON(t, installed) || get(value.Latest, "version") != "v2.0" || !*value.UpdateAvailable {
		t.Fatal(asJSON(t, value))
	}
}

func TestFullPackageStructureAndZipPreservedAndReloadWorksOffline(t *testing.T) {
	f := setup(t)
	f.install(t)
	executable := f.manager.Executable()
	if filepath.Base(executable) != "DataTool.exe" || !pycompat.IsRelativeTo(executable, f.manager.Root()) {
		t.Fatal(executable)
	}
	folder := filepath.Dir(executable)
	if data, _ := os.ReadFile(filepath.Join(folder, "DataTool.dll")); string(data) != "managed companion" {
		t.Fatal("dll")
	}
	if data, _ := os.ReadFile(filepath.Join(folder, "runtimes/win-x64/native/dependency.dll")); string(data) != "native companion" {
		t.Fatal("native")
	}
	pkg := filepath.Dir(filepath.Dir(folder))
	if data, _ := os.ReadFile(filepath.Join(pkg, AssetName)); !bytes.Equal(data, f.network.blob) {
		t.Fatal("zip not preserved")
	}
	if !pycompat.IsFile(filepath.Join(pkg, "installation.json")) {
		t.Fatal("installation.json")
	}
	f.network.err = &URLError{errors.New("offline")}
	reloaded := NewManager(f.root, Options{Opener: f.network.open})
	if reloaded.Executable() != executable {
		t.Fatal(reloaded.Executable())
	}
	if asJSON(t, reloaded.Snapshot().Installed) != asJSON(t, f.manager.Snapshot().Installed) {
		t.Fatal("installed differs after reload")
	}
	reloaded.CheckAsync()
	value := reloaded.Wait(5 * time.Second)
	if !strings.Contains(errText(value), "internet connection") || value.UpdateAvailable != nil || reloaded.Executable() != executable {
		t.Fatal(asJSON(t, value))
	}
}

func TestSameTagReplacementIsAnUpdate(t *testing.T) {
	f := setup(t)
	original := f.install(t).Installed
	with := func(key string, value any) *Package {
		clone := clonePackage(original)
		clone.Set(key, value)
		return clone
	}
	if !PackageDiffers(original, with("assetId", 999)) || !PackageDiffers(original, with("digest", "sha256:replacement")) ||
		!PackageDiffers(original, with("packageUpdatedAt", "2026-09-01T00:00:00Z")) {
		t.Fatal("replacement not detected")
	}
	if PackageDiffers(original, with("installedAt", "arbitrary local time")) {
		t.Fatal("installedAt must not count")
	}
}

func TestLegacyRuntimeIsUsedWithoutInventingReleaseMetadata(t *testing.T) {
	f := setup(t)
	os.MkdirAll(f.root, 0o777)
	executable := filepath.Join(f.root, "DataTool.exe")
	os.WriteFile(executable, []byte("MZlegacy"), 0o666)
	manager := NewManager(f.root, Options{Opener: f.network.open})
	if manager.Executable() != executable {
		t.Fatal(manager.Executable())
	}
	installed := manager.Snapshot().Installed
	if get(installed, "legacy") != true {
		t.Fatal("not legacy")
	}
	for _, key := range []string{"version", "releaseDate", "packageUpdatedAt", "installedAt", "digest"} {
		if value, ok := installed.Get(key); !ok || value != nil {
			t.Fatalf("%s = %v", key, value)
		}
	}
	if got := asJSON(t, installed); got != `{"version":null,"releaseDate":null,"packageUpdatedAt":null,"installedAt":null,"assetId":null,"digest":null,"legacy":true}` {
		t.Fatal(got)
	}
	manager.InstallAsync()
	if value := manager.Wait(5 * time.Second); value.Error != nil {
		t.Fatal(*value.Error)
	}
	if !pycompat.Exists(executable) || manager.Executable() == executable {
		t.Fatal("legacy runtime replaced or still active")
	}
}

func TestAbandonedStagingAndInactivePackagesAreNeverAdopted(t *testing.T) {
	f := setup(t)
	for _, folder := range []string{".staging/abandoned", "packages/unpublished"} {
		path := filepath.Join(f.root, folder)
		os.MkdirAll(path, 0o777)
		os.WriteFile(filepath.Join(path, "DataTool.exe"), []byte("MZorphan"), 0o666)
	}
	if manager := NewManager(f.root, Options{Opener: f.network.open}); manager.Executable() != "" {
		t.Fatal(manager.Executable())
	}
}

func listDir(t *testing.T, path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestDigestFailurePreservesPreviousPointerAndRuntime(t *testing.T) {
	f := setup(t)
	f.install(t)
	before, _ := os.ReadFile(filepath.Join(f.root, "active.json"))
	executable := f.manager.Executable()
	f.network.release = release(f.network.blob, "v2", 123, true)
	asset(f.network.release)["digest"] = "sha256:" + strings.Repeat("0", 64)
	f.manager.CheckAsync()
	f.manager.Wait(5 * time.Second)
	f.manager.InstallAsync()
	value := f.manager.Wait(5 * time.Second)
	if !strings.Contains(errText(value), "digest does not match") {
		t.Fatal(errText(value))
	}
	after, _ := os.ReadFile(filepath.Join(f.root, "active.json"))
	if !bytes.Equal(before, after) || f.manager.Executable() != executable {
		t.Fatal("previous installation changed")
	}
	if len(listDir(t, filepath.Join(f.root, "packages"))) != 1 || len(listDir(t, filepath.Join(f.root, ".staging"))) != 0 {
		t.Fatal("leftover folders")
	}
}

func TestFailedAtomicActivationPreservesOldInstallation(t *testing.T) {
	f := setup(t)
	f.install(t)
	previous, _ := os.ReadFile(filepath.Join(f.root, "active.json"))
	f.manager.rename = func(oldpath, newpath string) error {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: fs.ErrPermission}
	}
	f.manager.InstallAsync()
	value := f.manager.Wait(5 * time.Second)
	if !strings.Contains(errText(value), "permissions") {
		t.Fatal(errText(value))
	}
	after, _ := os.ReadFile(filepath.Join(f.root, "active.json"))
	if !bytes.Equal(previous, after) || !pycompat.IsFile(f.manager.Executable()) {
		t.Fatal("old installation changed")
	}
	if len(listDir(t, filepath.Join(f.root, "packages"))) != 1 {
		t.Fatal("packages")
	}
}

func TestMissingAssetRateLimitOfflineAndCorruptZipAreActionable(t *testing.T) {
	f := setup(t)
	cases := []struct {
		kind string
		err  error
		text string
	}{
		{"missing", nil, "toolchain-release.zip"},
		{"rate", &HTTPError{URL: ReleaseURL, Code: 403, Header: http.Header{"X-Ratelimit-Reset": {"1791151200"}}}, "rate limit"},
		{"offline", &URLError{errors.New("disconnected")}, "internet connection"},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			network := newNetwork(t, nil)
			network.err = c.err
			if c.kind == "missing" {
				network.release["assets"] = []any{}
			}
			manager := NewManager(f.root, Options{Opener: network.open})
			manager.CheckAsync()
			if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, c.text) {
				t.Fatal(got)
			}
		})
	}
	network := newNetwork(t, []byte("not a zip file"))
	manager := NewManager(f.root, Options{Opener: network.open})
	manager.InstallAsync()
	if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "not a valid ZIP") {
		t.Fatal(got)
	}
}

func TestMissingDigestIsExplicitAndDoesNotFabricateVerification(t *testing.T) {
	f := setup(t)
	f.network.release = release(f.network.blob, "v1.2.3", 123, false)
	value := f.install(t)
	if get(value.Installed, "digestVerified") != false || get(value.Installed, "digest") != nil {
		t.Fatal(asJSON(t, value.Installed))
	}
	if !strings.Contains(value.Message, "did not provide a digest") {
		t.Fatal(value.Message)
	}
}

func TestIncompleteDownloadAndUnsupportedDigestAreRejected(t *testing.T) {
	f := setup(t)
	f.network.blob = f.network.blob[:len(f.network.blob)-10]
	f.manager.InstallAsync()
	if got := errText(f.manager.Wait(5 * time.Second)); !strings.Contains(got, "incomplete") {
		t.Fatal(got)
	}
	network := newNetwork(t, nil)
	asset(network.release)["digest"] = "md5:" + strings.Repeat("0", 32)
	manager := NewManager(f.root, Options{Opener: network.open})
	manager.InstallAsync()
	if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "unsupported or invalid package digest") {
		t.Fatal(got)
	}
}

func TestRejectsTraversalWindowsAliasesAndSymlinkEntries(t *testing.T) {
	f := setup(t)
	names := []string{"../escape.txt", "/absolute.txt", "C:/escape.txt", `..\escape.txt`,
		`\\server\share.txt`, "folder/../escape.txt", "file:stream",
		"CON.txt", "folder./DataTool.exe", "folder /DataTool.exe"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			blob := archive(t, []entry{{name: "DataTool.exe", value: []byte("MZvalid")}, {name: name, value: []byte("evil")}})
			manager := NewManager(f.root, Options{Opener: newNetwork(t, blob).open})
			manager.InstallAsync()
			if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "unsafe path") {
				t.Fatal(got)
			}
			if manager.Executable() != "" {
				t.Fatal("installed")
			}
		})
	}
	blob := archive(t, []entry{{name: "DataTool.exe", value: []byte("MZvalid")}, {name: "link", value: []byte("../elsewhere"), mode: 0o120777}})
	manager := NewManager(f.root, Options{Opener: newNetwork(t, blob).open})
	manager.InstallAsync()
	if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "symbolic links") {
		t.Fatal(got)
	}
}

func TestCaseInsensitiveDuplicatePathsAreRejected(t *testing.T) {
	f := setup(t)
	blob := archive(t, []entry{{name: "DataTool.exe", value: []byte("MZone")}, {name: "datatool.exe", value: []byte("MZtwo")}})
	manager := NewManager(f.root, Options{Opener: newNetwork(t, blob).open})
	manager.InstallAsync()
	if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "duplicate") {
		t.Fatal(got)
	}
}

func TestExpectedRuntimeMustBeUniqueAndAWindowsExecutable(t *testing.T) {
	f := setup(t)
	for _, entries := range [][]entry{
		{{name: "Other.exe", value: []byte("MZother")}},
		{{name: "DataTool.exe", value: []byte("not executable")}},
		{{name: "one/DataTool.exe", value: []byte("MZone")}, {name: "two/DataTool.exe", value: []byte("MZtwo")}},
	} {
		manager := NewManager(f.root, Options{Opener: newNetwork(t, archive(t, entries)).open})
		manager.InstallAsync()
		if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "DataTool") {
			t.Fatal(got)
		}
		if manager.Executable() != "" {
			t.Fatal("installed")
		}
	}
}

func TestIncompleteRuntimeAndManifestDependenciesAreRejected(t *testing.T) {
	f := setup(t)
	for _, omitted := range []string{"Toolchain/DataTool.dll", "Toolchain/coreclr.dll", "Toolchain/runtimes/win-x64/native/dependency.dll"} {
		t.Run(omitted, func(t *testing.T) {
			var entries []entry
			for _, item := range defaultEntries() {
				if item.name != omitted {
					entries = append(entries, item)
				}
			}
			manager := NewManager(f.root, Options{Opener: newNetwork(t, archive(t, entries)).open})
			manager.InstallAsync()
			if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "runtime is incomplete") {
				t.Fatal(got)
			}
			if manager.Executable() != "" {
				t.Fatal("installed")
			}
		})
	}
	entries := defaultEntries()
	for i := range entries {
		if entries[i].name == "Toolchain/DataTool.deps.json" {
			entries[i].value = []byte("corrupt json")
		}
	}
	manager := NewManager(f.root, Options{Opener: newNetwork(t, archive(t, entries)).open})
	manager.InstallAsync()
	if got := errText(manager.Wait(5 * time.Second)); !strings.Contains(got, "invalid .NET dependency") {
		t.Fatal(got)
	}
}

func TestCheckFailureKeepsLatestSeparateButStatusUnverified(t *testing.T) {
	f := setup(t)
	f.install(t)
	latest := asJSON(t, f.manager.Snapshot().Latest)
	f.network.err = &URLError{errors.New("offline")}
	f.manager.CheckAsync()
	value := f.manager.Wait(5 * time.Second)
	if asJSON(t, value.Latest) != latest || value.UpdateAvailable != nil || value.CheckError == nil || *value.CheckError == "" {
		t.Fatal(asJSON(t, value))
	}
}

func TestUpdateCannotStartWhileExportHoldsSharedLock(t *testing.T) {
	f := setup(t)
	lock := &sync.Mutex{}
	manager := NewManager(f.root, Options{Opener: f.network.open, ActivityLock: lock})
	lock.Lock()
	defer lock.Unlock()
	if _, err := manager.InstallAsync(); err == nil || !strings.Contains(err.Error(), "active DataTool job") {
		t.Fatal(err)
	} else if _, ok := err.(*Error); !ok {
		t.Fatalf("%T", err)
	}
	manager.CheckAsync()
	if value := manager.Wait(5 * time.Second); value.Error != nil {
		t.Fatal(*value.Error)
	}
}

func TestInstallIsAsyncPreventsDuplicatesAndHoldsJobLock(t *testing.T) {
	f := setup(t)
	started := make(chan struct{}, 1)
	finish := make(chan struct{})
	lock := &sync.Mutex{}
	delayed := func(url string, header http.Header) (io.ReadCloser, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-finish:
		case <-time.After(5 * time.Second):
		}
		return f.network.open(url, header)
	}
	manager := NewManager(f.root, Options{Opener: delayed, ActivityLock: lock})
	manager.InstallAsync()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("not started")
	}
	if !manager.Snapshot().Busy {
		t.Fatal("not busy")
	}
	if lock.TryLock() {
		t.Fatal("lock not held")
	}
	if _, err := manager.InstallAsync(); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatal(err)
	}
	manager.Close()
	close(finish)
	value := manager.Wait(5 * time.Second)
	if !strings.Contains(errText(value), "interrupted") || value.Busy {
		t.Fatal(asJSON(t, value))
	}
	if !lock.TryLock() {
		t.Fatal("lock not released")
	}
	lock.Unlock()
	if manager.Executable() != "" {
		t.Fatal("installed")
	}
}

func TestMetadataOnlyAcceptsExactAssetNameAndOfficialAssetURL(t *testing.T) {
	f := setup(t)
	decode := func() any {
		data, _ := json.Marshal(f.network.release)
		value, err := pycompat.Loads(data)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	metadata, err := ReleaseMetadata(decode())
	if err != nil || get(metadata, "assetName") != AssetName {
		t.Fatal(err)
	}
	asset(f.network.release)["browser_download_url"] = "https://evil.example/toolchain-release.zip"
	if _, err := ReleaseMetadata(decode()); err == nil || !strings.Contains(err.Error(), "unexpected download URL") {
		t.Fatal(err)
	}
}

func TestActivePointerCannotEscapeToolchainRoot(t *testing.T) {
	f := setup(t)
	os.MkdirAll(f.root, 0o777)
	os.WriteFile(filepath.Join(filepath.Dir(f.root), "DataTool.exe"), []byte("MZoutside"), 0o666)
	os.WriteFile(filepath.Join(f.root, "active.json"), []byte(`{"executable": "../DataTool.exe", "package": {}}`), 0o666)
	manager := NewManager(f.root, Options{Opener: f.network.open})
	if manager.Executable() != "" || !strings.Contains(errText(manager.Snapshot()), "could not be loaded") {
		t.Fatal(asJSON(t, manager.Snapshot()))
	}
	// A schema-valid pointer that escapes is rejected by the containment check.
	os.WriteFile(filepath.Join(f.root, "active.json"), []byte(`{"schema": 1, "executable": "../DataTool.exe", "package": {}}`), 0o666)
	manager = NewManager(f.root, Options{Opener: f.network.open})
	if manager.Executable() != "" || !strings.Contains(errText(manager.Snapshot()), "unsafe path") {
		t.Fatal(errText(manager.Snapshot()))
	}
}

func TestUtcNowFormat(t *testing.T) {
	defer func() { now = time.Now }()
	now = func() time.Time { return time.Date(2026, 7, 1, 12, 3, 4, 5000, time.FixedZone("x", 3600)) }
	if got := utcNow(); got != "2026-07-01T11:03:04.000005Z" {
		t.Fatal(got)
	}
	now = func() time.Time { return time.Date(2026, 7, 1, 12, 3, 4, 0, time.UTC) }
	if got := utcNow(); got != "2026-07-01T12:03:04Z" {
		t.Fatal(got)
	}
}

func TestSnapshotKeyOrder(t *testing.T) {
	f := setup(t)
	f.install(t)
	data := asJSON(t, f.manager.Snapshot())
	keys := []string{`"root"`, `"installed"`, `"latest"`, `"executable"`, `"updateAvailable"`, `"checkedAt"`, `"state"`, `"busy"`, `"error"`, `"checkError"`, `"message"`}
	last := -1
	for _, key := range keys {
		index := strings.Index(data, key)
		if index < last {
			t.Fatalf("%s out of order in %s", key, data)
		}
		last = index
	}
	var installed ordered.Map[any]
	json.Unmarshal([]byte(asJSON(t, f.manager.Snapshot().Installed)), &installed)
	if got := strings.Join(installed.Keys(), ","); got != "version,releaseId,releaseDate,packageUpdatedAt,installedAt,assetId,assetName,assetSize,digest,downloadUrl,digestVerified" {
		t.Fatal(got)
	}
}
