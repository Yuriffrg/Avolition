package c4dexport

import "strings"

// SourceIdentity derives export hints from the original file path, never from
// editable object names. Accept either separator regardless of the host OS.
func SourceIdentity(sourcePath string) (gameplay1P bool, heroName string) {
	parts := strings.FieldsFunc(sourcePath, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return false, ""
	}
	gameplay1P = strings.EqualFold(parts[len(parts)-1], "Gameplay1P.owentity")
	// The nearest Heroes directory identifies the export, even when its parent
	// library directory also happens to be named Heroes.
	for i := len(parts) - 3; i >= 0; i-- {
		if strings.EqualFold(parts[i], "Heroes") && parts[i+1] != "." && parts[i+1] != ".." {
			return gameplay1P, parts[i+1]
		}
	}
	return gameplay1P, ""
}
