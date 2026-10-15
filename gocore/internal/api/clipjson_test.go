package api

import (
	"testing"

	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
)

// orderedClipJSON is the ordered.Map encoding ClipEntry.MarshalJSON replaced.
func orderedClipJSON(c *ClipEntry) ([]byte, error) {
	out := ordered.New[any]()
	if c.Header != nil {
		h := c.Header
		out.Set("name", h.Name)
		out.Set("path", h.Path)
		out.Set("fps", h.FPS)
		out.Set("durationFrames", h.DurationFrames)
		out.Set("lastFrame", h.LastFrame)
		out.Set("durationSeconds", h.DurationSeconds)
		out.Set("boneCount", h.BoneCount)
		out.Set("size", h.Size)
		out.Set("version", h.Version)
	} else {
		out.Set("path", c.Path)
		out.Set("name", c.Name)
		out.Set("size", c.Size)
		out.Set("fps", 0)
		out.Set("durationFrames", 0)
		out.Set("durationSeconds", 0)
		out.Set("boneCount", 0)
		out.Set("error", c.ErrorText)
	}
	out.Set("relativePath", c.RelativePath)
	out.Set("annotation", c.Annotation)
	out.Set("duplicateKey", c.DuplicateKey)
	out.Set("duplicateCount", c.DuplicateCount)
	if c.DuplicateError != nil {
		out.Set("duplicateError", *c.DuplicateError)
	}
	return out.MarshalJSON()
}

func TestClipEntryJSONMatchesOrderedEncoding(t *testing.T) {
	message := "can't <read> & \"hash\" \u2028"
	annotation := defaultAnnotation()
	annotation.Set("notes", "a<b>&c é 🙂")
	entries := []*ClipEntry{
		{Header: &formats.ClipHeader{Name: "a<&>.owanimclip", Path: "/x/a<&>.owanimclip", FPS: 30, DurationFrames: 60,
			LastFrame: 59, DurationSeconds: 2.0000000001, BoneCount: 120, Size: 1 << 40, Version: "2.1"},
			RelativePath: "x/é", Annotation: annotation, DuplicateKey: "k", DuplicateCount: 2},
		{Header: &formats.ClipHeader{FPS: 1e-7, DurationSeconds: 1e21}, Annotation: defaultAnnotation(), DuplicateError: &message},
		{Path: "/bad\x01.owanimclip", Name: "bad\x01", Size: 3, ErrorText: message, Annotation: nil},
	}
	for i, entry := range entries {
		got, err := entry.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		want, err := orderedClipJSON(entry)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("entry %d:\n got %s\nwant %s", i, got, want)
		}
	}
}
