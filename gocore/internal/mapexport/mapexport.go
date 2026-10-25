// Package mapexport ports owpreview/map_export.py: static maps through
// Avolition's existing transactional native C4D bridge.
//
//	_scene_name           SceneName
//	make_static_payload   MakeStaticPayload
//	_bake_map_materials   bakeMapMaterials
//	export_map            ExportMap
//
// The c4d_export/c4d_scene helpers it imports are the c4dexport package's
// NormalizeMeshLists/NormalizedNormalAsset, OrderedGraph and AssetModels.
package mapexport

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf16"

	"avolition/gocore/internal/c4dbridge"
	"avolition/gocore/internal/c4dexport"
	"avolition/gocore/internal/exportbundle"
	"avolition/gocore/internal/exporttextures"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
	"avolition/gocore/internal/pypath"
)

// Materials is model id -> material key -> descriptor (the payload's
// materials, c4dexport.Materials).
type Materials = c4dexport.Materials

// Progress receives stage messages; completed/total are nil for messages
// without counts (Python's progress(message) calls).
type Progress func(message string, completed, total *int)

// Bridge is the part of c4dbridge.Client export_map uses (tests substitute
// a mock, as the Python tests do).
type Bridge interface {
	Status() *c4dbridge.Status
	Export(payload any, destination, textureFolder string, options c4dbridge.ExportOptions) (json.RawMessage, error)
}

// Animation is the static payload's animation: no tracks.
type Animation struct {
	FPS       int   `json:"fps"`
	LastFrame int   `json:"lastFrame"`
	Tracks    []any `json:"tracks"`
}

// StaticPayload is make_static_payload's dict, in Python's key order.
type StaticPayload struct {
	Version      int            `json:"version"`
	Asset        *formats.Asset `json:"asset"`
	Name         string         `json:"name"`
	Materials    *Materials     `json:"materials"`
	IncludeMesh  bool           `json:"includeMesh"`
	MapInstances bool           `json:"mapInstances"`
	Animation    Animation      `json:"animation"`
	Warnings     []string       `json:"warnings"`
}

func (p *StaticPayload) RequiredBridgeCapabilities() []string { return []string{"mapTiming24"} }

// MarshalJSON writes the asset with formats.AppendAssetJSON (no reflection
// over the geometry arrays).
func (p *StaticPayload) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("null"), nil
	}
	dst := []byte(`{"version":`)
	dst = appendJSON(dst, p.Version)
	dst = append(dst, `,"asset":`...)
	var err error
	if dst, err = formats.AppendAssetJSON(dst, p.Asset); err != nil {
		return nil, err
	}
	materials := p.Materials
	if materials == nil {
		materials = ordered.New[*ordered.Map[*formats.Material]]()
	}
	for _, part := range []struct {
		key   string
		value any
	}{{"name", p.Name}, {"materials", materials}, {"includeMesh", p.IncludeMesh},
		{"mapInstances", p.MapInstances}, {"mapTiming24", true}, {"animation", p.Animation}, {"warnings", p.Warnings}} {
		dst = append(dst, ',')
		dst = appendJSON(dst, part.key)
		dst = append(dst, ':')
		encoded, err := c4dbridge.Marshal(part.value)
		if err != nil {
			return nil, err
		}
		dst = append(dst, encoded...)
	}
	return append(dst, '}'), nil
}

func appendJSON(dst []byte, value any) []byte {
	encoded, _ := c4dbridge.Marshal(value)
	return append(dst, encoded...)
}

var (
	invalidName  = regexp.MustCompile("[<>:\"/\\\\|?*\x00-\x1f]")
	reservedName = regexp.MustCompile(`^(?i:CON|PRN|AUX|NUL|COM[1-9¹²³]|LPT[1-9¹²³])$`)
)

// SceneName is _scene_name: a map scene name Windows can use as a filename
// (a trailing ".c4d" is removed).
func SceneName(value string) (string, error) {
	name := pycompat.Strip(value)
	runes := []rune(name)
	if len(runes) >= 4 && strings.ToLower(string(runes[len(runes)-4:])) == ".c4d" {
		name = string(runes[:len(runes)-4])
	}
	if name == "" || name == "." || name == ".." || invalidName.MatchString(name) ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") ||
		reservedName.MatchString(strings.SplitN(name, ".", 2)[0]) {
		return "", errors.New("Enter a map scene name that Windows can use as a filename.")
	}
	if len(utf16.Encode([]rune(name))) > 180 {
		return "", errors.New("Keep the map scene filename within 180 characters.")
	}
	return name, nil
}

