// Package datatool ports owpreview/datatool.py: managed OWLib DataTool jobs;
// the upstream executable owns all extraction.
//
// Command contracts were checked against OWLib's README, ExtractHeroUnlocks,
// ExtractFlags, ListHeroes, ListHeroUnlocks, ListFlags and JSONTool. Skin
// extraction includes the gameplay entities and their animations; emotes,
// victory poses and highlight intros are separate unlock categories, not an
// extract-animations mode. The released executable v2.24.1.0+1149+df522f0c
// was run with --help; hidden flags/categories were also checked against its
// corresponding df522f0c source. This is verification provenance, not a
// pinned runtime version or release date. Map listing/extraction also follows
// ListMaps, MapHeader and ExtractMaps; see MAP_SOURCE_REFERENCE.md for the
// exact selection and structured catalog contract.
// https://github.com/overtools/OWLib#how-to-use
// https://github.com/overtools/OWLib/tree/develop/DataTool
package datatool

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

// Category is one supported unlock category.
type Category struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Categories is CATEGORIES (sent to the UI as capabilities.categories).
var Categories = []Category{
	{"skin", "Models + gameplay animations",
		"Extract a skin's entities, models and gameplay animation clips, including Gameplay1P and Gameplay3P when available."},
	{"emote", "Emotes", "Extract hero emote animation assets."},
	{"victorypose", "Victory poses", "Extract hero victory-pose animation assets."},
	{"highlightintro", "Highlight intros", "Extract hero highlight-intro animation assets."},
}

func isCategory(id string) bool {
	for _, category := range Categories {
		if category.ID == id {
			return true
		}
	}
	return false
}

var (
	ansi        = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	unsupported = regexp.MustCompile(`(?i)does not support this version|doesn't (?:properly )?support|unsupportedbuildversion`)
	badQuery    = regexp.MustCompile(`(?i)found nothing matching|error parsing query|unknown (?:type|tag|mode)|invalid for flag|cannot interpret your command|positional .* is required|flag .* is required`)
	badRuntime  = regexp.MustCompile(`(?i)you must install or update \.net|framework.*not found|failed to load.*(?:dll|hostfxr)|dllnotfound|badimageformat|not a valid win32`)
	fatal       = regexp.MustCompile(`(?i)unhandled exception|error initializing casc|error reading game files|not a valid overwatch|invalid archive directory|access.*denied|permission denied|failed to determine tact product`)
)

// Error is DataToolError.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func dataToolError(message string) error { return &Error{message} }

func dataToolErrorf(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// ValueError is a Python ValueError (invalid selection or path input).
type ValueError struct{ Message string }

func (e *ValueError) Error() string { return e.Message }

var now = time.Now

// isoNow is datetime.now(timezone.utc).isoformat().
func isoNow() string {
	t := now().UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05") + "+00:00"
	}
	return t.Format("2006-01-02T15:04:05.000000") + "+00:00"
}

func path(value any, label string) (string, error) {
	text := ""
	if pycompat.Truthy(value) {
		text = pycompat.Strip(pycompat.Str(value))
	}
	if text == "" {
		return "", &ValueError{fmt.Sprintf("Choose %s.", label)}
	}
	expanded, err := pycompat.ExpandUser(text)
	if err != nil {
		return "", err
	}
	if !pycompat.IsAbs(expanded) {
		return "", &ValueError{fmt.Sprintf("Choose an absolute path for %s.", label)}
	}
	return pycompat.Resolve(expanded), nil
}

// Validation is validate_paths()'s result; errors keep Python's key order
// (overwatchInstall, then outputDirectory).
type Validation struct {
	OK               bool                 `json:"ok"`
	Errors           *ordered.Map[string] `json:"errors"`
	OverwatchInstall string               `json:"overwatchInstall"`
	OutputDirectory  string               `json:"outputDirectory"`
}

