// Package analysis keeps local, read-only animation evidence. It never
// assigns, renames or edits assets. It is the Go port of
// owpreview/animation_analysis.py.
//
// The inventory separates resource identity, byte equivalence and
// occurrences. Exported indexes are incomplete identities; no missing GUID
// bits are invented. See ANIMATION_SOURCE_REFERENCE.md for the
// version-pinned exporter evidence.
//
// # Python → Go map
//
//	PARSER_VERSION, TRACK_DEFINITION, LIMITATIONS  ParserVersion, TrackDefinition, Limitations
//	key, digest_json, parser_token                 Key, DigestJSON, ParserToken
//	local_identity, exported_resource              LocalIdentity, ExportedResource
//	snapshot                                       TakeSnapshot
//	analyze                                        Analyze (Options)
//	index_summary                                  Summarize
//	AnalysisManager                                Manager (NewManager)
//
// Inventories marshal to Python's dicts key for key; WriteJSON/Encode write
// the same bytes as json.dumps(inventory, ensure_ascii=False, indent=2), and
// DigestJSON is the same sha256 as Python's digest_json, so revisions, ids
// and cache files are interchangeable with the Python backend.
//
// File work (hashing, parsing, clip loading) runs on a bounded worker pool;
// assembly is sequential in Python's order, so results are identical.
package analysis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"avolition/gocore/internal/evidence"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pyjson"
	"avolition/gocore/internal/pypath"
)

// ParserVersion is the inventory parser version (a variable so tests can
// change it, like patching the Python module constant).
var ParserVersion = "1.0.0"

// TrackDefinition explains trackCount.
const TrackDefinition = "Exported bone records in the .owanimclip header; not skeleton size or transform-channel count."

var contextTypes = map[string]string{"highlightintro": "HighlightIntro", "emote": "Emote", "victorypose": "VictoryPose"}

// Limitations are listed in every inventory.
var Limitations = []string{
	"A shortened export identifier lacks part of the resource GUID. Local identities are scoped to this scan root.",
	"Model association and bone compatibility do not identify an action. Shared bones can match multiple models.",
	"Named unlock folders establish dependency membership, not the main actor or original internal animation name.",
	"No game build or exporting DataTool version is inferred from the currently installed Toolchain.",
}

const coverageDefinition = "Asset counts except userLabels (occurrences). Categories overlap; unknown means no source name or named usage context. Suggestions are skeleton compatibility only."

// DigestJSON is digest_json(value): the sha256 of Python's
// json.dumps(value, sort_keys=True, ensure_ascii=False).
func DigestJSON(value any) (string, error) { return pyjson.Digest(value) }

// ParserToken is parser_token().
func ParserToken() []string { return []string{ParserVersion, evidence.ParserVersion} }

var fullGUIDPattern = regexp.MustCompile(`^(?:0x)?[0-9A-Fa-f]{16}$`)

// LocalIdentity is local_identity(root, resource, content_hash,
// relative_path). Full GUIDs, if proven, remain strings. Different IDs never
// merge on bytes.
func LocalIdentity(root string, resource Resource, contentHash *string, relativePath string) (string, error) {
	return localIdentity(Key(root), resource, contentHash, relativePath)
}

func localIdentity(rootKey string, resource Resource, contentHash *string, relativePath string) (string, error) {
	var token []any
	if resource.FullGUID != nil && *resource.FullGUID != "" {
		full := *resource.FullGUID
		if !fullGUIDPattern.MatchString(full) {
			return "", valueError("A full GUID must be a lossless 16-hex-character string.")
		}
		token = []any{"guid", strings.ToUpper(strings.TrimPrefix(full, "0x"))}
	} else {
		scoped := relativePath
		if resource.LongKey != nil && *resource.LongKey != "" {
			scoped = ""
		}
		token = []any{"scoped", rootKey, resource.LongKey, resource.Type, resource.Index, scoped}
	}
	return "animation:" + pyjson.MustDigest([]any{token, contentHash}), nil
}

var hex12 = regexp.MustCompile(`^[0-9a-fA-F]{12}$`)

// ExportedResource is exported_resource(filename).
func ExportedResource(filename string) Resource {
	stem := pypath.Stem(filename)
	// GetNameIndex may be user/lookup text. Hex-looking filenames are
	// evidence of a displayed key only, never proof of a complete 64-bit
	// resource GUID.
	limitation := "Full GUID unavailable: filename/header do not preserve all resource identity bits."
	resource := Resource{Type: "006", TypeProvenance: "OWLib animation resource/exporter format", Display: stem, Limitation: &limitation}
	if hex12.MatchString(stem) {
		raw := strings.ToUpper(stem)
		index := raw[len(raw)-8:]
		resource.LongKey, resource.Index = &raw, &index
	}
	return resource
}

func rawJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func newEvidence(kind, source, field string, value any, description string) Evidence {
	return Evidence{Kind: kind, Source: source, Field: field, Value: rawJSON(value), Description: description}
}

// Options are analyze()'s keyword arguments.
type Options struct {
	// Annotations is the catalog's annotations object (path key → {name,
	// ...}); any JSON-marshalable value. nil is {}. Use json.Number or raw
	// JSON for numbers so revisions match Python's.
	Annotations any
	// InstalledToolchain is recorded as-is (JSON-marshalable; nil is null).
	InstalledToolchain any
	// Excluded paths are left out of the snapshot.
	Excluded []string
	// Progress receives status messages; it may be called from worker
	// goroutines, one call at a time.
	Progress func(string)
	// Prepared is a snapshot taken earlier (the manager's).
	Prepared *Snapshot
	// LabelsUnavailable is labels_available=False.
	LabelsUnavailable bool
	// now is replaceable in tests.
	now func() time.Time
}