func isMap(asset *formats.Asset) bool {
	if asset == nil || asset.Map == nil {
		return false
	}
	value := reflect.ValueOf(asset.Map)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return false
	}
	switch value.Kind() {
	case reflect.Map, reflect.Slice, reflect.String:
		return value.Len() > 0
	}
	return pycompat.Truthy(asset.Map)
}

// MakeStaticPayload is make_static_payload(asset, materials, name): no
// inferred animation; exact map placement and rest attachments are kept.
//
// Compact geometry stays compact over IPC. Normalization is performed once
// per prototype, not once for every placement of the same model. The asset
// is not modified; the payload's asset shares unchanged data with it.
func MakeStaticPayload(asset *formats.Asset, materials *Materials, name string) (*StaticPayload, error) {
	if !isMap(asset) {
		return nil, errors.New("Load a map before exporting a static map scene.")
	}
	if _, err := c4dexport.OrderedGraph(asset.ExportHierarchy); err != nil {
		return nil, err
	}
	resolved, err := c4dexport.AssetModels(asset)
	if err != nil {
		return nil, err
	}
	hasMeshes := false
	resolved.Range(func(_ string, model *formats.Model) bool {
		hasMeshes = len(model.Meshes) > 0
		return !hasMeshes
	})
	if !hasMeshes {
		return nil, errors.New("The loaded map has no meshes to export.")
	}
	var prepared *formats.Asset
	if asset.MeshPrototypes != nil {
		keys := asset.MeshPrototypes.Keys()
		lists := make([][]*formats.Mesh, len(keys))
		for i, key := range keys {
			if prototype, _ := asset.MeshPrototypes.Get(key); prototype != nil {
				lists[i] = prototype.Meshes
			}
		}
		normalized, warnings, err := c4dexport.NormalizeMeshLists(lists, asset.Warnings)
		if err != nil {
			return nil, err
		}
		prototypes := ordered.New[*formats.MeshPrototype]()
		for i, key := range keys {
			prototypes.Set(key, &formats.MeshPrototype{Meshes: normalized[i]})
		}
		copied := *asset
		copied.MeshPrototypes = prototypes
		copied.Warnings = warnings
		prepared = &copied
	} else if prepared, err = c4dexport.NormalizedNormalAsset(asset); err != nil {
		return nil, err
	}
	if name == "" {
		name = asset.Name
	}
	if materials == nil {
		materials = ordered.New[*ordered.Map[*formats.Material]]()
	}
	return &StaticPayload{Version: 1, Asset: prepared, Name: name, Materials: materials, IncludeMesh: true,
		MapInstances: true, Animation: Animation{FPS: 24, Tracks: []any{}},
		Warnings: slices.Clone(prepared.Warnings)}, nil
}

// bakeMapMaterials is _bake_map_materials. One appearance may occur
// thousands of times. The map adapter preserves shared immutable material
// maps (as does objectnames.ApplyObjectNames), so models whose Materials is
// the same map are baked once and share the result.
func bakeMapMaterials(asset *formats.Asset, textureFolder string, progress exporttextures.Progress) (*exporttextures.Result, error) {
	var representatives []*formats.Model
	appearances := map[*ordered.Map[*formats.Material]]string{}
	aliases := make([]string, len(asset.Models))
	for i, model := range asset.Models {
		key := model.Materials
		if _, ok := appearances[key]; !ok {
			appearances[key] = model.ID
			representatives = append(representatives, model)
		}
		aliases[i] = appearances[key]
	}
	copied := *asset
	copied.Models = representatives
	baked, err := exporttextures.BakeMaterials(&copied, textureFolder, "", progress)
	if err != nil {
		return nil, err
	}
	materials := ordered.New[*ordered.Map[*formats.Material]]()
	for i, model := range asset.Models {
		value, ok := baked.Materials.Get(aliases[i])
		if !ok || value == nil {
			value = ordered.New[*formats.Material]()
		}
		materials.Set(model.ID, value)
	}
	baked.Materials = materials
	return baked, nil
}

// unbakedMaterials is the bake_textures=False branch: every material with
// its maps removed.
func unbakedMaterials(asset *formats.Asset) *Materials {
	materials := ordered.New[*ordered.Map[*formats.Material]]()
	stripped := map[*formats.Material]*formats.Material{}
	for _, model := range asset.Models {
		out := ordered.New[*formats.Material]()
		if model.Materials != nil {
			model.Materials.Range(func(key string, value *formats.Material) bool {
				if value == nil {
					out.Set(key, nil)
					return true
				}
				copied, ok := stripped[value]
				if !ok {
					shallow := *value
					shallow.Maps = ordered.New[*formats.MaterialMap]()
					copied = &shallow
					stripped[value] = copied
				}
				out.Set(key, copied)
				return true
			})
		}
		materials.Set(model.ID, out)
	}
	return materials
}

