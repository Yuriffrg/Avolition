package analysis

import (
	"encoding/json"

	"avolition/gocore/internal/evidence"
	"avolition/gocore/internal/pyjson"
)

// The types below are the inventory's dicts. Field order is Python's key
// order (including keys Python adds later with update/setdefault), so
// pyjson.Indented writes the same bytes as json.dumps(..., indent=2).
// Values whose shape varies in Python (evidence values, the installed
// toolchain, nickname values) are raw JSON so they survive a cache round
// trip unchanged.

// ManifestItem is one snapshot entry: size/mtimeNs/sha256, or error.
type ManifestItem struct {
	RelativePath string  `json:"relativePath"`
	Size         *int64  `json:"size,omitempty"`
	MtimeNs      *string `json:"mtimeNs,omitempty"`
	SHA256       *string `json:"sha256,omitempty"`
	Error        *string `json:"error,omitempty"`
}

// Snapshot is snapshot()'s (paths, manifest).
type Snapshot struct {
	Paths    []string
	Manifest []ManifestItem
}

// ParseError is one file that could not be parsed.
type ParseError struct {
	Path         string `json:"path"`
	RelativePath string `json:"relativePath"`
	Error        string `json:"error"`
}

// Evidence is evidence(kind, source, field, value, description).
type Evidence struct {
	Kind        string          `json:"kind"`
	Source      string          `json:"source"`
	Field       string          `json:"field"`
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description"`
}

// Row keys analyze() adds to every parsed file.
type rowKeys struct {
	RelativePath string  `json:"relativePath"`
	SHA256       *string `json:"sha256"`
	ID           string  `json:"id"`
}

// ModelRow is a parsed .owmdl.
type ModelRow struct {
	evidence.Model
	rowKeys
}

// EntityRow is a parsed .owentity with its resolved references.
type EntityRow struct {
	evidence.Entity
	rowKeys
	ModelReferenceConflicts []string `json:"modelReferenceConflicts"`
	ModelIDs                []string `json:"modelIds"`
	EffectTargets           []string `json:"effectTargets"`
	ChildEntityIDs          []string `json:"childEntityIds"`
}

// CompanionRow is a parsed .owanim with its resolution results.
type CompanionRow struct {
	evidence.Companion
	rowKeys
	EffectTargets        []string `json:"effectTargets"`
	ReferenceMissing     bool     `json:"referenceMissing"`
	IdentityConflicts    []string `json:"identityConflicts"`
	CandidateOccurrences []string `json:"candidateOccurrences"`
	Ambiguous            bool     `json:"ambiguous"`
}

// EffectRow is a parsed .oweffect.
type EffectRow struct {
	evidence.Effect
	rowKeys
}

// Resource is an occurrence's (and asset's) resource identity. Engine and
// IsNull exist only once a full GUID was proven (Python's dict.update).
type Resource struct {
	FullGUID       *string `json:"fullGuid"`
	LongKey        *string `json:"longKey"`
	Index          *string `json:"index"`
	Type           string  `json:"type"`
	TypeProvenance string  `json:"typeProvenance"`
	Display        string  `json:"display"`
	Limitation     *string `json:"limitation"`
	Engine         *string `json:"engine,omitempty"`
	IsNull         *bool   `json:"isNull,omitempty"`
}

// Nickname is a user annotation shown with an occurrence.
type Nickname struct {
	Value      any    `json:"value"`
	Provenance string `json:"provenance"`
}

// Context is a named unlock folder.
type Context struct {
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Category string     `json:"category"`
	Kind     string     `json:"kind"`
	Role     string     `json:"role"`
	Evidence []Evidence `json:"evidence"`
}

// ModelEvidence associates an occurrence with a model.
type ModelEvidence struct {
	ModelID  string     `json:"modelId"`
	Kind     string     `json:"kind"`
	Evidence []Evidence `json:"evidence"`
}

// ClipSummary is the clip information of an occurrence; assets repeat it.
// Warnings and Version exist only when the clip loaded.
type ClipSummary struct {
	DurationSeconds       *pyjson.Float `json:"durationSeconds"`
	FPS                   *pyjson.Float `json:"fps"`
	TrackCount            *int          `json:"trackCount"`
	TrackDefinition       string        `json:"trackDefinition"`
	TransformChannelCount *int          `json:"transformChannelCount"`
	BoneRecordIDs         []string      `json:"boneRecordIds"`
	AnimatedBoneIDs       []string      `json:"animatedBoneIds"`
	Warnings              *[]string     `json:"warnings,omitempty"`
	Version               *string       `json:"version,omitempty"`
}

// Occurrence is one .owanimclip file.
type Occurrence struct {
	Path               string          `json:"path"`
	RelativePath       string          `json:"relativePath"`
	Filename           string          `json:"filename"`
	Resource           Resource        `json:"resource"`
	ContentHash        *string         `json:"contentHash"`
	Nickname           *Nickname       `json:"nickname"`
	Priority           *json.Number    `json:"priority"`
	Group              *json.Number    `json:"group"`
	ModelEvidence      []ModelEvidence `json:"modelEvidence"`
	Contexts           []*Context      `json:"contexts"`
	ParseErrors        []ParseError    `json:"parseErrors"`
	Clip               ClipSummary     `json:"clip"`
	ReferenceConflicts []string        `json:"referenceConflicts,omitempty"`
	IdentityConflicts  []string        `json:"identityConflicts"`
	IdentityEvidence   []Evidence      `json:"identityEvidence,omitempty"`
	AssetID            string          `json:"assetId"`
}

