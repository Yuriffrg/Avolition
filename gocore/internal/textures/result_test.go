package textures

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Result's JSON form is what the former struct (dataUri string) encoded to.
func TestResultJSONMatchesStructEncoding(t *testing.T) {
	type legacy struct {
		DataURI string `json:"dataUri"`
		Width   int    `json:"width"`
		Height  int    `json:"height"`
		Warning string `json:"warning,omitempty"`
	}
	for _, r := range []*Result{
		{PNG: []byte{1, 2, 3, 250}, Width: 3, Height: 1},
		{PNG: EncodePNG(patternImage(5, 3, 4)), Width: 5, Height: 3, Warning: "a<b>&c \"é\"  : preview resized to 5 × 3"},
	} {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		encoder.Encode(legacy{r.DataURI(), r.Width, r.Height, r.Warning})
		want := strings.TrimSuffix(buffer.String(), "\n")
		if got := string(r.AppendJSON(nil)); got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
		var back Result
		if err := json.Unmarshal([]byte(want), &back); err != nil || !bytes.Equal(back.PNG, r.PNG) || back.Warning != r.Warning {
			t.Fatalf("round trip: %v", err)
		}
	}
}
