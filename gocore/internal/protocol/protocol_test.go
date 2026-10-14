package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestParseMatchesPythonValidation(t *testing.T) {
	cases := []struct {
		line, id, err string
	}{
		{`{"id":1,"method":"ping","args":[]}`, `1`, ""},
		{`{"id":"日本語-id","method":"bootstrap"}`, `"日本語-id"`, ""},
		{`{"id":-7,"method":"ping"}`, `-7`, ""},
		{`{bad json`, "", "Invalid JSON request"},
		{``, "", "Invalid JSON request"},
		{`[]`, "", "Expected a request object"},
		{`null`, "", "Expected a request object"},
		{`"not an object"`, "", "Expected a request object"},
		{`{"method":"ping"}`, "", "Expected a string or integer request id"},
		{`{"id":true,"method":"ping"}`, "", "Expected a string or integer request id"},
		{`{"id":1.0,"method":"ping"}`, "", "Expected a string or integer request id"},
		{`{"id":1e3,"method":"ping"}`, "", "Expected a string or integer request id"},
		{`{"id":null,"method":"ping"}`, "", "Expected a string or integer request id"},
		{`{"id":"x","method":123}`, `"x"`, "Unknown API method"},
		{`{"id":"x","args":[]}`, `"x"`, "Unknown API method"},
		{`{"id":"x","method":"_save"}`, `"x"`, "Unknown API method"},
		{`{"id":"x","method":"__class__"}`, `"x"`, "Unknown API method"},
		{`{"id":"x","method":"scan_root","args":{}}`, `"x"`, "Expected an argument list"},
		{`{"id":"x","method":"bootstrap","args":null}`, `"x"`, "Expected an argument list"},
		{`{"id":"x","method":"bootstrap","args":"bad"}`, `"x"`, "Expected an argument list"},
		{`{"id":1,"method":"ping"}` + strings.Repeat(" ", MaxRequestChars), "", "Request too large"},
		{"{\"id\":1,\"method\":\"ping\",\"args\":[\"\xff\"]}", "", "Request is not valid UTF-8"},
	}
	for _, c := range cases {
		request, failure := Parse([]byte(c.line))
		if c.err == "" {
			if failure != nil {
				t.Errorf("%s: unexpected failure %q", c.line, failure.Message)
			} else if string(request.ID) != c.id {
				t.Errorf("%s: id %s, want %s", c.line, request.ID, c.id)
			}
			continue
		}
		if failure == nil {
			t.Errorf("%.60s: accepted, want %q", c.line, c.err)
			continue
		}
		if failure.Message != c.err || string(failure.ID) != c.id {
			t.Errorf("%.60s: got (%s, %q), want (%s, %q)", c.line, failure.ID, failure.Message, c.id, c.err)
		}
	}
}

func TestLimitCountsCharactersNotBytes(t *testing.T) {
	// 40,000 three-byte characters are 120,000 bytes but within the limit.
	line := `{"id":1,"method":"ping","args":["` + strings.Repeat("日", 40000) + `"]}`
	if _, failure := Parse([]byte(line)); failure != nil {
		t.Fatalf("rejected: %s", failure.Message)
	}
}

func TestRepliesKeepUnicodeAndNullIDs(t *testing.T) {
	line, err := Success(json.RawMessage(`"日本"`), map[string]any{"path": `C:\Héro <a&b>`})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(line), `{"id":"日本","result":{"data":{"path":"C:\\Héro <a&b>"},"ok":true}}`+"\n"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if got, want := string(Error(nil, "Unknown")), `{"id":null,"result":{"error":"Unknown","ok":false}}`+"\n"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

// The Python core stays the reference while the port is in progress: a method
// added to app.py must be added here too, or the Go core would reject it.
func TestMethodsMatchAppPy(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "..", "app.py"))
	if err != nil {
		t.Skip("app.py is not available:", err)
	}
	block := regexp.MustCompile(`(?s)API_METHODS = \((.*?)\)\n`).FindSubmatch(source)
	if block == nil {
		t.Fatal("API_METHODS was not found in app.py")
	}
	var python []string
	for _, match := range regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllSubmatch(block[1], -1) {
		python = append(python, string(match[1]))
	}
	if !slices.Equal(python, Methods) {
		t.Fatalf("Methods differ from app.py API_METHODS:\n go:     %v\n python: %v", Methods, python)
	}
}