// SkeletonComparison compares an asset's animated bones with one skeleton.
type SkeletonComparison struct {
	ModelID                 string   `json:"modelId"`
	MatchedBoneCount        int      `json:"matchedBoneCount"`
	AnimatedBoneCount       int      `json:"animatedBoneCount"`
	SkeletonBoneCount       int      `json:"skeletonBoneCount"`
	MissingBoneIDs          []string `json:"missingBoneIds"`
	AllAnimatedBonesPresent bool     `json:"allAnimatedBonesPresent"`
}

// EntityReference names an entity using a model.
type EntityReference struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Index        string `json:"index"`
	RelativePath string `json:"relativePath"`
}

// ModelAssociation is one of an asset's models, with its comparison.
type ModelAssociation struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Evidence []Evidence `json:"evidence"`
	SkeletonComparison
	Label            string            `json:"label"`
	EntityIDs        []string          `json:"entityIds"`
	EntityReferences []EntityReference `json:"entityReferences"`
}

// SuggestedLabel is a non-action label candidate.
type SuggestedLabel struct {
	Value      string `json:"value"`
	Provenance string `json:"provenance"`
	ModelID    string `json:"modelId"`
}

// Asset groups occurrences of one local identity.
type Asset struct {
	ID              string              `json:"id"`
	Resource        Resource            `json:"resource"`
	ContentHash     *string             `json:"contentHash"`
	SourceName      *string             `json:"sourceName"`
	SuggestedLabels []SuggestedLabel    `json:"suggestedLabels"`
	Statuses        []string            `json:"statuses"`
	GameBuild       *string             `json:"gameBuild"`
	DataToolVersion *string             `json:"dataToolVersion"`
	Models          []*ModelAssociation `json:"models"`
	Contexts        []*Context          `json:"contexts"`
	Evidence        []Evidence          `json:"evidence"`
	Conflicts       []string            `json:"conflicts"`
	ParseErrors     []ParseError        `json:"parseErrors"`
	OccurrencePaths []string            `json:"occurrencePaths"`
	ClipSummary
	SkeletonComparisons []SkeletonComparison `json:"skeletonComparisons"`
}

// ContentGroup lists the assets and occurrences sharing bytes.
type ContentGroup struct {
	SHA256      string   `json:"sha256"`
	AssetIDs    []string `json:"assetIds"`
	Occurrences []string `json:"occurrences"`
}

// Coverage counts assets per evidence category.
type Coverage struct {
	SourceNames               int    `json:"sourceNames"`
	NamedContexts             int    `json:"namedContexts"`
	UserLabels                *int   `json:"userLabels"`
	UserVerified              int    `json:"userVerified"`
	Suggestions               int    `json:"suggestions"`
	Unknown                   int    `json:"unknown"`
	ExplicitModelAssociations int    `json:"explicitModelAssociations"`
	CompatibleSkeletons       int    `json:"compatibleSkeletons"`
	Definition                string `json:"definition"`
}

// Inventory is analyze()'s result and the persisted animation-index.json.
type Inventory struct {
	Schema              int               `json:"schema"`
	ParserVersion       string            `json:"parserVersion"`
	Root                string            `json:"root"`
	AnalyzedAt          string            `json:"analyzedAt"`
	GameBuild           *string           `json:"gameBuild"`
	DataToolVersion     *string           `json:"dataToolVersion"`
	InstalledToolchain  json.RawMessage   `json:"installedToolchain"`
	SourceNames         []json.RawMessage `json:"sourceNames"`
	Models              []*ModelRow       `json:"models"`
	Entities            []*EntityRow      `json:"entities"`
	Companions          []*CompanionRow   `json:"companions"`
	Effects             []*EffectRow      `json:"effects"`
	Assets              []*Asset          `json:"assets"`
	Occurrences         []*Occurrence     `json:"occurrences"`
	ParseErrors         []ParseError      `json:"parseErrors"`
	Limitations         []string          `json:"limitations"`
	Manifest            []ManifestItem    `json:"manifest"`
	UserLabelsAvailable bool              `json:"userLabelsAvailable"`
	ProvenanceFiles     []ManifestItem    `json:"provenanceFiles"`
	Contexts            []*Context        `json:"contexts"`
	ContentGroups       []ContentGroup    `json:"contentGroups"`
	Coverage            Coverage          `json:"coverage"`
	AssetCount          int               `json:"assetCount"`
	OccurrenceCount     int               `json:"occurrenceCount"`
	ParserVersions      []string          `json:"parserVersions"`
	Revision            string            `json:"revision"`
}

// IndexEntry is one occurrence in the index summary.
type IndexEntry struct {
	Path       string   `json:"path"`
	AssetID    string   `json:"assetId"`
	ModelIDs   []string `json:"modelIds"`
	ContextIDs []string `json:"contextIds"`
	Statuses   []string `json:"statuses"`
}

// IDLabel is an id with a display label.
type IDLabel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// IndexSummary is index_summary()'s result.
type IndexSummary struct {
	Root            string       `json:"root"`
	Revision        string       `json:"revision"`
	Coverage        Coverage     `json:"coverage"`
	AssetCount      int          `json:"assetCount"`
	OccurrenceCount int          `json:"occurrenceCount"`
	Entries         []IndexEntry `json:"entries"`
	Models          []IDLabel    `json:"models"`
	Contexts        []IDLabel    `json:"contexts"`
}