// isoformat is datetime.isoformat() for an aware UTC datetime.
func isoformat(t time.Time) string {
	t = t.UTC()
	text := t.Format("2006-01-02T15:04:05")
	if micro := t.Nanosecond() / 1000; micro != 0 {
		text += fmt.Sprintf(".%06d", micro)
	}
	return text + "+00:00"
}

// annotationSet is the normalized annotations argument.
type annotationSet struct {
	raw    json.RawMessage
	byPath map[string]json.RawMessage
}

func normalizeJSON(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buffer.Bytes(), "\n")), nil
}

func newAnnotationSet(value any) (*annotationSet, error) {
	if value == nil {
		value = json.RawMessage("{}")
	}
	raw, err := normalizeJSON(value)
	if err != nil {
		return nil, err
	}
	set := &annotationSet{raw: raw}
	if err := json.Unmarshal(raw, &set.byPath); err != nil {
		return nil, fmt.Errorf("annotations must be an object: %w", err)
	}
	if set.byPath == nil { // JSON null: Python's `annotations or {}`.
		set.raw = json.RawMessage("{}")
	}
	return set, nil
}

// name is annotations.get(key, {}).get('name', ”).
func (a *annotationSet) name(key string) any {
	raw, ok := a.byPath[key]
	if !ok {
		return ""
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return ""
	}
	nameRaw, ok := object["name"]
	if !ok {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(nameRaw))
	decoder.UseNumber()
	var name any
	if decoder.Decode(&name) != nil {
		return ""
	}
	return name
}

// truthy is Python's bool() of a JSON value.
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func nickname(value any) *Nickname {
	if !truthy(value) {
		return nil
	}
	return &Nickname{Value: value, Provenance: "Whitehound user annotation"}
}

var driveLetter = regexp.MustCompile(`^[A-Za-z]:`)

type analyzer struct {
	root       string
	rootKey    string
	resolved   map[string]string // memoized pypath.Resolve
	byPath     map[string]string // key → walked path
	byParent   map[string][]string
	progress   func(string)
	annotation *annotationSet
}

func (a *analyzer) resolvePath(path string) string {
	if resolved, ok := a.resolved[path]; ok {
		return resolved
	}
	resolved := pypath.Resolve(path)
	a.resolved[path] = resolved
	return resolved
}

func (a *analyzer) key(path string) string { return normcase(a.resolvePath(path)) }

func (a *analyzer) rel(path string) string { return asPosix(relativeTo(path, a.root)) }

