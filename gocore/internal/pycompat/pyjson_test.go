package pycompat

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"avolition/gocore/internal/oracle"
)

// TestLoadsTextMatchesPython compares values and error messages with CPython.
func TestLoadsTextMatchesPython(t *testing.T) {
	if _, err := exec.LookPath(oracle.Interpreter()); err != nil {
		t.Skip("python3 unavailable")
	}
	cases := []string{
		`{"a": not`, `{"incomplete":`, ``, `[1,]`, `{"a" 1}`, `"abc`, `{"a":1} x`, `{1:2}`,
		"\"a\x01\"", `"\q"`, `{"a":1,}`, `{"a":1,"b"}`, `[1 2]`, `{"a":1 "b":2}`, `   `, "\n\n  [1,\n 2,]",
		`"\u12"`, `"\u12g4x"`, "\"\\ud83d\\ude00\"", `-`, `-x`, `01`, `1.`, `1e`, `1e+`, `1.5e3`, `[NaN]`, `[-Infinity]`,
		`{"a":{"b":[1,{"c":null}]},"a":true}`, "\ufeff{}", `[`, `{`, `{"a"`, `{"a":`, `[1,`, `"\`, `"\u`, `tru`, `nul`,
		`{"k":"v","k2":[1.0,2e5,-0,3.25]}`, `{ }`, `[ ]`, `{"a" : 1 , "b" : [ ] }`, "{\"é\": \"日本語\"} ]",
	}
	code := `import json,sys
out=[]
for s in json.load(sys.stdin):
  try:
    v=json.loads(s); out.append({"ok":True,"v":None if "NaN" in s or "Inf" in s else v})
  except Exception as e: out.append({"ok":False,"e":str(e)})
print(json.dumps(out))`
	input, _ := json.Marshal(cases)
	cmd := exec.Command(oracle.Interpreter(), "-X", "utf8", "-c", code)
	cmd.Stdin = strings.NewReader(string(input))
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var wants []struct {
		OK bool
		V  any
		E  string
	}
	if err := json.Unmarshal(out, &wants); err != nil {
		t.Fatal(err)
	}
	for i, text := range cases {
		want := wants[i]
		value, err := LoadsText(text)
		if !want.OK {
			if err == nil || err.Error() != want.E {
				t.Errorf("%q: got %v, want error %q", text, err, want.E)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", text, err)
			continue
		}
		got, _ := Dumps(value, -1)
		var a any
		if strings.Contains(text, "NaN") || strings.Contains(text, "Infinity") {
			continue
		}
		_ = json.Unmarshal(got, &a)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(want.V)
		if string(ja) != string(jb) {
			t.Errorf("%q: got %s want %s", text, ja, jb)
		}
	}
}

func TestDecodeUTF8Strict(t *testing.T) {
	for data, want := range map[string]string{
		"\xff":       "'utf-8' codec can't decode byte 0xff in position 0: invalid start byte",
		"ab\xc3":     "'utf-8' codec can't decode byte 0xc3 in position 2: unexpected end of data",
		"\xe6\x97":   "'utf-8' codec can't decode bytes in position 0-1: unexpected end of data",
		"\xc3(":      "'utf-8' codec can't decode byte 0xc3 in position 0: invalid continuation byte",
		"\xe6\x97(x": "'utf-8' codec can't decode bytes in position 0-1: invalid continuation byte",
	} {
		if _, err := DecodeUTF8Strict([]byte(data)); err == nil || err.Error() != want {
			t.Errorf("%q: %v", data, err)
		}
	}
}
