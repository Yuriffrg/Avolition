package toolchain

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/pycompat"
)

type pyOutcome struct {
	OK    bool            `json:"ok"`
	JSON  string          `json:"json"`
	Value json.RawMessage `json:"value"`
	Error string          `json:"error"`
	Type  string          `json:"type"`
}

const batchHelper = `
import json
def run(function, inputs, dump=True):
    out = []
    for value in inputs:
        try:
            result = function(*value) if isinstance(value, list) else function(value)
            out.append({'ok': True, 'json': json.dumps(result, ensure_ascii=False, indent=2) if dump else '', 'value': result})
        except Exception as error:
            out.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
    return out
`

func batch(t *testing.T, code string, inputs any) []pyOutcome {
	t.Helper()
	raw := oracle.Exec(t, batchHelper+code, inputs).MustValue(t)
	var outcomes []pyOutcome
	if err := json.Unmarshal(raw, &outcomes); err != nil {
		t.Fatal(err)
	}
	return outcomes
}

func TestOracleReleaseMetadata(t *testing.T) {
	valid := `{"id": 90, "tag_name": "v1.2.3", "published_at": "2026-07-11T18:12:13Z", "assets": [
		{"name": "source.zip"},
		{"id": 123, "name": "toolchain-release.zip", "size": 5, "updated_at": "2026-07-12T19:13:14Z",
		 "digest": "sha256:abc", "browser_download_url": "https://github.com/overtools/OWLib/releases/download/v1/toolchain-release.zip"}]}`
	withAsset := func(replace string) string {
		return `{"id": "rel-1", "tag_name": "Ünïcode ✓", "assets": [{"name": "toolchain-release.zip", ` + replace + `}]}`
	}
	url := `"browser_download_url": "https://github.com/overtools/OWLib/releases/download/v1/toolchain-release.zip"`
	inputs := []string{
		valid, `null`, `[]`, `"text"`, `{}`, `{"tag_name": 5}`, `{"tag_name": "  \u001c "}`, `{"tag_name": "v1"}`,
		`{"tag_name": "v1", "assets": null}`, `{"tag_name": "v1", "assets": 7}`, `{"tag_name": "v1", "assets": "toolchain-release.zip"}`,
		`{"tag_name": "v1", "assets": {"toolchain-release.zip": 1}}`,
		`{"tag_name": "v1", "assets": [{"name": "toolchain-release.zip", "size": 1}, {"name": "toolchain-release.zip", "size": 1}]}`,
		withAsset(`"size": true, ` + url), withAsset(`"size": 10.0, ` + url), withAsset(`"size": 0, ` + url),
		withAsset(`"size": -4, ` + url), withAsset(`"size": 2147483649, ` + url), withAsset(`"size": 2147483648, ` + url),
		withAsset(`"size": 99999999999999999999999999, ` + url), withAsset(`"size": "5", ` + url),
		withAsset(`"size": 5, "digest": "", "id": 1.5, "updated_at": null, ` + url),
		withAsset(`"size": 5, "digest": 0, ` + url), withAsset(`"size": 5, "digest": [1], ` + url),
		withAsset(`"size": 5`), withAsset(`"size": 5, "browser_download_url": ""`),
		withAsset(`"size": 5, "browser_download_url": "http://github.com/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://evil.example/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://user@github.com/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://:pw@github.com/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://@github.com/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://GitHub.COM:443/OVERTOOLS/owlib/Releases/Download/x?y#z"`),
		withAsset(`"size": 5, "browser_download_url": "  \tHTTPS://github.com/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://github.com/overtools/OWLib/releases/download"`),
		withAsset(`"size": 5, "browser_download_url": "https://github.com.evil/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://[github.com/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https://[::1]/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "github.com/overtools/OWLib/releases/download/x"`),
		withAsset(`"size": 5, "browser_download_url": "https:////github.com/overtools/OWLib/releases/download/x"`),
	}
	raw := make([]json.RawMessage, len(inputs))
	for i, input := range inputs {
		raw[i] = json.RawMessage("[" + input + "]")
	}
	outcomes := batch(t, "from owpreview.toolchain_runtime import release_metadata\nresult = run(release_metadata, args[0])", raw)
	for i, input := range inputs {
		want := outcomes[i]
		value, err := pycompat.Loads([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReleaseMetadata(value)
		if !want.OK {
			if err == nil || err.Error() != want.Error {
				t.Errorf("input %d %s: want %s %q, got %v", i, input, want.Type, want.Error, err)
			}
			var toolchainErr *Error
			if (want.Type == "ToolchainError") != errors.As(err, &toolchainErr) {
				t.Errorf("input %d: error type %T, want %s", i, err, want.Type)
			}
			continue
		}
		if err != nil {
			t.Errorf("input %d %s: unexpected error %v", i, input, err)
			continue
		}
		dumped, _ := pycompat.Dumps(got, 2)
		if string(dumped) != want.JSON {
			t.Errorf("input %d:\n got %s\nwant %s", i, dumped, want.JSON)
		}
	}
}

func TestOraclePackageDiffers(t *testing.T) {
	base := `{"version": "v1", "releaseId": 1, "assetId": 2, "digest": "sha256:a", "assetSize": 3, "packageUpdatedAt": "x", "installedAt": "y"}`
	pairs := [][2]string{
		{`null`, base}, {base, base}, {`{"legacy": true}`, base}, {`{"legacy": 0, "version": "v1", "releaseId": 1, "assetId": 2, "digest": "sha256:a", "assetSize": 3, "packageUpdatedAt": "x"}`, base},
		{`{"version": "v1", "releaseId": 1.0, "assetId": true, "digest": "sha256:a", "assetSize": 3, "packageUpdatedAt": "x"}`,
			`{"version": "v1", "releaseId": 1, "assetId": 1, "digest": "sha256:a", "assetSize": 3, "packageUpdatedAt": "x"}`},
		{`{}`, `{}`}, {`{"version": null}`, `{}`}, {`{"version": "v1"}`, `{}`},
		{base, `{"version": "v1", "releaseId": 1, "assetId": 2, "digest": "sha256:A", "assetSize": 3, "packageUpdatedAt": "x"}`},
		{`{"version": [1, {"a": 2}], "releaseId": {"b": 1}}`, `{"version": [1, {"a": 2}], "releaseId": {"b": 1}}`},
		{`{"version": [1, {"a": 2}]}`, `{"version": [1, {"a": 3}]}`},
	}
	var raw [][2]json.RawMessage
	for _, pair := range pairs {
		raw = append(raw, [2]json.RawMessage{json.RawMessage(pair[0]), json.RawMessage(pair[1])})
	}
	outcomes := batch(t, "from owpreview.toolchain_runtime import package_differs\nresult = run(package_differs, args[0], False)", raw)
	for i, pair := range pairs {
		a, _ := pycompat.Loads([]byte(pair[0]))
		b, _ := pycompat.Loads([]byte(pair[1]))
		var want bool
		json.Unmarshal(outcomes[i].Value, &want)
		if got := PackageDiffers(a, b); got != want {
			t.Errorf("%v: got %v want %v", pair, got, want)
		}
	}
}

func TestOracleSafeMember(t *testing.T) {
	names := []string{"a/b.txt", "a\\b", "dir/", "", "/x", "../x", "a/./b", "a//b", "C:/x", "x:y", "trail ", "trail.",
		"CON", "con.txt", "COM1", "COM0", "LPT9.log", "NUL\n", "Console", "x\x00y", "it's", "say \"hi\"", "both '\"",
		"tab\tname", "日本語/ファイル.dll", "\u200bzero", "\u00a0nbsp", "emoji 😀", "aux.", "folder/CON/file", "ſomething"}
	outcomes := batch(t, "from owpreview.toolchain_runtime import _safe_member\nresult = run(_safe_member, args[0], False)", names)
	for i, name := range names {
		want := outcomes[i]
		parts, err := SafeMember(name)
		if !want.OK {
			if err == nil || err.Error() != want.Error {
				t.Errorf("%q: want error %q, got %v", name, want.Error, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected %v", name, err)
			continue
		}
		jsoncmp.Equal(t, want.Value, parts, jsoncmp.Default)
	}
}

func TestOracleErrorMessages(t *testing.T) {
	type httpCase struct {
		Code    int               `json:"code"`
		Headers map[string]string `json:"headers"`
	}
	cases := []httpCase{
		{403, map[string]string{"X-RateLimit-Reset": "1791151200"}}, {429, map[string]string{"X-RateLimit-Reset": "0"}},
		{403, map[string]string{"X-RateLimit-Reset": "99999999999999"}}, {403, map[string]string{"X-RateLimit-Reset": "12a"}},
		{403, map[string]string{"X-RateLimit-Reset": "253402300799"}}, {403, map[string]string{"X-RateLimit-Reset": "253402300800"}},
		{403, map[string]string{}}, {404, nil}, {500, nil}, {502, nil},
	}
	code := `
from urllib.error import HTTPError, URLError
from owpreview.toolchain_runtime import _error_message
import zipfile
result = [_error_message(HTTPError('u', c['code'], 'm', c['headers'] or {}, None)) for c in args[0]]
result += [_error_message(URLError('offline')), _error_message(TimeoutError()), _error_message(ConnectionResetError()),
           _error_message(PermissionError('x')), _error_message(zipfile.BadZipFile('x')), _error_message(RuntimeError('plain')),
           _error_message(RuntimeError(''))]
`
	var want []string
	json.Unmarshal(oracle.Exec(t, code, cases).MustValue(t), &want)
	var got []string
	for _, c := range cases {
		header := http.Header{}
		for k, v := range c.Headers {
			header.Set(k, v)
		}
		got = append(got, ErrorMessage(&HTTPError{Code: c.Code, Header: header}))
	}
	got = append(got, ErrorMessage(&URLError{errors.New("offline")}), ErrorMessage(os.ErrDeadlineExceeded),
		ErrorMessage(&URLError{errors.New("connection reset")}), ErrorMessage(&os.PathError{Op: "open", Path: "x", Err: os.ErrPermission}),
		ErrorMessage(&badZipError{errors.New("x")}), ErrorMessage(errors.New("plain")), ErrorMessage(errors.New("")))
	jsoncmp.Equal(t, mustJSON(t, want), got, jsoncmp.Default)
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestOracleInstallIsReadableByPython checks byte-compatible active.json and
// installation.json, and that the Python core adopts a Go-made installation.
func TestOracleInstallIsReadableByPython(t *testing.T) {
	f := setup(t)
	f.network.release["tag_name"] = "v1 <Ünï & \u2028 \"code\">"
	asset(f.network.release)["browser_download_url"] = "https://github.com/overtools/OWLib/releases/download/v1/" + AssetName
	f.install(t)
	snapshot := f.manager.Snapshot()
	pointer := filepath.Join(f.root, "active.json")
	executable := f.manager.Executable()
	installation := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(executable))), "installation.json")
	code := `
import json
from pathlib import Path
from owpreview.toolchain_runtime import ToolchainManager
def redump(path):
    text = Path(path).read_text(encoding='utf-8')
    return text == json.dumps(json.loads(text), ensure_ascii=False, indent=2)
manager = ToolchainManager(args[0])
snapshot = manager.snapshot()
result = {'pointer': redump(args[1]), 'installation': redump(args[2]),
          'executable': snapshot['executable'], 'installed': snapshot['installed'], 'error': snapshot['error']}
`
	var got struct {
		Pointer      bool            `json:"pointer"`
		Installation bool            `json:"installation"`
		Executable   string          `json:"executable"`
		Installed    json.RawMessage `json:"installed"`
		Error        *string         `json:"error"`
	}
	json.Unmarshal(oracle.Exec(t, code, f.root, pointer, installation).MustValue(t), &got)
	if !got.Pointer || !got.Installation {
		data, _ := os.ReadFile(pointer)
		t.Fatalf("not json.dumps(indent=2) compatible: %+v\n%s", got, data)
	}
	if got.Error != nil || got.Executable != executable {
		t.Fatalf("%+v", got)
	}
	jsoncmp.Equal(t, got.Installed, snapshot.Installed, jsoncmp.Default)
	data, _ := os.ReadFile(pointer)
	if !strings.HasPrefix(string(data), "{\n  \"schema\": 1,\n  \"executable\": \"packages/") {
		t.Fatalf("%s", data)
	}
}

// TestOraclePythonInstallIsReadableByGo loads an installation made by the
// Python ToolchainManager (mocked network) in Go.
func TestOraclePythonInstallIsReadableByGo(t *testing.T) {
	root := filepath.Join(t.TempDir(), "py toolchain")
	blob := archive(t, nil)
	data, _ := json.Marshal(release(blob, "v9 ✓", 7, true))
	code := `
import io, base64
from owpreview.toolchain_runtime import ToolchainManager, RELEASE_URL
blob = base64.b64decode(args[1])
def opener(request, timeout=30):
    return io.BytesIO(args[2].encode() if request.full_url == RELEASE_URL else blob)
manager = ToolchainManager(args[0], opener=opener)
manager.install_async()
result = manager.wait(10)
`
	encoded, _ := json.Marshal(blob) // base64 string
	var blobText string
	json.Unmarshal(encoded, &blobText)
	var py Snapshot
	if err := json.Unmarshal(oracle.Exec(t, code, root, blobText, string(data)).MustValue(t), &py); err != nil {
		t.Fatal(err)
	}
	if py.Error != nil {
		t.Fatal(*py.Error)
	}
	manager := NewManager(root, Options{Opener: func(string, http.Header) (io.ReadCloser, error) { return nil, &URLError{errors.New("offline")} }})
	got := manager.Snapshot()
	if got.Error != nil || got.Executable == nil || *got.Executable != *py.Executable {
		t.Fatalf("%s", mustJSON(t, got))
	}
	jsoncmp.Equal(t, mustJSON(t, py.Installed), got.Installed, jsoncmp.Default)
	if PackageDiffers(got.Installed, py.Installed) {
		t.Fatal("same package reported as different")
	}
}