// createProbe creates and removes a temporary file in dir
// (tempfile.NamedTemporaryFile); tests inject failures.
var createProbe = func(dir string) error {
	file, err := os.CreateTemp(dir, ".avolition-write-check-*")
	if err != nil {
		return err
	}
	name := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

// ValidatePaths reads the game directory and probes output permissions
// without touching the game. output is optional (Python's None).
//
// Battle.net installs use .build.info; Steam's static container uses
// data/.build.config (TACTLib ClientHandler). A selected _retail_ folder with
// .flavor.info is normalized to its installation parent as TACTLib does.
func ValidatePaths(install string, output ...string) Validation {
	errorsMap := ordered.New[string]()
	game, gameSet := "", false
	err := func() error {
		resolved, err := path(install, "the Overwatch installation folder")
		if err != nil {
			return err
		}
		game, gameSet = resolved, true
		if pycompat.IsFile(pycompat.Join(game, ".flavor.info")) {
			game = pycompat.Parent(game)
		}
		if !pycompat.IsDir(game) {
			return &ValueError{"Overwatch Install does not exist. Select the installed game's folder."}
		}
		marker := pycompat.Join(game, ".build.info")
		static := pycompat.Join(game, "data", ".build.config")
		if !pycompat.IsFile(marker) && !pycompat.IsFile(static) {
			return &ValueError{"Select the Overwatch install root containing .build.info or data/.build.config, not an exported model folder."}
		}
		if !pycompat.IsDir(pycompat.Join(game, "data")) {
			return &ValueError{"This installation has no data folder. Finish installing Overwatch, then select its install root."}
		}
		source := static
		if pycompat.IsFile(marker) {
			source = marker
		}
		markerBytes, err := readPrefix(source, 16384)
		if err != nil {
			return err
		}
		if pycompat.IsFile(marker) {
			rows := pycompat.SplitLines(pycompat.DecodeUTF8(pycompat.StripBOM(markerBytes)))
			var columns []string
			if len(rows) > 0 {
				columns = strings.Split(rows[0], "|")
			}
			productColumn := -1
			for index, name := range columns {
				if strings.SplitN(name, "!", 2)[0] == "Product" {
					productColumn = index
					break
				}
			}
			if productColumn >= 0 {
				var products []string
				for _, row := range rows[1:] {
					if cells := strings.Split(row, "|"); len(cells) > productColumn {
						products = append(products, pycompat.Strip(cells[productColumn]))
					}
				}
				overwatch := false
				for _, product := range products {
					overwatch = overwatch || product == "pro" || product == "prot"
				}
				if len(products) > 0 && !overwatch {
					return &ValueError{"This is a different Battle.net game installation. Select the Overwatch install root."}
				}
			}
		}
		return nil
	}()
	if err != nil {
		errorsMap.Set("overwatchInstall", err.Error())
	}
	destination, destinationSet := "", false
	if len(output) > 0 {
		err := func() error {
			resolved, err := path(output[0], "the Output Directory")
			if err != nil {
				return err
			}
			destination, destinationSet = resolved, true
			if gameSet && pycompat.IsRelativeTo(destination, game) {
				return &ValueError{"Choose an output folder outside the Overwatch installation. Avolition never exports into game files."}
			}
			if pycompat.Exists(destination) && !pycompat.IsDir(destination) {
				return &ValueError{"Output Directory is a file. Choose a folder instead."}
			}
			// Probe the nearest existing ancestor, so validation need not create
			// the user's selected folder before an export actually begins.
			parent := destination
			for !pycompat.Exists(parent) && pycompat.Parent(parent) != parent {
				parent = pycompat.Parent(parent)
			}
			if !pycompat.IsDir(parent) {
				return &ValueError{"Output Directory is unavailable. Connect its drive or choose another folder."}
			}
			return createProbe(parent)
		}()
		if errors.Is(err, fs.ErrPermission) {
			errorsMap.Set("outputDirectory", "Avolition cannot write to Output Directory. Choose a writable folder or grant this account write access.")
		} else if err != nil {
			errorsMap.Set("outputDirectory", err.Error())
		}
	}
	result := Validation{OK: errorsMap.Len() == 0, Errors: errorsMap, OverwatchInstall: install}
	if gameSet {
		result.OverwatchInstall = game
	}
	if destinationSet {
		result.OutputDirectory = destination
	} else if len(output) > 0 {
		result.OutputDirectory = output[0]
	}
	return result
}

func readPrefix(name string, limit int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, limit))
}

