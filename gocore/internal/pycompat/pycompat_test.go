package pycompat

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
)

func execStrings(t *testing.T, code string, args ...any) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal(oracle.Exec(t, code, args...).MustValue(t), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func compareStrings(t *testing.T, inputs []string, want []string, got func(string) string) {
	t.Helper()
	for i, input := range inputs {
		if value := got(input); value != want[i] {
			t.Errorf("%q: got %q want %q", input, value, want[i])
		}
	}
}

var jsonSamples = []string{
	`null`, `true`, `false`, `0`, `-0`, `12345678901234567890123`, `1.5`, `10.0`, `1e3`, `1E-7`, `0.0001`, `123456789012345678.0`,
	`1e16`, `1e15`, `-2.5e-5`, `3.141592653589793`, `"plain"`, `"quote ' and \" both"`, `"it's"`, `"tab\tnew\nline\r"`,
	`"\u0000\u0001\u001f\u007f\u0080\u00a0\u00ad\u00e9"`, `"\u2028\u2029 \ufeff \ud83d\ude00 漢字"`, `"back\\slash"`, `"<&>"`,
	`[]`, `{}`, `[1, "a", [2, {"b": null}]]`, `{"z": 1, "a": [true, false], "m": {"x": "y", "": 0}}`, `{"dup": 1, "other": 2, "dup": 3}`,
}

func TestOracleJSONRoundTrip(t *testing.T) {
	inputs := make([]json.RawMessage, len(jsonSamples))
	for i, s := range jsonSamples {
		inputs[i] = json.RawMessage(s)
	}
	code := `
import json
result = []
for value in args[0]:
    result += [json.dumps(value, ensure_ascii=False, indent=2), json.dumps(value, ensure_ascii=False), repr(value), str(value)]
`
	want := execStrings(t, code, inputs)
	for i, sample := range jsonSamples {
		value, err := Loads([]byte(sample))
		if err != nil {
			t.Fatalf("%s: %v", sample, err)
		}
		indented, _ := Dumps(value, 2)
		compact, _ := Dumps(value, -1)
		for j, got := range []string{string(indented), string(compact), Repr(value), Str(value)} {
			if got != want[4*i+j] {
				t.Errorf("%s [%d]: got %q want %q", sample, j, got, want[4*i+j])
			}
		}
	}
}

func TestLoadsRejectsWhatPythonRejects(t *testing.T) {
	for _, bad := range []string{"", "  ", "[1,]", "{\"a\" 1}", "[1] [2]", "\xef\xbb\xbf{}", "{'a': 1}"} {
		if _, err := Loads([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

var textSamples = []string{
	"", "  a b  ", "\x1c\x1dx\x1e\x1f", "\u00a0\u2003x\u3000", "\u200bx\u200b", "Lúcio", "STRASSE straße ẞ", "ΣΑΣ σας ς",
	"İstanbul", "ﬁne ﬂow", "ǅ ǈ ǋ", "Ⅻ ⓐ", "ᏸ", "a\nb\r\nc\rd\x0be\x0cf\x1cg\u0085h\u2028i\u2029j", "trailing\n", "\n\n", "x\r",
}

func TestOracleStringFunctions(t *testing.T) {
	code := `
result = []
for s in args[0]:
    result += [s.strip(), s.casefold(), '\x00'.join(s.splitlines()), s.lower()[:3]]
`
	want := execStrings(t, code, textSamples)
	for i, s := range textSamples {
		lines := SplitLines(s)
		joined := ""
		for k, line := range lines {
			if k > 0 {
				joined += "\x00"
			}
			joined += line
		}
		for j, got := range []string{Strip(s), Casefold(s), joined} {
			if got != want[4*i+j] {
				t.Errorf("%q [%d]: got %q want %q", s, j, got, want[4*i+j])
			}
		}
	}
}

func TestOracleDecodeUTF8(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	samples := [][]byte{[]byte("plain"), {0xe2, 0x82}, {0xe2, 0x82, 0x41}, {0xf0, 0x9f, 0x98}, {0xed, 0xa0, 0x80}, {0xc0, 0xaf},
		{0xf4, 0x90, 0x80, 0x80}, {0xff, 0xfe, 'a'}, {0xe0, 0x80, 0x80}, {0xf0, 0x80}, []byte("é\x80漢")}
	for i := 0; i < 200; i++ {
		sample := make([]byte, random.Intn(12))
		for j := range sample {
			sample[j] = byte(random.Intn(256))
			if random.Intn(3) == 0 {
				sample[j] = 0x80 + byte(random.Intn(0x40))
			}
		}
		samples = append(samples, sample)
	}
	want := execStrings(t, "import base64\nresult = [base64.b64decode(s).decode('utf-8', 'replace') for s in args[0]]", samples)
	for i, sample := range samples {
		if got := DecodeUTF8(sample); got != want[i] {
			t.Errorf("% x: got %q want %q", sample, got, want[i])
		}
	}
}

func TestOracleExpandVars(t *testing.T) {
	t.Setenv("AVOL_VAR", "/value dir")
	t.Setenv("AVOL_EMPTY", "")
	t.Setenv("AVOL-DASH", "dash")
	inputs := []string{"plain", "$AVOL_VAR/x", "${AVOL_VAR}/x", "$AVOL_MISSING/x", "${AVOL_MISSING}", "$", "$$AVOL_VAR",
		"${}", "${AVOL_VAR", "%AVOL_VAR%\\x", "%AVOL_MISSING%", "%%", "100%", "'$AVOL_VAR'", "'unterminated $AVOL_VAR",
		"$AVOL-DASH", "$AVOL_EMPTY-x", "a$AVOL_VAR$AVOL_VAR", "${AVOL_VAR}${AVOL_EMPTY}", "$1abc", "日本$AVOL_VAR語", "%AVOL_VAR%%AVOL_VAR%"}
	code := "import posixpath, ntpath\nresult = [posixpath.expandvars(s) for s in args[0]] + [ntpath.expandvars(s) for s in args[0]]"
	want := execStrings(t, code, inputs)
	compareStrings(t, inputs, want[:len(inputs)], posixExpandVars)
	compareStrings(t, inputs, want[len(inputs):], ntExpandVars)
}

func withWindowsPaths(t *testing.T) {
	previous := windowsPaths
	windowsPaths = true
	t.Cleanup(func() { windowsPaths = previous })
}

func TestOracleWindowsPurePaths(t *testing.T) {
	inputs := []string{`C:\Games\Overwatch`, `C:/Games//Overwatch/./_retail_/`, `c:relative`, `\rooted`, `relative\path`, ``, `.`,
		`\\server\share\folder`, `//server/share`, `\\?\C:\long\path`, `C:\a\..\b`, `D:`, `C:\`, `日本\語`}
	code := `
from pathlib import PureWindowsPath as P
result = []
for s in args[0]:
    p = P(s)
    result += [str(p), str(p.is_absolute()), str(p.parent), p.name, str(p / 'x' / 'y'), str(p / 'D:\\other'), str(p / '\\root'),
               str(p.is_relative_to(P('c:\\games'))), str(P('C:\\Games\\Overwatch\\data').is_relative_to(p))]
`
	want := execStrings(t, code, inputs)
	withWindowsPaths(t)
	boolText := func(b bool) string {
		if b {
			return "True"
		}
		return "False"
	}
	for i, s := range inputs {
		got := []string{PathStr(s), boolText(IsAbs(s)), Parent(s), Name(s), Join(s, "x", "y"), Join(s, `D:\other`), Join(s, `\root`),
			boolText(IsRelativeTo(s, `c:\games`)), boolText(IsRelativeTo(`C:\Games\Overwatch\data`, s))}
		for j := range got {
			if got[j] != want[9*i+j] {
				t.Errorf("%q [%d]: got %q want %q", s, j, got[j], want[9*i+j])
			}
		}
	}
}

func TestOracleWindowsExpandUser(t *testing.T) {
	t.Setenv("USERPROFILE", `C:\Users\Ana`)
	t.Setenv("USERNAME", "Ana")
	inputs := []string{"~", `~\lib`, "~/lib", "~Ana", `~Bob\x`, `C:\~\x`, "x~"}
	code := `
import ntpath
from pathlib import PureWindowsPath as P
result = []
for s in args[0]:
    p = P(s)
    if not (p.drive or p.root) and p.parts and p.parts[0][:1] == '~':
        home = ntpath.expanduser(p.parts[0])
        result.append(str(P(home, *p.parts[1:])))
    else:
        result.append(str(p))
`
	want := execStrings(t, code, inputs)
	withWindowsPaths(t)
	for i, s := range inputs {
		got, err := ExpandUser(s)
		if err != nil || got != want[i] {
			t.Errorf("%q: got %q (%v) want %q", s, got, err, want[i])
		}
	}
}

func TestOraclePosixResolve(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "real", "sub"), 0o777)
	os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "abs-link"))
	os.Symlink("real/sub", filepath.Join(root, "rel-link"))
	os.Symlink("loop-b", filepath.Join(root, "loop-a"))
	os.Symlink("loop-a", filepath.Join(root, "loop-b"))
	os.Symlink("missing-target", filepath.Join(root, "dangling"))
	os.Symlink("..", filepath.Join(root, "real", "up"))
	inputs := []string{root, root + "/abs-link/sub/../x", root + "/rel-link/..", root + "/loop-a/x", root + "/dangling/y",
		root + "/real/up/real/./sub", root + "//real///sub/", root + "/nope/../real", "/", "//", "///x", "relative/x", ".", ""}
	want := execStrings(t, "from pathlib import Path\nresult = [str(Path(s).resolve()) for s in args[0]]", inputs)
	compareStrings(t, inputs, want, Resolve)
	want = execStrings(t, "from pathlib import Path\nresult = [str(Path(s)) for s in args[0]]", inputs)
	compareStrings(t, inputs, want, PathStr)
}

func TestOracleEqualAndTruthy(t *testing.T) {
	values := []string{`null`, `0`, `1`, `1.0`, `true`, `false`, `""`, `"1"`, `[]`, `[0]`, `{}`, `{"a": 1}`, `0.0`, `-0.0`, `2`, `[1, 2]`, `[1.0, 2]`, `{"a": 1.0}`}
	inputs := make([]json.RawMessage, len(values))
	for i, v := range values {
		inputs[i] = json.RawMessage(v)
	}
	code := "result = [[bool(a)] + [a == b for b in args[0]] for a in args[0]]"
	var want [][]bool
	json.Unmarshal(oracle.Exec(t, code, inputs).MustValue(t), &want)
	var got [][]bool
	for _, a := range values {
		x, _ := Loads([]byte(a))
		row := []bool{Truthy(x)}
		for _, b := range values {
			y, _ := Loads([]byte(b))
			row = append(row, Equal(x, y))
		}
		got = append(got, row)
	}
	jsoncmp.Equal(t, mustJSON(want), got, jsoncmp.Default)
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
