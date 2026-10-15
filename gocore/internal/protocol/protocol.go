// Package protocol implements the private JSON-lines transport between the
// Avolition desktop host and its core. It mirrors run_stdio in app.py: one
// request object per line in, one {"id", "result"} object per line out.
package protocol

import (
	"avolition/gocore/internal/pycompat"
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// MaxRequestChars matches app.py's per-line limit (counted in characters).
const MaxRequestChars = 100000

// Methods is the public API surface, kept identical to API_METHODS in app.py.
// Anything else is rejected before dispatch, exactly as the Python core does.
var Methods = []string{
	"bootstrap", "browse_root", "browse_output", "set_output_folder", "set_export_settings", "export_animation", "export_animation_set", "c4d_status",
	"browse_entity", "scan_root", "load_entity", "preview_entity", "release_entity_preview", "load_texture", "load_animation", "save_annotation", "ping", "close_app",
	"workflow_settings", "use_library_directory", "browse_workflow_directory", "open_toolchain_output",
	"toolchain_status", "toolchain_check", "toolchain_install", "toolchain_validate", "toolchain_list",
	"toolchain_start", "toolchain_cancel", "analysis_start", "analysis_status", "analysis_index", "analysis_detail",
	"read_clip_bindings", "library_settings", "export_queue_save", "export_queue_remove",
	"export_queue_run", "open_export_output", "maps_settings", "scan_maps", "load_map", "export_map",
	"export_map_start", "export_map_status", "browse_map", "open_map_output", "object_names", "save_object_name",
}

var known = func() map[string]bool {
	set := make(map[string]bool, len(Methods))
	for _, method := range Methods {
		set[method] = true
	}
	return set
}()

// Request is a validated request. ID holds the original JSON token so replies
// echo it unchanged; Args stay raw until a handler decodes them.
type Request struct {
	ID     json.RawMessage
	Method string
	Args   []json.RawMessage
}

// Failure is a request rejected before dispatch. A nil ID is sent as null.
type Failure struct {
	ID      json.RawMessage
	Message string
}

// Parse validates one line in the same order as app.py, so the reply id is
// null for exactly the failures where Python could not establish one.
func Parse(line []byte) (Request, *Failure) {
	// Python counts the line with its "\n" terminator.
	if utf8.RuneCount(line)+1 > MaxRequestChars {
		return Request{}, &Failure{Message: "Request too large"}
	}
	if !utf8.Valid(line) {
		return Request{}, &Failure{Message: "Request is not valid UTF-8"}
	}
	trimmed := bytes.TrimSpace(line)
	if !json.Valid(trimmed) {
		// json.loads(line) on the "\n"-terminated line: report its exact
		// error text, positions included.
		message := "Invalid JSON request"
		if _, err := pycompat.LoadsText(string(line) + "\n"); err != nil {
			message = err.Error()
		}
		return Request{}, &Failure{Message: message}
	}
	var fields map[string]json.RawMessage
	if trimmed[0] != '{' || json.Unmarshal(trimmed, &fields) != nil {
		return Request{}, &Failure{Message: "Expected a request object"}
	}
	id := fields["id"]
	if !validID(id) {
		return Request{}, &Failure{Message: "Expected a string or integer request id"}
	}
	var method string
	if json.Unmarshal(fields["method"], &method) != nil || !known[method] {
		return Request{}, &Failure{ID: id, Message: "Unknown API method"}
	}
	args := []json.RawMessage{}
	if raw, present := fields["args"]; present {
		if len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &args) != nil {
			return Request{}, &Failure{ID: id, Message: "Expected an argument list"}
		}
	}
	return Request{ID: id, Method: method, Args: args}, nil
}

// validID accepts a JSON string or an integer literal. Python's json module
// parses 1.0 and 1e3 as floats, and booleans are explicitly refused.
func validID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '"' {
		return true // json.Valid already checked the whole line.
	}
	digits := raw
	if digits[0] == '-' {
		digits = digits[1:]
	}
	if len(digits) == 0 {
		return false
	}
	for _, char := range digits {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// Success encodes {"id": id, "result": {"ok": true, "data": data}}.
func Success(id json.RawMessage, data any) ([]byte, error) {
	return encode(id, map[string]any{"ok": true, "data": data})
}

// Error encodes {"id": id, "result": {"ok": false, "error": message}}.
func Error(id json.RawMessage, message string) []byte {
	line, err := encode(id, map[string]any{"ok": false, "error": message})
	if err != nil {
		panic(err) // A string message always encodes.
	}
	return line
}

func encode(id json.RawMessage, result map[string]any) ([]byte, error) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	// Python writes ensure_ascii=False without HTML escaping; match it.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		ID     json.RawMessage `json:"id"`
		Result map[string]any  `json:"result"`
	}{id, result}); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil // Encode already appended the newline.
}