// FilterName is _filter_name: DataTool query-language validation, not shell
// escaping. OWLib has no escaping for these name characters, so reject rather
// than change meaning.
func FilterName(value any, label string) (string, error) {
	text := ""
	if pycompat.Truthy(value) {
		text = pycompat.Strip(pycompat.Str(value))
	}
	if text == "" {
		return "", &ValueError{fmt.Sprintf("Select %s.", label)}
	}
	if strings.ContainsAny(text, "\x00\r\n|=,()!\"”") || (strings.Contains(text, "*") && text != "*") {
		return "", &ValueError{fmt.Sprintf("DataTool cannot represent this %s name in its filter syntax. Choose another asset or the whole category.", label)}
	}
	return text, nil
}

func getDefault(selection any, key string, fallback any) any {
	if pycompat.Has(selection, key) {
		return pycompat.Get(selection, key)
	}
	return fallback
}

func requireDict(selection any) error {
	if _, ok := pycompat.AsDict(selection); !ok {
		return fmt.Errorf("'%s' object has no attribute 'get'", pycompat.TypeName(selection))
	}
	return nil
}

// BuildExtractArgs is build_extract_args(install, output, selection).
func BuildExtractArgs(install, output string, selection any) ([]string, error) {
	if err := requireDict(selection); err != nil {
		return nil, err
	}
	category := pycompat.Str(getDefault(selection, "category", "skin"))
	if !isCategory(category) {
		return nil, &ValueError{"Choose a supported model or animation category."}
	}
	hero, err := FilterName(pycompat.Get(selection, "hero"), "hero")
	if err != nil {
		return nil, err
	}
	asset, err := FilterName(pycompat.Get(selection, "asset"), "asset")
	if err != nil {
		return nil, err
	}
	args := []string{install, "extract-unlocks", output, hero + "|" + category + "=" + asset}
	return append(args, extractOptions(selection)...), nil
}

