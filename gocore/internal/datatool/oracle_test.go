package datatool

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/pycompat"
)

type outcome struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Dump  string          `json:"dump"`
	Error string          `json:"error"`
	Type  string          `json:"type"`
}

const helper = `
import json
from owpreview import datatool
def run(function, calls, blank=False):
    out = []
    for call in calls:
        try:
            value = function(*call)
            dump = ''
            if blank:
                value['loadedAt'] = ''
                dump = json.dumps(value, ensure_ascii=False)
            out.append({'ok': True, 'value': value, 'dump': dump})
        except Exception as error:
            out.append({'ok': False, 'error': str(error), 'type': type(error).__name__})
    return out
`

func runOracle(t *testing.T, code string, calls any) []outcome {
	t.Helper()
	var outcomes []outcome
	if err := json.Unmarshal(oracle.Exec(t, helper+code, calls).MustValue(t), &outcomes); err != nil {
		t.Fatal(err)
	}
	return outcomes
}

func decode(t *testing.T, raw string) any {
	value, err := pycompat.Loads([]byte(raw))
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return value
}

// checkError compares a Go error with the Python exception, including its
// class (ValueError, DataToolError, others).
func checkError(t *testing.T, label string, want outcome, err error) {
	t.Helper()
	if err == nil || err.Error() != want.Error {
		t.Errorf("%s: want %s %q, got %v", label, want.Type, want.Error, err)
		return
	}
	var valueErr *ValueError
	var dataErr *Error
	if (want.Type == "ValueError") != errors.As(err, &valueErr) || (want.Type == "DataToolError") != errors.As(err, &dataErr) {
		t.Errorf("%s: error type %T, want %s", label, err, want.Type)
	}
}

var selections = []string{
	`{"hero": "Lúcio", "asset": "Overwatch Classic", "category": "skin"}`, `{"hero": "*", "asset": "*", "category": "emote"}`,
	`{"hero": "  Lúcio  ", "asset": " Dance "}`, `{"hero": "Lúcio", "asset": "*", "category": "invented"}`,
	`{"hero": "Lúcio", "asset": "*", "category": null}`, `{"hero": "Lúcio", "asset": "*", "category": 5}`,
	`{"hero": "", "asset": "*"}`, `{"asset": "*"}`, `{"hero": "Lúcio"}`, `{"hero": 0, "asset": "*"}`, `{"hero": 12, "asset": 3.5}`,
	`{"hero": true, "asset": "*"}`, `{"hero": ["a"], "asset": "*"}`, `{"hero": "a|b", "asset": "*"}`, `{"hero": "a=b", "asset": "*"}`,
	`{"hero": "a,b", "asset": "*"}`, `{"hero": "(a)", "asset": "*"}`, `{"hero": "a!", "asset": "*"}`, `{"hero": "a\"", "asset": "*"}`,
	`{"hero": "a”", "asset": "*"}`, `{"hero": "a“", "asset": "*"}`, `{"hero": "a\u0000", "asset": "*"}`, `{"hero": "a\nb", "asset": "*"}`,
	`{"hero": "**", "asset": "*"}`, `{"hero": " * ", "asset": "*"}`, `{"hero": "x", "asset": "*",
	  "includeAnimations": 0, "includeTextures": null, "includeSound": "yes", "allLods": []}`,
	`{"hero": "x", "asset": "y", "includeAnimations": [], "includeTextures": {}, "includeSound": [0], "allLods": 1.0}`,
	`{"hero": "x", "asset": "y", "category": "highlightintro", "includeSound": false, "allLods": false}`,
	`{}`, `[]`, `null`, `"text"`,
}

