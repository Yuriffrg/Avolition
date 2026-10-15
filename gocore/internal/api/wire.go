package api

import (
	"encoding/json"

	"avolition/gocore/internal/analysis"
	"avolition/gocore/internal/bonecounts"
	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/clipbindings"
	"avolition/gocore/internal/datatool"
	"avolition/gocore/internal/duplicates"
	"avolition/gocore/internal/exportbundle"
	"avolition/gocore/internal/exporttextures"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/mapexport"
	"avolition/gocore/internal/mapformats"
	_ "avolition/gocore/internal/materials" // load_asset's real materials
	"avolition/gocore/internal/objectnames"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/textures"
	"avolition/gocore/internal/toolchain"
)

// Default returns the ported modules. Fields still nil are modules that have
// not been ported yet.
func Default() Deps {
	return Deps{
		LoadAsset:      formats.LoadAsset,
		LoadClip:       formats.LoadClip,
		ReadClipHeader: formats.ReadClipHeader,
		LoadMap:        mapformats.LoadMap,
		ValidatePaths: func(install, output string) datatool.Validation {
			return datatool.ValidatePaths(install, output)
		},
		NewTextures:   func() TextureStore { return textureStore{textures.NewStore()} },
		NewDuplicates: func() DuplicateIndex { return duplicateIndex{duplicates.New(duplicates.DefaultMaxEntries)} },
		BoneCounts: func(root string, paths []string) []bonecounts.Metadata {
			return bonecounts.New(root, paths).MetadataAll(paths)
		},
		NewClipBindings: func() ClipBindings { return clipBindings{clipbindings.NewDefault()} },
		NewBridge:       func(root string) Bridge { return &bridgeClient{c4dbridge.NewClient(root)} },
		NewAnalysis:     newAnalysis,
		BakeMaterials: func(asset *formats.Asset, folder string) (Materials, []string, error) {
			result, err := exporttextures.BakeMaterials(asset, folder, "", nil)
			if err != nil {
				return nil, nil, err
			}
			return result.Materials, result.Warnings, nil
		},
		ExportMap: func(asset *formats.Asset, folder, name string, options mapexport.Options) (*exportbundle.Result, error) {
			return mapexport.ExportMap(asset, folder, &name, options)
		},
		ObjectNames: objectNames{},
		NewToolchain: func(root string, activity toolchain.ActivityLock) Toolchain {
			return toolchain.NewManager(root, toolchain.Options{ActivityLock: activity})
		},
		NewDataTool: func(stateDir string, activity datatool.ActivityLock) DataTool {
			return datatool.NewJobs(stateDir, activity, nil)
		},
	}
}

type textureStore struct{ store *textures.Store }

func (t textureStore) Register(specs *ordered.Map[*formats.Texture], owner string) error {
	converted := map[string]textures.Spec{}
	if specs != nil {
		specs.Range(func(id string, texture *formats.Texture) bool {
			if texture != nil {
				converted[id] = textures.Spec{Path: texture.Path, Channel: texture.Channel,
					Transform: texture.Transform, InputColorSpace: texture.InputColorSpace}
			}
			return true
		})
	}
	return t.store.Register(converted, owner)
}

func (t textureStore) Release(owner string) error { return t.store.Release(owner) }

func (t textureStore) Load(textureID, maxEdge any) (any, error) {
	rawID, err := json.Marshal(textureID)
	if err != nil {
		return nil, err
	}
	rawEdge, err := json.Marshal(maxEdge)
	if err != nil {
		return nil, err
	}
	result, err := t.store.LoadJSON(rawID, rawEdge)
	if err != nil {
		return nil, err
	}
	return textureData{result}, nil
}

type duplicateIndex struct{ index *duplicates.Index }

