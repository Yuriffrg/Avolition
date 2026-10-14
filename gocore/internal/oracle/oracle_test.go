package oracle

import (
	"encoding/json"
	"testing"

	"avolition/gocore/internal/jsoncmp"
	"avolition/gocore/internal/ordered"
)

func TestOracleRoundTrip(t *testing.T) {
	result := Call(t, "owpreview.object_names:validate_name", "  Arms  ")
	jsoncmp.Equal(t, result.MustValue(t), json.RawMessage(`"Arms"`), jsoncmp.Default)
	failure := Exec(t, "raise ValueError('boom')")
	if failure.OK || failure.Error != "boom" || failure.Type != "ValueError" {
		t.Fatalf("got %+v", failure)
	}
	value := Exec(t, "import struct\nresult = struct.unpack('<f', struct.pack('<f', 0.3))[0]").MustValue(t)
	jsoncmp.Equal(t, value, []byte("0.3"), jsoncmp.Default)
}

func TestOrderedMapKeepsPythonOrder(t *testing.T) {
	var m ordered.Map[int]
	if err := json.Unmarshal([]byte(`{"z":1,"a":2,"m":3}`), &m); err != nil {
		t.Fatal(err)
	}
	m.Set("a", 9)
	m.Set("b", 4)
	m.Delete("m")
	out, _ := json.Marshal(&m)
	if string(out) != `{"z":1,"a":9,"b":4}` {
		t.Fatalf("got %s", out)
	}
}