func TestOracleBuildExtractArgs(t *testing.T) {
	var calls []json.RawMessage
	for _, s := range selections {
		calls = append(calls, json.RawMessage(`["C:\\Games\\Overwatch", "/out dir", `+s+`]`))
	}
	outcomes := runOracle(t, "result = run(datatool.build_extract_args, args[0])", calls)
	for i, s := range selections {
		got, err := BuildExtractArgs(`C:\Games\Overwatch`, "/out dir", decode(t, s))
		if !outcomes[i].OK {
			checkError(t, s, outcomes[i], err)
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		jsoncmp.Equal(t, outcomes[i].Value, got, jsoncmp.Default)
	}
}

func TestOracleBuildMapAndListArgs(t *testing.T) {
	queries := []string{`"King’s Row:D86"`, `"*"`, `""`, `null`, `"  padded  "`, `"A|B"`, `"x*"`, `5`, `"Dorado\nOther"`, `"日本語:1F"`}
	var calls []json.RawMessage
	for _, q := range queries {
		calls = append(calls, json.RawMessage(`["/game", "/out", `+q+`, {"includeTextures": false, "allLods": true}]`))
	}
	outcomes := runOracle(t, "result = run(datatool.build_map_extract_args, args[0])", calls)
	for i, q := range queries {
		got, err := BuildMapExtractArgs("/game", "/out", decode(t, q), decode(t, `{"includeTextures": false, "allLods": true}`))
		if !outcomes[i].OK {
			checkError(t, q, outcomes[i], err)
		} else if err != nil {
			t.Errorf("%s: %v", q, err)
		} else {
			jsoncmp.Equal(t, outcomes[i].Value, got, jsoncmp.Default)
		}
	}
	kinds := []string{"heroes", "unlocks", "maps", "other", ""}
	var listCalls [][]string
	for _, kind := range kinds {
		listCalls = append(listCalls, []string{"/game dir", kind, "/state/目录/x.json"})
	}
	outcomes = runOracle(t, "result = run(datatool.build_list_args, args[0])", listCalls)
	for i, kind := range kinds {
		got, err := BuildListArgs("/game dir", kind, "/state/目录/x.json")
		if !outcomes[i].OK {
			checkError(t, kind, outcomes[i], err)
		} else {
			jsoncmp.Equal(t, outcomes[i].Value, got, jsoncmp.Default)
		}
	}
}

func TestOracleFilterName(t *testing.T) {
	values := []string{`"plain"`, `"  spaced\t"`, `"\u00a0nbsp\u3000"`, `"\u001cx"`, `"*"`, `"**"`, `""`, `null`, `false`, `0`, `0.0`, `1`, `[]`, `{}`,
		`["a"]`, `{"a": "b"}`, `"a=b"`, `"日本語"`, `"tab\there"`, `"car\rriage"`, `"’"`, `"”"`, `"\u2028"`}
	var calls []json.RawMessage
	for _, v := range values {
		calls = append(calls, json.RawMessage(`[`+v+`, "asset"]`))
	}
	outcomes := runOracle(t, "result = run(datatool._filter_name, args[0])", calls)
	for i, v := range values {
		got, err := FilterName(decode(t, v), "asset")
		if !outcomes[i].OK {
			checkError(t, v, outcomes[i], err)
		} else if err != nil {
			t.Errorf("%s: %v", v, err)
		} else {
			jsoncmp.Equal(t, outcomes[i].Value, got, jsoncmp.Default)
		}
	}
}

func TestOracleParseCatalog(t *testing.T) {
	cases := [][2]string{
		{`{"001.075": {"Name": "Lúcio", "IsHero": true}, "b": {"Name": "NPC", "IsHero": false}, "c": {"Name": "ana", "IsHero": 0, "GUID": 77},
		   "d": {"Name": "Ärger", "GUID": ""}, "e": {"Name": ""}, "f": "row", "g": {"Name": "zed", "IsHero": null}, "h": {"Name": "Äb", "GUID": [1, "x"]},
		   "i": {"Name": "STRASSE"}, "j": {"Name": "straße"}, "k": {"Name": 12.5, "GUID": {"x": true}}}`,
			`{"Lúcio": [{"Name": "Dance", "Type": "Emote", "GUID": "1"}, {"Name": "Dance", "Type": "EMOTE", "GUID": "2"}, {"Name": "Icon", "Type": "Icon"},
			  {"Name": "", "GUID": "g-only", "Type": "Skin"}, {"Name": null, "GUID": null, "Type": "skin"}, {"Name": "b", "Type": "VictoryPose"},
			  {"Name": "a", "Type": "victorypose", "GUID": null}, {"Name": "Z", "Type": "Skin"}, {"Name": "y", "Type": "skin"}, "junk", 5,
			  {"Name": 5, "Type": "Skin"}, {"Name": "5", "Type": "Skin"}, {"Name": "x", "Type": null}],
			  "Empty": [], "Zed": [{"Name": "HighlightIntro", "Type": "HighlightIntro"}]}`},
		{`{"a": {"Name": "Lúcio"}}`, `{"Lúcio": {}}`},
		{`{}`, `{}`}, {`[]`, `{}`}, {`{"a": {"Name": "x"}}`, `[]`}, {`{"a": {"Name": "x", "IsHero": false}}`, `{}`},
		{`{"a": {"Name": "x"}}`, `{"x": null}`},
	}
	var calls []json.RawMessage
	for _, c := range cases {
		calls = append(calls, json.RawMessage(`[`+c[0]+`, `+c[1]+`, "/game"]`))
	}
	outcomes := runOracle(t, "result = run(datatool.parse_catalog, args[0], True)", calls)
	for i, c := range cases {
		got, err := ParseCatalog(decode(t, c[0]), decode(t, c[1]), "/game")
		if !outcomes[i].OK {
			checkError(t, c[0], outcomes[i], err)
			continue
		}
		if err != nil {
			t.Errorf("case %d: %v", i, err)
			continue
		}
		got.LoadedAt = ""
		dump, _ := pycompat.Dumps(got, -1)
		if string(dump) != outcomes[i].Dump {
			t.Errorf("case %d:\n got %s\nwant %s", i, dump, outcomes[i].Dump)
		}
	}
}

func TestOracleParseMapCatalog(t *testing.T) {
	payloads := []string{
		mapsJSON,
		`{"000000000001.09F": {"VariantName": "A|B", "MapGUID": "012300000D86.002", "Name": "N"}, "000000000002.09F": {"Name": null, "VariantName": null, "MapGUID": "000000000D87.002"}}`,
		`{"k1": {"GUID": "00000000000A.09f", "MapGUID": "abcdef012345.0AB", "Name": "lower", "GameModes": [{"Name": "M"}, {"Name": ""}, "x", {"Name": 7}]},
		  "00000000000a.09F": {"MapGUID": "000000000001.002", "Name": "dup by case"}, "00000000000B.09F": {"MapGUID": "FFFFFFFF00000000.002"},
		  "00000000000C.09F": {"MapGUID": "123400000000.002", "Name": "zero index"}, "00000000000D.09F": {"MapGUID": "000000000001.002", "VariantName": 5},
		  "00000000000E.09F": {"MapGUID": "000000000002.002", "Name": "  spaced  ", "StateA": {"x": 1}, "Description": [1]},
		  "00000000000F.09F": {"MapGUID": "000000000003.002", "Name": "Équipe", "GameModes": {"Name": "dict"}},
		  "000000000010.09F": {"MapGUID": "000000000004.002", "Name": "équipe", "GameModes": "str"},
		  "000000000011.09F": {"MapGUID": "000000000005.002", "VariantName": "", "Name": "ignored"},
		  "000000000012.09F": {"MapGUID": "000000000006.002", "Name": "*"},
		  "bad": {"MapGUID": "000000000007.002"}, "000000000013.09F": {"GUID": 5, "MapGUID": "000000000007.002"},
		  "000000000014.09F": {"MapGUID": "0000000007.002"}, "000000000015.09F": "row"}`,
		`{"000000000001.09F": {"MapGUID": "000000000D86.002", "GameModes": 5}}`,
		`[]`, `null`, `{}`, `{"bad": {}}`, `{"x": {"GUID": "not-a-guid", "MapGUID": "000000000D86.002"}}`,
	}
	var calls []json.RawMessage
	for _, p := range payloads {
		calls = append(calls, json.RawMessage(`[`+p+`, "/game"]`))
	}
	outcomes := runOracle(t, "result = run(datatool.parse_map_catalog, args[0], True)", calls)
	for i, p := range payloads {
		got, err := ParseMapCatalog(decode(t, p), "/game")
		if !outcomes[i].OK {
			if err == nil || err.Error() != outcomes[i].Error {
				t.Errorf("payload %d: want %s %q, got %v", i, outcomes[i].Type, outcomes[i].Error, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("payload %d: %v", i, err)
			continue
		}
		got.LoadedAt = ""
		dump, _ := pycompat.Dumps(got, -1)
		if string(dump) != outcomes[i].Dump {
			t.Errorf("payload %d:\n got %s\nwant %s", i, dump, outcomes[i].Dump)
		}
	}
}

func TestOracleValidatePaths(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		os.MkdirAll(filepath.Dir(path), 0o777)
		os.WriteFile(path, []byte(text), 0o666)
	}
	game := filepath.Join(root, "Overwatch 安装")
	write(filepath.Join(game, ".build.info"), "\ufeffBranch!STRING:0|Product!STRING:0\r\nus|prot\r\n")
	os.MkdirAll(filepath.Join(game, "data"), 0o777)
	write(filepath.Join(game, "_retail_", ".flavor.info"), "x")
	steam := filepath.Join(root, "steam")
	write(filepath.Join(steam, "data", ".build.config"), "x")
	other := filepath.Join(root, "other game")
	write(filepath.Join(other, ".build.info"), "Product!STRING:0\nwow\n\n")
	os.MkdirAll(filepath.Join(other, "data"), 0o777)
	noData := filepath.Join(root, "no data")
	write(filepath.Join(noData, ".build.info"), "x")
	noProduct := filepath.Join(root, "no product")
	write(filepath.Join(noProduct, ".build.info"), "Name!STRING:0\nwow\n")
	os.MkdirAll(filepath.Join(noProduct, "data"), 0o777)
	blankRows := filepath.Join(root, "blank rows")
	write(filepath.Join(blankRows, ".build.info"), "Product!STRING:0|X\n\npro|1\n\xff\xfe|bad\n")
	os.MkdirAll(filepath.Join(blankRows, "data"), 0o777)
	file := filepath.Join(root, "file.txt")
	write(file, "x")
	output := filepath.Join(root, "Exports 输出")
	os.Mkdir(output, 0o777)
	installs := []string{game, game + "/_retail_", "  " + game + "  ", steam, other, noData, noProduct, blankRows, "", "   ", "relative",
		filepath.Join(root, "missing"), file, output, root + "/./Overwatch 安装/../Overwatch 安装"}
	outputs := []any{nil, output, filepath.Join(output, "new", "deeper"), filepath.Join(game, "Exports"), file, filepath.Join(file, "child"),
		"", "relative", "/"}
	type call struct {
		Install string `json:"install"`
		Output  any    `json:"output"`
	}
	var calls [][]any
	var pairs []call
	for _, install := range installs {
		for _, out := range outputs {
			calls = append(calls, []any{install, out})
			pairs = append(pairs, call{install, out})
		}
	}
	outcomes := runOracle(t, "result = run(datatool.validate_paths, args[0])", calls)
	for i, pair := range pairs {
		var got Validation
		if pair.Output == nil {
			got = ValidatePaths(pair.Install)
		} else {
			got = ValidatePaths(pair.Install, pair.Output.(string))
		}
		if !outcomes[i].OK {
			t.Errorf("%+v: python raised %s", pair, outcomes[i].Error)
			continue
		}
		jsoncmp.Equal(t, outcomes[i].Value, got, jsoncmp.Default)
		var want struct {
			Errors json.RawMessage `json:"errors"`
		}
		json.Unmarshal(outcomes[i].Value, &want)
		wantErrors, _ := pycompat.Loads(want.Errors)
		wantDump, _ := pycompat.Dumps(wantErrors, -1)
		gotDump, _ := pycompat.Dumps(got.Errors, -1)
		if string(wantDump) != string(gotDump) {
			t.Errorf("%+v: error order %s vs %s", pair, gotDump, wantDump)
		}
	}
}