// Options are export_map's keyword arguments.
type Options struct {
	// BakeTextures is bake_textures (default true).
	BakeTextures bool
	// Bridge defaults to c4dbridge.NewClient("").
	Bridge Bridge
	// Progress may be nil.
	Progress Progress
}

// DefaultOptions has bake_textures=True.
func DefaultOptions() Options { return Options{BakeTextures: true} }

// BakeTexturesOption validates a decoded JSON bake_textures argument.
func BakeTexturesOption(value any) (bool, error) {
	flag, ok := value.(bool)
	if !ok {
		return false, errors.New("Bake textures must be on or off.")
	}
	return flag, nil
}

func runtimeError(message string) error { return &c4dbridge.RuntimeError{Message: message} }

// statusMessage is status.get('message', default).
func statusMessage(status *c4dbridge.Status, fallback string) string {
	raw, ok := status.Get("message")
	if !ok {
		return fallback
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	return pycompat.Str(value)
}

// reportWarnings is report.get('warnings', []).
func reportWarnings(report json.RawMessage) []string {
	var fields struct {
		Warnings []json.RawMessage `json:"warnings"`
	}
	if json.Unmarshal(report, &fields) != nil {
		return nil
	}
	out := make([]string, 0, len(fields.Warnings))
	for _, raw := range fields.Warnings {
		var value any
		if json.Unmarshal(raw, &value) == nil {
			out = append(out, pycompat.Str(value))
		}
	}
	return out
}

func ptr(value int) *int { return &value }

// ExportMap is export_map(asset, output_folder, scene_name, ...): export an
// already loaded/renamed map and publish only a verified native file.
// sceneName nil uses the asset's name.
func ExportMap(asset *formats.Asset, outputFolder string, sceneName *string, opts Options) (*exportbundle.Result, error) {
	value := asset.Name
	if sceneName != nil {
		value = *sceneName
	}
	name, err := SceneName(value)
	if err != nil {
		return nil, err
	}
	folder := pypath.ExpandUser(outputFolder)
	if !pypath.IsAbs(folder) {
		return nil, errors.New("Choose a full path to a map output folder.")
	}
	if info, err := os.Stat(folder); err == nil && !info.IsDir() {
		return nil, errors.New("Choose a full path to a map output folder.")
	}
	folder = pypath.Resolve(folder)
	client := opts.Bridge
	if client == nil {
		client = c4dbridge.NewClient("")
	}
	status := client.Status()
	if !status.Connected() || !status.Octane() {
		return nil, runtimeError(statusMessage(status, "Cinema 4D with Octane is not connected."))
	}
	if !status.MapInstances() {
		return nil, runtimeError(c4dbridge.MapBridgeMessage(status))
	}
	if !status.MapTiming24() {
		return nil, runtimeError(c4dbridge.FeatureBridgeMessage)
	}
	progress := opts.Progress
	if progress != nil {
		progress("Preparing map geometry", nil, nil)
	}
	payload, err := MakeStaticPayload(asset, nil, name)
	if err != nil {
		return nil, err
	}
	build := func(destinations map[string]string, textureFolder string) (*exportbundle.Details, error) {
		var bakeWarnings []string
		if opts.BakeTextures {
			var bakeProgress exporttextures.Progress
			if progress != nil {
				bakeProgress = func(message string, completed, total int) { progress(message, ptr(completed), ptr(total)) }
			}
			baked, err := bakeMapMaterials(asset, textureFolder, bakeProgress)
			if err != nil {
				return nil, err
			}
			payload.Materials = baked.Materials
			bakeWarnings = baked.Warnings
		} else {
			payload.Materials = unbakedMaterials(asset)
		}
		options := c4dbridge.ExportOptions{MapInstances: true}
		if progress != nil {
			options.Progress = func(message string) { progress(message, nil, nil) }
		}
		report, err := client.Export(payload, destinations["c4d"], textureFolder, options)
		if err != nil {
			return nil, err
		}
		if progress != nil {
			progress("Publishing the C4D file and textures to the output folder", nil, nil)
		}
		var warnings []string
		for _, list := range [][]string{payload.Warnings, bakeWarnings, reportWarnings(report)} {
			for _, warning := range list {
				if !slices.Contains(warnings, warning) {
					warnings = append(warnings, warning)
				}
			}
		}
		if warnings == nil {
			warnings = []string{}
		}
		return &exportbundle.Details{Warnings: warnings}, nil
	}
	return exportbundle.ExportBundle(asset.Path, folder, name+".c4d", build, []string{"c4d"})
}
