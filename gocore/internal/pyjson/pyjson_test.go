package pyjson_test

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"avolition/gocore/internal/oracle"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pyjson"
)

func TestFloatRepr(t *testing.T) {
	for value, want := range map[float64]string{
		0: "0.0", 30: "30.0", -0.5: "-0.5", 1e-5: "1e-05", 1e16: "1e+16", 1234567890123456: "1234567890123456.0",
		0.1: "0.1", 1.0 / 3: "0.3333333333333333", 1e-4: "0.0001", 123e300: "1.23e+302", math.Copysign(0, -1): "-0.0",
		float64(float32(0.1)): "0.10000000149011612", 5e-324: "5e-324",
	} {
		if got := pyjson.FloatRepr(value); got != want {
			t.Errorf("%v: got %s want %s", value, got, want)
		}
	}
}

type inner struct {
	B string `json:"b"`
}

type embedded struct {
	inner
	A      int          `json:"a"`
	F      pyjson.Float `json:"f"`
	P      *int         `json:"p"`
	O      *int         `json:"o,omitempty"`
	Skip   string       `json:"-"`
	hidden int
}

func TestStructsFollowEncodingJSONFieldRules(t *testing.T) {
	value := embedded{inner: inner{B: "x"}, A: 1, F: 2, hidden: 3}
	got, err := pyjson.Dumps(value, pyjson.Options{Indent: -1})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"b": "x", "a": 1, "f": 2.0, "p": null}` {
		t.Fatal(string(got))
	}
	standard, _ := json.Marshal(value)
	if string(standard) != `{"b":"x","a":1,"f":2.0,"p":null}` {
		t.Fatal(string(standard))
	}
}

const dumpsPython = `
import json, hashlib
value = json.loads(args[0])
result = {
    'canonical': json.dumps(value, sort_keys=True, ensure_ascii=False),
    'indented': json.dumps(value, ensure_ascii=False, indent=2),
    'ascii': json.dumps(value, indent=2),
    'digest': hashlib.sha256(json.dumps(value, sort_keys=True, ensure_ascii=False).encode('utf-8')).hexdigest(),
}
`

func TestDumpsMatchesPython(t *testing.T) {
	m := ordered.New[any]()
	m.Set("zeta", 1)
	m.Set("alpha", []any{})
	m.Set("Ä", map[string]any{})
	m.Set("emoji 😀", "  tab\t nul\x00 del\x7f quote\" back\\ slash/ é 日本")
	values := []any{
		m,
		[]any{1.5, 30.0, 1e-7, 1e22, -0.0, 0.1, json.Number("1E400"), json.Number("-0"), json.Number("12345678901234567890123")},
		map[string]any{"b": []any{map[string]any{"y": nil, "x": true}}, "a": false, "c": [][]any{{}, {1}}},
		"plain",
		[]any{},
	}
	for _, value := range values {
		input, err := pyjson.Dumps(value, pyjson.Options{Indent: -1})
		if err != nil {
			t.Fatal(err)
		}
		var want struct {
			Canonical, Indented, ASCII, Digest string
		}
		if err := json.Unmarshal(oracle.Exec(t, dumpsPython, string(input)).MustValue(t), &want); err != nil {
			t.Fatal(err)
		}
		check := func(label string, got []byte, err error, want string) {
			t.Helper()
			if err != nil || string(got) != want {
				t.Errorf("%s: %v\nwant %s\ngot  %s", label, err, want, got)
			}
		}
		got, err := pyjson.Canonical(value)
		check("canonical", got, err, want.Canonical)
		got, err = pyjson.Indented(value)
		check("indented", got, err, want.Indented)
		got, err = pyjson.Dumps(value, pyjson.Options{Indent: 2, EnsureASCII: true})
		check("ascii", got, err, want.ASCII)
		var buffer bytes.Buffer
		err = pyjson.Write(&buffer, value, pyjson.Options{Indent: 2})
		check("write", buffer.Bytes(), err, want.Indented)
		got, err = pyjson.Reformat(input, pyjson.Options{Indent: -1, SortKeys: true})
		check("reformat", got, err, want.Canonical)
		digest, err := pyjson.Digest(value)
		check("digest", []byte(digest), err, want.Digest)
	}
}

func TestNonFiniteFloatsAreRejected(t *testing.T) {
	if _, err := pyjson.Dumps(math.NaN(), pyjson.Options{Indent: -1}); err == nil {
		t.Fatal("NaN accepted")
	}
	if _, err := pyjson.Dumps(pyjson.Float(math.Inf(1)), pyjson.Options{Indent: -1}); err == nil {
		t.Fatal("Inf accepted")
	}
}