func flag(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func extractOptions(selection any) []string {
	args := []string{"--online=false", "--convert-textures-type=png", "--convert-animations-type=owanimclip"}
	for _, option := range [][2]string{{"includeAnimations", "skip-animations"}, {"includeTextures", "skip-textures"}} {
		args = append(args, "--"+option[1]+"="+flag(!pycompat.Truthy(getDefault(selection, option[0], true))))
	}
	args = append(args, "--skip-sound="+flag(!pycompat.Truthy(getDefault(selection, "includeSound", false))))
	args = append(args, "--all-lods="+flag(pycompat.Truthy(getDefault(selection, "allLods", false))))
	return args
}

// BuildMapExtractArgs is build_map_extract_args: the resolved map query
// remains one literal argv entry, including spaces.
func BuildMapExtractArgs(install, output string, query any, selection any) ([]string, error) {
	name, err := FilterName(query, "map")
	if err != nil {
		return nil, err
	}
	if err := requireDict(selection); err != nil {
		return nil, err
	}
	return append([]string{install, "extract-maps", output, name}, extractOptions(selection)...), nil
}

// BuildListArgs is build_list_args(install, kind, destination).
func BuildListArgs(install, kind, destination string) ([]string, error) {
	if kind != "heroes" && kind != "unlocks" && kind != "maps" {
		return nil, &ValueError{"Unknown DataTool catalog request."}
	}
	args := []string{install, "list-" + kind, "--json", "--out=" + destination, "--online=false"}
	if kind == "unlocks" {
		args = append(args, "--flatten")
	}
	return args, nil
}

// Hero is one hero choice.
type Hero struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Unlock is one exportable unlock of a hero.
type Unlock struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// Catalog is parse_catalog()'s result. Treat it as read-only once built.
type Catalog struct {
	Heroes   []Hero                 `json:"heroes"`
	Unlocks  *ordered.Map[[]Unlock] `json:"unlocks"`
	Install  string                 `json:"install"`
	LoadedAt string                 `json:"loadedAt"`
}

// ParseCatalog is parse_catalog(heroes, unlocks, install) on decoded JSON
// (pycompat.Loads values).
func ParseCatalog(heroes, unlocks any, install string) (*Catalog, error) {
	heroRows, heroesOK := pycompat.AsDict(heroes)
	unlockRows, unlocksOK := pycompat.AsDict(unlocks)
	if !heroesOK || !unlocksOK {
		return nil, dataToolError("DataTool returned an unsupported catalog format. Check for a compatible Toolchain update.")
	}
	entries := []Hero{}
	for _, ident := range heroRows.Keys() {
		row, _ := heroRows.Get(ident)
		if _, ok := pycompat.AsDict(row); !ok || !pycompat.Truthy(pycompat.Get(row, "Name")) {
			continue
		}
		if isHero, ok := pycompat.Get(row, "IsHero").(bool); ok && !isHero {
			continue
		}
		id := any(ident)
		if guid := pycompat.Get(row, "GUID"); pycompat.Truthy(guid) {
			id = guid
		}
		entries = append(entries, Hero{ID: pycompat.Str(id), Name: pycompat.Str(pycompat.Get(row, "Name"))})
	}
	sort.SliceStable(entries, func(a, b int) bool {
		return pycompat.Casefold(entries[a].Name) < pycompat.Casefold(entries[b].Name)
	})
	choices := ordered.New[[]Unlock]()
	for _, hero := range unlockRows.Keys() {
		value, _ := unlockRows.Get(hero)
		rows, ok := value.([]any)
		if !ok {
			return nil, dataToolError("DataTool did not return flattened unlock data. Check for a compatible Toolchain update.")
		}
		seen := map[[2]string]bool{}
		list := []Unlock{}
		for _, row := range rows {
			if _, ok := pycompat.AsDict(row); !ok {
				continue
			}
			category := strings.ToLower(pycompat.Str(getDefault(row, "Type", "")))
			name := pycompat.Get(row, "Name")
			if !pycompat.Truthy(name) {
				name = pycompat.Get(row, "GUID")
			}
			if !isCategory(category) || !pycompat.Truthy(name) {
				continue
			}
			key := [2]string{category, pycompat.Str(name)}
			if seen[key] {
				continue
			}
			seen[key] = true
			list = append(list, Unlock{ID: pycompat.Str(getDefault(row, "GUID", name)), Name: pycompat.Str(name), Category: category})
		}
		sort.SliceStable(list, func(a, b int) bool {
			if list[a].Category != list[b].Category {
				return list[a].Category < list[b].Category
			}
			return pycompat.Casefold(list[a].Name) < pycompat.Casefold(list[b].Name)
		})
		choices.Set(hero, list)
	}
	if len(entries) == 0 {
		return nil, dataToolError("DataTool found no heroes in this installation. Check its version and the log.")
	}
	return &Catalog{Heroes: entries, Unlocks: choices, Install: install, LoadedAt: isoNow()}, nil
}

// Map is one map choice. Metadata fields keep DataTool's JSON values.
type Map struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Query       string   `json:"query"`
	MapGUID     string   `json:"mapGuid"`
	VariantName any      `json:"variantName"`
	MapType     any      `json:"mapType"`
	Description any      `json:"description"`
	StateA      any      `json:"stateA"`
	StateB      any      `json:"stateB"`
	GameModes   []string `json:"gameModes"`
}

// MapCatalog is parse_map_catalog()'s result. Treat it as read-only.
type MapCatalog struct {
	Maps     []Map  `json:"maps"`
	Install  string `json:"install"`
	LoadedAt string `json:"loadedAt"`
}