func (d duplicateIndex) Annotate(clips []*ClipEntry) []string {
	rows := make([]duplicates.Clip, len(clips))
	for i, clip := range clips {
		if clip.Header != nil {
			h := clip.Header
			rows[i] = duplicates.Clip{Path: h.Path, Size: h.Size, Version: h.Version, FPS: h.FPS,
				DurationFrames: h.DurationFrames, BoneCount: int64(h.BoneCount)}
		} else {
			rows[i] = duplicates.Clip{Path: clip.Path, Size: clip.Size, Error: clip.ErrorText}
		}
	}
	annotations, warnings := d.index.Annotate(rows)
	for i, annotation := range annotations {
		clips[i].DuplicateKey, clips[i].DuplicateCount = annotation.DuplicateKey, annotation.DuplicateCount
		clips[i].DuplicateError = nil
		if annotation.DuplicateError != "" {
			text := annotation.DuplicateError
			clips[i].DuplicateError = &text
		}
	}
	return warnings
}

type clipBindings struct{ index *clipbindings.Index }

func (c clipBindings) Read(paths any) (any, error) { return c.index.Read(paths) }

type bridgeClient struct{ client *c4dbridge.Client }

func (b *bridgeClient) Root() string              { return b.client.Root }
func (b *bridgeClient) Status() BridgeStatus      { return b.client.Status() }
func (b *bridgeClient) Client() *c4dbridge.Client { return b.client }

// MapBridge is the client itself (*c4dbridge.Client satisfies mapexport.Bridge).
func (b *bridgeClient) MapBridge() mapexport.Bridge { return b.client }

func (b *bridgeClient) Export(payload any, destination, textureFolder string) ([]string, error) {
	raw, err := b.client.Export(payload, destination, textureFolder, c4dbridge.ExportOptions{})
	if err != nil {
		return nil, err
	}
	return warningsOf(raw), nil
}

// warningsOf is result.get('warnings', []) of a bridge result.
func warningsOf(raw json.RawMessage) []string {
	var result struct {
		Warnings []any `json:"warnings"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	var out []string
	for _, w := range result.Warnings {
		if text, ok := w.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

type analysisManager struct{ manager *analysis.Manager }

func newAnalysis(cacheDir string, hooks AnalysisHooks) Analysis {
	return &analysisManager{analysis.NewManager(cacheDir, analysis.ManagerOptions{
		Annotations: func() any { return hooks.Annotations() },
		Installed:   hooks.Installed,
		Excluded: func() []string {
			var out []string
			for _, item := range hooks.Excluded() {
				if text, ok := item.(string); ok {
					out = append(out, text)
				}
			}
			return out
		},
		LabelsAvailable: hooks.LabelsAvailable,
	})}
}

func (m *analysisManager) Start(root any) (any, error) {
	text, err := pathOf(root)
	if err != nil {
		return nil, err
	}
	return m.manager.Start(text, false)
}

func (m *analysisManager) Status(root any) (any, error) {
	text, err := pathOf(root)
	if err != nil {
		return nil, err
	}
	return m.manager.Status(text, true)
}

func (m *analysisManager) Index(root any) (any, error) {
	text, err := pathOf(root)
	if err != nil {
		return nil, err
	}
	if summary := m.manager.Index(text); summary != nil {
		return summary, nil
	}
	return nil, nil
}

func (m *analysisManager) Detail(path, root any) (any, error) {
	text, err := pathOf(path)
	if err != nil {
		return nil, err
	}
	var rootText *string
	if root != nil {
		value, err := pathOf(root)
		if err != nil {
			return nil, err
		}
		rootText = &value
	}
	if detail := m.manager.Detail(text, rootText); detail != nil {
		return detail, nil
	}
	return nil, nil
}

type objectNames struct{}

func (objectNames) ValidateName(name any) (string, error) { return objectnames.ValidateName(name) }
func (objectNames) ValidateNames(names any, asset *formats.Asset) (*ordered.Map[string], error) {
	return objectnames.ValidateNames(names, asset)
}
func (objectNames) Keys(asset *formats.Asset) (map[string]bool, error) {
	return objectnames.ObjectNameKeys(asset)
}
func (objectNames) Apply(asset *formats.Asset, names any) (*formats.Asset, error) {
	return objectnames.ApplyObjectNames(asset, names)
}
