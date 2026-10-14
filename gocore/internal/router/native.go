package router

import (
	"encoding/json"
	"fmt"
)

// Native returns the methods answered in Go. Phase 1 ports only stateless
// methods: everything touching catalog.json stays in Python until every
// writer of that file is ported, so the two cores never race on it.
func Native() map[string]Handler {
	folder := constantError(0, "Folder dialogs are provided by the Avolition desktop window.")
	return map[string]Handler{
		// app.py records the time, but nothing reads it.
		"ping": constant(0, true),
		// Only the developer HTTP server stops on close_app; --stdio returns true.
		"close_app":                 constant(0, true),
		"browse_root":               folder,
		"browse_output":             folder,
		"browse_workflow_directory": constantError(1, "Folder dialogs are provided by the Avolition desktop window."),
		"browse_entity":             constantError(0, "File dialogs are provided by the Avolition desktop window."),
		"browse_map":                constantError(0, "Map file dialogs are provided by the Avolition desktop window."),
	}
}

func constant(arity int, value any) Handler {
	return func(args []json.RawMessage) (any, error) {
		if err := arguments(args, arity); err != nil {
			return nil, err
		}
		return value, nil
	}
}

func constantError(arity int, message string) Handler {
	return func(args []json.RawMessage) (any, error) {
		if err := arguments(args, arity); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s", message)
	}
}

// arguments reports a wrong argument count, as Python's TypeError would.
func arguments(args []json.RawMessage, want int) error {
	if len(args) != want {
		return fmt.Errorf("expected %d argument(s) but %d were given", want, len(args))
	}
	return nil
}