var (
	headerGUID = regexp.MustCompile(`^[0-9a-fA-F]{12}\.09[Ff]$`)
	mapGUID    = regexp.MustCompile(`^[0-9a-fA-F]{12}\.[0-9a-fA-F]{3}$`)
)

// ParseMapCatalog is parse_map_catalog(payload, install). ListMaps JSON keys
// are header GUIDs; MapGUID identifies the actual map.
//
// Match MapHeader.GetUniqueName(): VariantName ?? Name ?? "Title Screen",
// followed by the low 32-bit map index in hex. This distinguishes map variants.
func ParseMapCatalog(payload any, install string) (*MapCatalog, error) {
	rows, ok := pycompat.AsDict(payload)
	if !ok {
		return nil, dataToolError("DataTool returned an unsupported map catalog format. Check for a compatible Toolchain update.")
	}
	entries := []Map{}
	seen := map[string]bool{}
	for _, key := range rows.Keys() {
		row, _ := rows.Get(key)
		if _, ok := pycompat.AsDict(row); !ok {
			continue
		}
		identValue := pycompat.Get(row, "GUID")
		if !pycompat.Truthy(identValue) {
			identValue = key
		}
		ident, identOK := identValue.(string)
		guid, guidOK := pycompat.Get(row, "MapGUID").(string)
		if !identOK || !headerGUID.MatchString(ident) || !guidOK || !mapGUID.MatchString(guid) {
			continue
		}
		if seen[strings.ToUpper(ident)] {
			continue
		}
		full, _ := new(big.Int).SetString(strings.SplitN(guid, ".", 2)[0], 16)
		index := new(big.Int).And(full, big.NewInt(0xffffffff)).Uint64()
		if index == 0 {
			continue
		}
		nameValue := pycompat.Get(row, "VariantName")
		if nameValue == nil {
			nameValue = pycompat.Get(row, "Name")
		}
		name := "Title Screen"
		if nameValue != nil {
			text, ok := nameValue.(string)
			if !ok {
				continue
			}
			name = text
		}
		query := fmt.Sprintf("%s:%X", name, index)
		if _, err := FilterName(query, "map"); err != nil {
			// The extractor also matches the numeric map index, so a localized
			// name containing query-language punctuation still has an exact ID.
			query = fmt.Sprintf("%X", index)
		}
		modes := []string{}
		if value := pycompat.Get(row, "GameModes"); pycompat.Truthy(value) {
			items, err := pycompat.Iterate(value)
			if err != nil {
				return nil, err
			}
			for _, mode := range items {
				if _, ok := pycompat.AsDict(mode); ok && pycompat.Truthy(pycompat.Get(mode, "Name")) {
					modes = append(modes, pycompat.Str(pycompat.Get(mode, "Name")))
				}
			}
		}
		entries = append(entries, Map{
			ID: ident, Name: name, Query: query, MapGUID: guid,
			VariantName: pycompat.Get(row, "VariantName"), MapType: pycompat.Get(row, "MapType"),
			Description: pycompat.Get(row, "Description"), StateA: pycompat.Get(row, "StateA"),
			StateB: pycompat.Get(row, "StateB"), GameModes: modes,
		})
		seen[strings.ToUpper(ident)] = true
	}
	if len(entries) == 0 {
		return nil, dataToolError("DataTool found no usable maps in this installation. Check its version and the log.")
	}
	sort.SliceStable(entries, func(a, b int) bool {
		x, y := pycompat.Casefold(entries[a].Name), pycompat.Casefold(entries[b].Name)
		if x != y {
			return x < y
		}
		if entries[a].MapGUID != entries[b].MapGUID {
			return entries[a].MapGUID < entries[b].MapGUID
		}
		return entries[a].ID < entries[b].ID
	})
	return &MapCatalog{Maps: entries, Install: install, LoadedAt: isoNow()}, nil
}