// resolve retains all exact relative matches. It never guesses a basename
// match.
func (a *analyzer) resolve(reference *string, owner string) []string {
	if reference == nil || *reference == "" {
		return nil
	}
	ref := strings.ReplaceAll(*reference, `\`, "/")
	if pypath.IsAbs(ref) || driveLetter.MatchString(ref) {
		return nil
	}
	parent := pypath.Parent(owner)
	bases := append([]string{parent}, pypath.Parents(parent)...)
	found := map[string]string{}
	for _, base := range bases {
		if !pypath.IsRelativeTo(base, a.root) {
			continue
		}
		candidate := a.resolvePath(pypath.Join(base, ref))
		if !pypath.IsRelativeTo(candidate, a.root) {
			continue
		}
		key := a.key(candidate)
		if path, ok := a.byPath[key]; ok {
			found[key] = path
		}
	}
	out := make([]string, 0, len(found))
	for _, path := range found {
		out = append(out, path)
	}
	sortPaths(out)
	return out
}

func ptr[T any](value T) *T { return &value }

// entityTargets resolves an entity's model or child reference using the
// exporter's Models/<name>/<file> and Entities/<name>/<file> layouts.
func (a *analyzer) entityTargets(reference string, entity *EntityRow, kind string) []string {
	if reference == "" {
		return nil
	}
	owner := entity.Path
	base := a.resolvePath(pypath.Join(pypath.Parent(owner), strings.ReplaceAll(entity.RelativeRoot, `\`, "/")))
	if !pypath.IsRelativeTo(base, a.root) {
		return nil
	}
	folder, extension := "Entities", ".owentity"
	if kind == "model" {
		folder, extension = "Models", ".owmdl"
	}
	name := strings.ReplaceAll(reference, `\`, "/")
	if strings.Contains(name, "/") || strings.HasSuffix(strings.ToLower(name), extension) {
		return a.resolve(&name, owner)
	}
	filename := name
	if kind == "model" && strings.HasSuffix(strings.ToUpper(name), ".00C") {
		filename = name[:len(name)-4]
	}
	directory := pypath.Join(base, folder, name)
	candidates := []string{pypath.Join(directory, filename+extension)}
	// Named exports can use GetNameIndex() for the inner filename. The
	// written folder scopes candidates; the header index must also match.
	for _, path := range a.byParent[normcase(pypath.Clean(directory))] {
		if strings.ToLower(pypath.Suffix(path)) == extension {
			candidates = append(candidates, path)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, path := range candidates {
		if _, ok := a.byPath[a.key(path)]; !ok {
			continue
		}
		resolved := a.resolvePath(path)
		if !seen[resolved] {
			seen[resolved] = true
			out = append(out, resolved)
		}
	}
	sortPaths(out)
	return out
}

// isDecimal is re.fullmatch(r'\d+', text) for str patterns.
func isDecimal(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// decimalValue is int(text) for a string of Unicode decimal digits.
func decimalValue(text string) *json.Number {
	digits := make([]byte, 0, len(text))
	for _, r := range text {
		if r >= '0' && r <= '9' {
			digits = append(digits, byte(r))
			continue
		}
		// Unicode decimal digits come in runs of ten starting at zero.
		zero := r
		for unicode.IsDigit(zero - 1) {
			zero--
		}
		digits = append(digits, byte('0'+(r-zero)%10))
	}
	trimmed := strings.TrimLeft(string(digits), "0")
	if trimmed == "" {
		trimmed = "0"
	}
	number := json.Number(trimmed)
	return &number
}

type parsed struct {
	model     *evidence.Model
	entity    *evidence.Entity
	companion *evidence.Companion
	effect    *evidence.Effect
	clip      *ClipSummary
	err       error
}

func summarizeClip(path string) (*ClipSummary, error) {
	clip, err := formats.LoadClip(path)
	if err != nil {
		return nil, err
	}
	animatedSet := map[string]bool{}
	channels := 0
	names := make([]string, 0, len(clip.Bones))
	for _, bone := range clip.Bones {
		names = append(names, bone.Name)
		for _, track := range []*formats.Track{bone.Positions, bone.Rotations, bone.Scales} {
			if track != nil && len(track.Frames) > 0 {
				channels++
				animatedSet[bone.Name] = true
			}
		}
	}
	animated := make([]string, 0, len(animatedSet))
	for name := range animatedSet {
		animated = append(animated, name)
	}
	sort.Strings(animated)
	warnings := clip.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return &ClipSummary{
		DurationSeconds: pyjson.FloatPtr(clip.DurationSeconds), FPS: pyjson.FloatPtr(clip.FPS),
		TrackCount: ptr(clip.BoneCount), TrackDefinition: TrackDefinition, TransformChannelCount: ptr(channels),
		BoneRecordIDs: names, AnimatedBoneIDs: animated, Warnings: &warnings, Version: ptr(clip.Version),
	}, nil
}

func parseFile(path string) (result parsed) {
	// Python records any exception as a parse error.
	defer func() {
		if recovered := recover(); recovered != nil {
			result = parsed{err: fmt.Errorf("%v", recovered)}
		}
	}()
	switch strings.ToLower(pypath.Suffix(path)) {
	case ".owmdl":
		model, err := evidence.ReadModel(path)
		return parsed{model: model, err: err}
	case ".owentity":
		entity, err := evidence.ReadEntity(path)
		return parsed{entity: entity, err: err}
	case ".owanim":
		companion, err := evidence.ReadCompanion(path)
		return parsed{companion: companion, err: err}
	case ".oweffect":
		effect, err := evidence.ReadEffect(path)
		return parsed{effect: effect, err: err}
	case ".owanimclip":
		clip, err := summarizeClip(path)
		return parsed{clip: clip, err: err}
	}
	return parsed{}
}

func (a *analyzer) namedContext(path string, contexts map[string]*Context, order *[]string) *Context {
	p := parts(path)
	for i := 1; i < len(p)-3; i++ {
		category, ok := contextTypes[strings.ToLower(p[i])]
		// Validate Heroes/<hero>/<UnlockType>/<event>/<name>, allowing a
		// chosen scan root inside Heroes. Generic Entities/HighlightIntro
		// is deliberately not a named unlock.
		if !ok || i < 2 || strings.ToLower(p[i-2]) != "heroes" {
			continue
		}
		folder := strings.Join(p[:i+3], "/")
		id := "context:" + pyjson.MustDigest([]any{a.rootKey, folder})
		context := &Context{ID: id, Label: p[i+2], Category: category, Kind: "named-unlock-folder",
			Role: "Dependency membership; actor role unknown",
			Evidence: []Evidence{newEvidence("export-path", a.rel(path), "UnlockType/event/unlock.GetName()", folder,
				"Verified OWLib unlock export layout; sanitized display label, not an internal clip name.")}}
		if _, exists := contexts[id]; !exists {
			contexts[id] = context
			*order = append(*order, id)
		}
		return context
	}
	return nil
}

func (a *analyzer) progressf(format string, args ...any) {
	a.progress(fmt.Sprintf(format, args...))
}

// Analyze is analyze(root, ...).
func Analyze(root string, opts Options) (*Inventory, error) { return analyzeWith(root, opts, nil) }

// analyzeWith is Analyze; resolved holds Path.resolve() results the snapshot
// already computed for a prepared snapshot (may be nil).
func analyzeWith(root string, opts Options, resolved map[string]string) (*Inventory, error) {
	root = pypath.Resolve(root)
	if !pypath.IsDir(root) {
		return nil, valueError("Scan an existing export folder before analyzing its library.")
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	snapshot := opts.Prepared
	if snapshot == nil {
		var err error
		if snapshot, resolved, err = snapshotWith(root, opts.Excluded, progress); err != nil {
			return nil, err
		}
	}
	annotations, err := newAnnotationSet(opts.Annotations)
	if err != nil {
		return nil, err
	}
	installed, err := normalizeJSON(opts.InstalledToolchain)
	if err != nil {
		return nil, err
	}
	if resolved == nil {
		resolved = map[string]string{}
	}
	a := &analyzer{root: root, rootKey: Key(root), resolved: resolved, byPath: map[string]string{},
		byParent: map[string][]string{}, progress: progress, annotation: annotations}
	now := time.Now
	if opts.now != nil {
		now = opts.now
	}
	return a.run(snapshot, annotations, installed, !opts.LabelsUnavailable, now())
}

func (a *analyzer) run(snapshot *Snapshot, annotations *annotationSet, installed json.RawMessage, labelsAvailable bool, analyzedAt time.Time) (*Inventory, error) {
	root := a.root
	paths := snapshot.Paths
	manifestByRel := make(map[string]*ManifestItem, len(snapshot.Manifest))
	for i := range snapshot.Manifest {
		manifestByRel[snapshot.Manifest[i].RelativePath] = &snapshot.Manifest[i]
	}
	for _, path := range paths {
		a.byPath[a.key(path)] = path
		parent := normcase(pypath.Parent(path))
		a.byParent[parent] = append(a.byParent[parent], path)
	}
	inventory := &Inventory{
		Schema: 1, ParserVersion: ParserVersion, Root: root, AnalyzedAt: isoformat(analyzedAt),
		InstalledToolchain: installed, SourceNames: []json.RawMessage{}, Models: []*ModelRow{},
		Entities: []*EntityRow{}, Companions: []*CompanionRow{}, Effects: []*EffectRow{},
		Assets: []*Asset{}, Occurrences: []*Occurrence{}, ParseErrors: []ParseError{},
		Limitations: append([]string{}, Limitations...), Manifest: snapshot.Manifest,
		UserLabelsAvailable: labelsAvailable, ProvenanceFiles: []ManifestItem{},
	}
	if inventory.Manifest == nil {
		inventory.Manifest = []ManifestItem{}
	}
	for _, item := range inventory.Manifest {
		if !suffixes[strings.ToLower(pypath.Suffix(item.RelativePath))] {
			inventory.ProvenanceFiles = append(inventory.ProvenanceFiles, item)
		}
	}

	// Parse every file on the worker pool; assemble in Python's order below.
	results := make([]parsed, len(paths))
	parallel(len(paths), func(i int) { results[i] = parseFile(paths[i]) })

	sha := func(path string) *string {
		if item, ok := manifestByRel[a.rel(path)]; ok {
			return item.SHA256
		}
		return nil
	}
	rowFor := func(path string) rowKeys {
		return rowKeys{RelativePath: a.rel(path), SHA256: sha(path),
			ID: pypath.Suffix(path)[1:] + ":" + pyjson.MustDigest([]any{a.rootKey, a.rel(path)})}
	}
	for i, path := range paths {
		result := results[i]
		if strings.ToLower(pypath.Suffix(path)) == ".owanimclip" || (result.model == nil && result.entity == nil &&
			result.companion == nil && result.effect == nil && result.err == nil) {
			continue
		}
		if result.err != nil {
			inventory.ParseErrors = append(inventory.ParseErrors, ParseError{path, a.rel(path), result.err.Error()})
			continue
		}
		switch {
		case result.model != nil:
			result.model.Path = path
			inventory.Models = append(inventory.Models, &ModelRow{Model: *result.model, rowKeys: rowFor(path)})
		case result.entity != nil:
			result.entity.Path = path
			inventory.Entities = append(inventory.Entities, &EntityRow{Entity: *result.entity, rowKeys: rowFor(path)})
		case result.companion != nil:
			result.companion.Path = path
			inventory.Companions = append(inventory.Companions, &CompanionRow{Companion: *result.companion, rowKeys: rowFor(path)})
		case result.effect != nil:
			result.effect.Path = path
			inventory.Effects = append(inventory.Effects, &EffectRow{Effect: *result.effect, rowKeys: rowFor(path)})
		}
	}
	models, entities, companions, effects := inventory.Models, inventory.Entities, inventory.Companions, inventory.Effects

	modelPaths := map[string]*ModelRow{}
	for _, m := range models {
		modelPaths[a.key(m.Path)] = m
	}
	entityPaths := map[string]*EntityRow{}
	for _, e := range entities {
		entityPaths[a.key(e.Path)] = e
	}
	modelBones := map[string]map[string]bool{}
	for _, m := range models {
		bones := make(map[string]bool, len(m.Bones))
		for _, b := range m.Bones {
			bones[b.Name] = true
		}
		modelBones[m.ID] = bones
	}
	modelEntities := map[string]map[string]bool{}
	var modelEntityOrder []string
	addModelEntity := func(modelID, entityID string) {
		members, ok := modelEntities[modelID]
		if !ok {
			members = map[string]bool{}
			modelEntities[modelID] = members
			modelEntityOrder = append(modelEntityOrder, modelID)
		}
		members[entityID] = true
	}
	for _, ent := range entities {
		targets := a.entityTargets(ent.ModelReference, ent, "model")
		ent.ModelReferenceConflicts = []string{}
		ent.ModelIDs = []string{}
		for _, p := range targets {
			if model, ok := modelPaths[a.key(p)]; ok && model.Index != ent.ModelIndex {
				ent.ModelReferenceConflicts = append(ent.ModelReferenceConflicts, p)
			}
		}
		for _, p := range targets {
			if model, ok := modelPaths[a.key(p)]; ok && model.Index == ent.ModelIndex {
				ent.ModelIDs = append(ent.ModelIDs, model.ID)
			}
		}
		ent.EffectTargets = []string{}
		effectReference := ent.EffectReference
		for _, p := range a.resolve(&effectReference, ent.Path) {
			ent.EffectTargets = append(ent.EffectTargets, p)
		}
		for _, modelID := range ent.ModelIDs {
			addModelEntity(modelID, ent.ID)
		}
		ent.ChildEntityIDs = []string{}
		for _, child := range ent.Children {
			for _, p := range a.entityTargets(child.Reference, ent, "entity") {
				if target, ok := entityPaths[a.key(p)]; ok {
					ent.ChildEntityIDs = append(ent.ChildEntityIDs, target.ID)
				}
			}
		}
	}

	// Parent entity membership is useful, but its exact path must be preserved.
	for range entities {
		changed := false
		for _, ent := range entities {
			for _, childID := range ent.ChildEntityIDs {
				for _, modelID := range modelEntityOrder {
					members := modelEntities[modelID]
					if members[childID] && !members[ent.ID] {
						members[ent.ID] = true
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}

	contexts := map[string]*Context{}
	var contextOrder []string
	occurrences := []*Occurrence{}
	occurrenceByKey := map[string]*Occurrence{}
	clipNumber := 0
	for i, path := range paths {
		if strings.ToLower(pypath.Suffix(path)) != ".owanimclip" {
			continue
		}
		relative := a.rel(path)
		var contentHash *string
		if item, ok := manifestByRel[relative]; ok {
			contentHash = item.SHA256
		}
		occurrence := &Occurrence{
			Path: path, RelativePath: relative, Filename: pypath.Name(path), Resource: ExportedResource(pypath.Name(path)),
			ContentHash: contentHash, Nickname: nickname(a.annotation.name(a.key(path))),
			ModelEvidence: []ModelEvidence{}, Contexts: []*Context{}, ParseErrors: []ParseError{},
		}
		relParts := strings.Split(relativeTo(path, root), string(filepath.Separator))
		for j := 0; j < len(relParts)-4; j++ {
			if strings.ToLower(relParts[j]) == "models" && strings.ToLower(relParts[j+2]) == "animations" {
				modelFolder := pypath.Join(root, relParts[:j+2]...)
				occurrence.Priority, occurrence.Group = nil, nil
				if isDecimal(relParts[j+3]) {
					occurrence.Priority = decimalValue(relParts[j+3])
				}
				if isDecimal(relParts[j+4]) {
					occurrence.Group = decimalValue(relParts[j+4])
				}
				for _, model := range models {
					if pathEqual(pypath.Parent(model.Path), modelFolder) {
						occurrence.ModelEvidence = append(occurrence.ModelEvidence, ModelEvidence{ModelID: model.ID, Kind: "explicit-export-path",
							Evidence: []Evidence{newEvidence("export-path", relative, "Models/<model>/Animations/<Priority>/<Group>",
								strings.Join(relParts[:j+5], "/"), "Explicit model folder; raw priority/group semantics remain unproven.")}})
					}
				}
			}
		}
		if context := a.namedContext(path, contexts, &contextOrder); context != nil {
			occurrence.Contexts = append(occurrence.Contexts, context)
		}
		if results[i].err == nil {
			occurrence.Clip = *results[i].clip
		} else {
			occurrence.Clip = ClipSummary{TrackDefinition: TrackDefinition, BoneRecordIDs: []string{}, AnimatedBoneIDs: []string{}}
			parseError := ParseError{path, relative, results[i].err.Error()}
			occurrence.ParseErrors = append(occurrence.ParseErrors, parseError)
			inventory.ParseErrors = append(inventory.ParseErrors, parseError)
		}
		occurrences = append(occurrences, occurrence)
		occurrenceByKey[a.key(path)] = occurrence
		if clipNumber%25 == 0 {
			a.progressf("Reading animation evidence: %d clips", clipNumber+1)
		}
		clipNumber++
	}

	byIndex := map[string][]*Occurrence{}
	for _, occ := range occurrences {
		if occ.Resource.Index != nil && *occ.Resource.Index != "" {
			byIndex[*occ.Resource.Index] = append(byIndex[*occ.Resource.Index], occ)
		}
	}
	for _, companion := range companions {
		owner := companion.Path
		targets := a.resolve(companion.EffectReference, owner)
		companion.EffectTargets = append([]string{}, targets...)
		companion.ReferenceMissing = companion.EffectReference != nil && *companion.EffectReference != "" && len(targets) == 0
		var clipTargets []*Occurrence
		for _, p := range a.resolve(companion.ClipReference, owner) {
			if occ, ok := occurrenceByKey[a.key(p)]; ok {
				clipTargets = append(clipTargets, occ)
			}
		}
		written := ""
		switch {
		case companion.EffectReference != nil && *companion.EffectReference != "":
			written = *companion.EffectReference
		case companion.ClipReference != nil && *companion.ClipReference != "":
			written = *companion.ClipReference
		}
		writtenStem := pypath.Stem(strings.ReplaceAll(written, `\`, "/"))
		// The written resource key and scoped path accompany the low32
		// header. Never attach a global low32 match when the target key is
		// absent.
		var modelCandidates []*ModelRow
		for _, p := range a.resolve(companion.ModelReference, owner) {
			if model, ok := modelPaths[a.key(p)]; ok {
				modelCandidates = append(modelCandidates, model)
			}
		}
		effectReference := ""
		if companion.EffectReference != nil {
			effectReference = *companion.EffectReference
		}
		refParts := strings.Split(strings.ReplaceAll(effectReference, `\`, "/"), "/")
		if len(modelCandidates) == 0 && len(refParts) >= 2 && strings.ToLower(refParts[0]) == "models" {
			// Resolve the model using the written folder even when the
			// effect target itself is absent; this is explicit path
			// evidence only.
			for _, model := range models {
				if pypath.Name(pypath.Parent(model.Path)) == refParts[1] {
					parentRef := strings.Join(refParts[:2], "/") + "/" + pypath.Name(model.Path)
					for _, p := range a.resolve(&parentRef, owner) {
						if pathEqual(p, model.Path) {
							modelCandidates = append(modelCandidates, model)
							break
						}
					}
				}
			}
		}
		var scoped []*Occurrence
		for _, model := range modelCandidates {
			modelFolder := pypath.Parent(model.Path)
			exportBase := pypath.Parent(pypath.Parent(modelFolder))
			animations := pypath.Join(modelFolder, "Animations")
			for _, o := range byIndex[companion.AnimationIndex] {
				parent := pypath.Parent(o.Path)
				if pypath.IsRelativeTo(o.Path, animations) || pathEqual(parent, pypath.Join(exportBase, "animations")) ||
					pathEqual(parent, pypath.Join(exportBase, "Animations")) {
					scoped = append(scoped, o)
				}
			}
		}
		candidates := clipTargets
		if len(candidates) == 0 {
			candidates = nil
			if hex12.MatchString(writtenStem) {
				for _, o := range scoped {
					if o.Resource.LongKey != nil && *o.Resource.LongKey == strings.ToUpper(writtenStem) {
						candidates = append(candidates, o)
					}
				}
			}
		}
		// Deduplicate by path, keeping the first position.
		var unique []*Occurrence
		seen := map[string]bool{}
		for _, o := range candidates {
			if !seen[o.Path] {
				seen[o.Path] = true
				unique = append(unique, o)
			}
		}
		companion.IdentityConflicts = []string{}
		var consistent []*Occurrence
		for _, o := range unique {
			if o.Resource.Index != nil && *o.Resource.Index != "" && *o.Resource.Index != companion.AnimationIndex {
				companion.IdentityConflicts = append(companion.IdentityConflicts, o.RelativePath)
				o.ReferenceConflicts = append(o.ReferenceConflicts, "Companion animation index disagrees with clip filename: "+companion.RelativePath)
			} else {
				consistent = append(consistent, o)
			}
		}
		// Path/index disagreements and multiple payloads stay unresolved.
		identities := map[[2]string]bool{}
		companion.CandidateOccurrences = []string{}
		for _, o := range consistent {
			identities[[2]string{nullable(o.Resource.LongKey), nullable(o.ContentHash)}] = true
			companion.CandidateOccurrences = append(companion.CandidateOccurrences, o.RelativePath)
		}
		companion.Ambiguous = len(identities) > 1 || len(modelCandidates) > 1 || len(clipTargets) > 1
		if companion.Ambiguous {
			continue
		}
		var reference any
		if companion.EffectReference != nil && *companion.EffectReference != "" {
			reference = *companion.EffectReference
		} else if companion.ModelReference != nil {
			reference = *companion.ModelReference
		}
		for _, occ := range consistent {
			for _, model := range modelCandidates {
				occ.ModelEvidence = append(occ.ModelEvidence, ModelEvidence{ModelID: model.ID, Kind: "entity-reference",
					Evidence: []Evidence{newEvidence("companion-reference", companion.RelativePath, "Animation.Index + model path",
						orderedPair{"index", companion.AnimationIndex, "reference", reference},
						"Scoped partial-index match and explicit model reference. Missing effect target does not prove an action.")}})
			}
		}
	}

	// Recover complete animation GUIDs only through an exact
	// effect->companion->clip path chain. Dangling references and basename
	// matches are not proof.
	companionPaths := map[string]*CompanionRow{}
	for _, c := range companions {
		companionPaths[a.key(c.Path)] = c
	}
	type proof struct {
		guid     evidence.GUID
		evidence Evidence
	}
	fullCandidates := map[string][]proof{}
	type effectOwner struct {
		effect *evidence.Effect
		owner  string
	}
	var allEffects []effectOwner
	for _, e := range effects {
		allEffects = append(allEffects, effectOwner{&e.Effect, e.Path})
	}
	for _, c := range companions {
		if c.Effect != nil {
			allEffects = append(allEffects, effectOwner{c.Effect, c.Path})
		}
	}
	type animationRef struct {
		kind      string
		guid      evidence.GUID
		reference string
	}
	for _, eo := range allEffects {
		var refs []animationRef
		for _, r := range eo.effect.DMCE {
			refs = append(refs, animationRef{"dmce", r.Animation, r.AnimationReference})
		}
		for _, r := range eo.effect.CECE {
			refs = append(refs, animationRef{"cece", r.Animation, r.AnimationReference})
		}
		for _, ref := range refs {
			guid := ref.guid
			if guid.FullGUID == "" || strings.Trim(guid.FullGUID, "0") == "" {
				continue
			}
			animationReference := ref.reference
			companionTargets := a.resolve(&animationReference, eo.owner)
			if len(companionTargets) != 1 {
				continue
			}
			for _, cp := range companionTargets {
				comp, ok := companionPaths[a.key(cp)]
				if !ok {
					continue
				}
				clipPaths := a.resolve(comp.ClipReference, cp)
				if len(clipPaths) != 1 {
					continue
				}
				for _, target := range clipPaths {
					occ, ok := occurrenceByKey[a.key(target)]
					if !ok {
						continue
					}
					if comp.AnimationIndex != guid.Index || guid.Type != "006" ||
						(occ.Resource.LongKey != nil && *occ.Resource.LongKey != "" && *occ.Resource.LongKey != guid.LongKey) {
						occ.ReferenceConflicts = append(occ.ReferenceConflicts, "Full GUID disagrees with clip key/type: "+a.rel(eo.owner))
						continue
					}
					fullCandidates[a.key(target)] = append(fullCandidates[a.key(target)], proof{guid,
						newEvidence("resource-reference", a.rel(eo.owner), strings.ToUpper(ref.kind)+".Animation", guid.FullGUID,
							"Exact reference chain: "+a.rel(eo.owner)+" -> "+a.rel(cp)+" -> "+a.rel(target))})
				}
			}
		}
	}
	for _, occ := range occurrences {
		proven := fullCandidates[a.key(occ.Path)]
		// identityConflicts aliases referenceConflicts in Python when present.
		occ.IdentityConflicts = occ.ReferenceConflicts
		if occ.IdentityConflicts == nil {
			occ.IdentityConflicts = []string{}
		}
		guids := map[string]bool{}
		for _, p := range proven {
			guids[p.guid.FullGUID] = true
		}
		if len(guids) == 1 {
			g := proven[0].guid
			occ.Resource.FullGUID = ptr(g.FullGUID)
			occ.Resource.LongKey = ptr(g.LongKey)
			occ.Resource.Type = g.Type
			occ.Resource.Index = ptr(g.Index)
			occ.Resource.Engine = ptr(g.Engine)
			occ.Resource.IsNull = ptr(g.IsNull)
			occ.Resource.Display = g.Display
			occ.Resource.Limitation = nil
			occ.IdentityEvidence = make([]Evidence, 0, len(proven))
			for _, p := range proven {
				occ.IdentityEvidence = append(occ.IdentityEvidence, p.evidence)
			}
		} else if len(proven) > 0 {
			occ.IdentityConflicts = append(occ.IdentityConflicts, "Multiple full GUID references reach this occurrence; identity remains unresolved.")
			if occ.ReferenceConflicts != nil {
				occ.ReferenceConflicts = occ.IdentityConflicts
			}
		}
	}

	grouped := map[string]*Asset{}
	var assets []*Asset
	for _, occ := range occurrences {
		assetID, err := localIdentity(a.rootKey, occ.Resource, occ.ContentHash, occ.RelativePath)
		if err != nil {
			return nil, err
		}
		occ.AssetID = assetID
		asset, ok := grouped[assetID]
		if !ok {
			resource := occ.Resource
			asset = &Asset{ID: assetID, Resource: resource, ContentHash: occ.ContentHash,
				SuggestedLabels: []SuggestedLabel{}, Statuses: []string{}, Models: []*ModelAssociation{},
				Contexts: []*Context{}, Evidence: []Evidence{}, Conflicts: []string{}, ParseErrors: []ParseError{},
				OccurrencePaths: []string{}, ClipSummary: occ.Clip}
			grouped[assetID] = asset
			assets = append(assets, asset)
		}
		asset.OccurrencePaths = append(asset.OccurrencePaths, occ.Path)
		asset.ParseErrors = append(asset.ParseErrors, occ.ParseErrors...)
		asset.Conflicts = append(asset.Conflicts, occ.IdentityConflicts...)
		asset.Evidence = append(asset.Evidence, occ.IdentityEvidence...)
		for _, ctx := range occ.Contexts {
			present := false
			for _, c := range asset.Contexts {
				present = present || c.ID == ctx.ID
			}
			if !present {
				asset.Contexts = append(asset.Contexts, ctx)
			}
		}
		for _, association := range occ.ModelEvidence {
			var present *ModelAssociation
			for _, m := range asset.Models {
				if m.ID == association.ModelID {
					present = m
					break
				}
			}
			if present != nil {
				present.Evidence = append(present.Evidence, association.Evidence...)
			} else {
				asset.Models = append(asset.Models, &ModelAssociation{ID: association.ModelID, Kind: association.Kind,
					Evidence: append([]Evidence{}, association.Evidence...)})
			}
		}
	}
	modelsByID := map[string]*ModelRow{}
	for _, m := range models {
		modelsByID[m.ID] = m
	}
	for _, asset := range assets {
		animated := asset.AnimatedBoneIDs
		asset.SkeletonComparisons = make([]SkeletonComparison, 0, len(models))
		for _, model := range models {
			skeleton := modelBones[model.ID]
			matched := 0
			missing := []string{}
			for _, bone := range animated {
				if skeleton[bone] {
					matched++
				} else {
					missing = append(missing, bone)
				}
			}
			comparison := SkeletonComparison{ModelID: model.ID, MatchedBoneCount: matched, AnimatedBoneCount: len(animated),
				SkeletonBoneCount: len(skeleton), MissingBoneIDs: missing,
				AllAnimatedBonesPresent: len(animated) > 0 && len(missing) == 0}
			asset.SkeletonComparisons = append(asset.SkeletonComparisons, comparison)
			if comparison.AllAnimatedBonesPresent {
				present := false
				for _, m := range asset.Models {
					present = present || m.ID == model.ID
				}
				if !present {
					asset.Models = append(asset.Models, &ModelAssociation{ID: model.ID, Kind: "compatible-bones", Evidence: []Evidence{
						newEvidence("bone-set-comparison", model.RelativePath, "bone names (skeleton.IDs, not positions)",
							orderedPair{"matched", matched, "animated", len(animated)},
							"All nonempty animated bone IDs occur in this skeleton. This is a candidate, not a proven use.")}})
				}
			}
		}
		for _, association := range asset.Models {
			model := modelsByID[association.ID]
			for _, c := range asset.SkeletonComparisons {
				if c.ModelID == model.ID {
					association.SkeletonComparison = c
					break
				}
			}
			association.Label = model.Name
			if association.Label == "" {
				association.Label = pypath.Stem(model.Path)
			}
			members := modelEntities[model.ID]
			association.EntityIDs = make([]string, 0, len(members))
			for id := range members {
				association.EntityIDs = append(association.EntityIDs, id)
			}
			sort.Strings(association.EntityIDs)
			association.EntityReferences = []EntityReference{}
			for _, e := range entities {
				if members[e.ID] {
					association.EntityReferences = append(association.EntityReferences,
						EntityReference{ID: e.ID, Name: e.Name, Index: e.Index, RelativePath: e.RelativePath})
				}
			}
		}
		var compatible []*ModelAssociation
		for _, m := range asset.Models {
			if m.Kind == "compatible-bones" {
				compatible = append(compatible, m)
			}
		}
		if len(compatible) > 0 {
			for _, m := range compatible {
				asset.SuggestedLabels = append(asset.SuggestedLabels, SuggestedLabel{Value: "Compatible skeleton: " + m.Label,
					Provenance: "Measured animated bone IDs; action unknown", ModelID: m.ID})
			}
			asset.Statuses = append(asset.Statuses, "suggested")
		}
		if len(asset.Models) > 1 {
			asset.Conflicts = append(asset.Conflicts, "Multiple models are supported or compatible; no single model is assigned.")
		}
		if len(asset.Contexts) > 0 {
			asset.Statuses = append(asset.Statuses, "referenced-by-named-context")
		}
		// Unknown describes unresolved action/source naming, independently
		// of known object associations and any non-action skeleton
		// suggestions.
		if (asset.SourceName == nil || *asset.SourceName == "") && len(asset.Contexts) == 0 {
			asset.Statuses = append(asset.Statuses, "unknown")
		}
		if asset.SourceName != nil && *asset.SourceName != "" {
			asset.Statuses = append(asset.Statuses, "source-name-found")
		}
	}
	for _, companion := range companions {
		if !companion.Ambiguous {
			continue
		}
		candidates := map[string]bool{}
		for _, relative := range companion.CandidateOccurrences {
			candidates[relative] = true
		}
		for _, occ := range occurrences {
			if candidates[occ.RelativePath] {
				asset := grouped[occ.AssetID]
				asset.Conflicts = append(asset.Conflicts, "Ambiguous partial animation index in "+companion.RelativePath+"; association withheld.")
			}
		}
	}
	if assets == nil {
		assets = []*Asset{}
	}
	inventory.Assets = assets
	inventory.Occurrences = occurrences
	inventory.Contexts = make([]*Context, 0, len(contextOrder))
	for _, id := range contextOrder {
		inventory.Contexts = append(inventory.Contexts, contexts[id])
	}
	hashes := map[string]bool{}
	for _, o := range occurrences {
		if o.ContentHash != nil && *o.ContentHash != "" {
			hashes[*o.ContentHash] = true
		}
	}
	sortedHashes := make([]string, 0, len(hashes))
	for h := range hashes {
		sortedHashes = append(sortedHashes, h)
	}
	sort.Strings(sortedHashes)
	inventory.ContentGroups = make([]ContentGroup, 0, len(sortedHashes))
	for _, h := range sortedHashes {
		group := ContentGroup{SHA256: h, AssetIDs: []string{}, Occurrences: []string{}}
		for _, asset := range assets {
			if asset.ContentHash != nil && *asset.ContentHash == h {
				group.AssetIDs = append(group.AssetIDs, asset.ID)
			}
		}
		for _, o := range occurrences {
			if o.ContentHash != nil && *o.ContentHash == h {
				group.Occurrences = append(group.Occurrences, o.RelativePath)
			}
		}
		inventory.ContentGroups = append(inventory.ContentGroups, group)
	}
	coverage := Coverage{Definition: coverageDefinition}
	for _, asset := range assets {
		if asset.SourceName != nil && *asset.SourceName != "" {
			coverage.SourceNames++
		}
		if len(asset.Contexts) > 0 {
			coverage.NamedContexts++
		}
		if len(asset.SuggestedLabels) > 0 {
			coverage.Suggestions++
		}
		for _, status := range asset.Statuses {
			if status == "unknown" {
				coverage.Unknown++
				break
			}
		}
		for _, m := range asset.Models {
			if m.Kind != "compatible-bones" {
				coverage.ExplicitModelAssociations++
				break
			}
		}
		for _, c := range asset.SkeletonComparisons {
			if c.AllAnimatedBonesPresent {
				coverage.CompatibleSkeletons++
				break
			}
		}
	}
	if labelsAvailable {
		labels := 0
		for _, o := range occurrences {
			if o.Nickname != nil {
				labels++
			}
		}
		coverage.UserLabels = &labels
	}
	inventory.Coverage = coverage
	inventory.AssetCount, inventory.OccurrenceCount = len(assets), len(occurrences)
	inventory.ParserVersions = ParserToken()
	revision, err := revisionDigest(inventory.Manifest, annotations.raw, installed, labelsAvailable)
	if err != nil {
		return nil, err
	}
	inventory.Revision = revision
	return inventory, nil
}

func revisionDigest(manifest []ManifestItem, annotations, installed json.RawMessage, labelsAvailable bool) (string, error) {
	return pyjson.Digest([]any{ParserToken(), manifest, annotations, installed, labelsAvailable})
}

func nullable(value *string) string {
	if value == nil {
		return "\x00null"
	}
	return *value
}

// orderedPair marshals a two-key object in the given key order.
type orderedPair struct {
	key1   string
	value1 any
	key2   string
	value2 any
}

func (p orderedPair) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range [][2]any{{p.key1, p.value1}, {p.key2, p.value2}} {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(kv[0])
		value, err := json.Marshal(kv[1])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Summarize is index_summary(inventory).
func Summarize(inventory *Inventory) *IndexSummary {
	assets := make(map[string]*Asset, len(inventory.Assets))
	for _, a := range inventory.Assets {
		assets[a.ID] = a
	}
	summary := &IndexSummary{Root: inventory.Root, Revision: inventory.Revision, Coverage: inventory.Coverage,
		AssetCount: inventory.AssetCount, OccurrenceCount: inventory.OccurrenceCount,
		Entries: make([]IndexEntry, 0, len(inventory.Occurrences)), Models: make([]IDLabel, 0, len(inventory.Models)),
		Contexts: make([]IDLabel, 0, len(inventory.Contexts))}
	for _, o := range inventory.Occurrences {
		asset := assets[o.AssetID]
		entry := IndexEntry{Path: o.Path, AssetID: o.AssetID, ModelIDs: []string{}, ContextIDs: []string{}, Statuses: []string{}}
		if asset != nil {
			for _, m := range asset.Models {
				entry.ModelIDs = append(entry.ModelIDs, m.ID)
			}
			entry.Statuses = asset.Statuses
		}
		for _, c := range o.Contexts {
			entry.ContextIDs = append(entry.ContextIDs, c.ID)
		}
		summary.Entries = append(summary.Entries, entry)
	}
	for _, m := range inventory.Models {
		label := m.Name
		if label == "" {
			label = pypath.Stem(m.Path)
		}
		summary.Models = append(summary.Models, IDLabel{ID: m.ID, Label: label + " · " + m.RelativePath})
	}
	for _, c := range inventory.Contexts {
		summary.Contexts = append(summary.Contexts, IDLabel{ID: c.ID, Label: c.Category + " · " + c.Label})
	}
	return summary
}

// Encode returns json.dumps(inventory, ensure_ascii=False, indent=2).
func Encode(inventory *Inventory) ([]byte, error) { return pyjson.Indented(inventory) }

// WriteJSON streams json.dumps(inventory, ensure_ascii=False, indent=2) to w.
func WriteJSON(w io.Writer, inventory *Inventory) error {
	return pyjson.Write(w, inventory, pyjson.Options{Indent: 2})
}

// WriteFile writes the inventory to path like Path.write_text(json.dumps(
// inventory, ensure_ascii=False, indent=2), encoding='utf-8').
func WriteFile(path string, inventory *Inventory) error {
	file, err := os.Create(path)
	if err != nil {
		return formats.PythonOSError(err)
	}
	if err := WriteJSON(file, inventory); err != nil {
		file.Close()
		return formats.PythonOSError(err)
	}
	return formats.PythonOSError(file.Close())
}
