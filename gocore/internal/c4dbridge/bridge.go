// Package c4dbridge ports the backend side of owpreview/c4d_bridge.py: the
// local, JSON-only export queue shared with the Cinema 4D companion plugin.
//
// Cinema owns native document creation. Jobs write only inside their unique
// queue directory; Avolition publishes the completed file with its other
// exports. BridgeService stays Python (it runs inside Cinema 4D), so every
// file written here must stay readable by it: compact JSON without ASCII
// escaping, written to a .tmp sibling and atomically replaced, with the same
// file names (status-*.json, jobs/<uuid hex>/{payload,request,result}.json,
// processing, cancelled, tex/, scene.c4d) and the same polling rules.
package c4dbridge

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"avolition/gocore/internal/ordered"
)

const (
	Protocol          = 1
	MaxPayload        = 256 * 1024 * 1024
	MapExportTimeout  = 30 * 60 // seconds
	QueueStartTimeout = 150     // seconds
	// DefaultTimeout is the animation export budget (seconds).
	DefaultTimeout = 150

	SetupMessage = "C4D export needs Cinema 4D with Octane and the included Avolition C4D Bridge. " +
		"Copy the Avolition C4D Bridge folder from this download into Cinema 4D's " +
		"plugins folder, restart Cinema 4D, then export again. Keep Cinema 4D open."
	MapBridgeMessageText = "Install the bundled Avolition C4D Bridge 0.7.14, replacing the old bridge " +
		"copy, then restart Cinema 4D with Octane. Keep only one bridge installed."
	FeatureBridgeMessage = "This export needs the updated motion clip and map timing support. " + MapBridgeMessageText
)

// MapBridgeMessage explains that the connected bridge cannot export maps.
func MapBridgeMessage(status *Status) string {
	return "Cinema 4D is connected through bridge " + status.version() +
		", but that bridge does not support maps. " + MapBridgeMessageText
}

// QueueRoot is the mailbox directory. The existing location is kept so renamed
// and previously installed companions can still discover each other; new
// isolated installations can use either environment alias.
func QueueRoot() string {
	if override := os.Getenv("AVOLITION_C4D_QUEUE"); override != "" {
		return override
	}
	if override := os.Getenv("WHITEHOUND_C4D_QUEUE"); override != "" {
		return override
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, "Whitehound", "c4d-bridge")
}

// Status is a bridge status reply: the plugin's status file (keys and values
// kept verbatim and in order) plus "connected" and "message".
type Status struct {
	fields ordered.Map[json.RawMessage]
}

// NewStatus builds a status from raw JSON fields in order.
func NewStatus(fields *ordered.Map[json.RawMessage]) *Status {
	status := &Status{}
	if fields != nil {
		fields.Range(func(key string, value json.RawMessage) bool {
			status.fields.Set(key, value)
			return true
		})
	}
	return status
}

func (s *Status) MarshalJSON() ([]byte, error) { return s.fields.MarshalJSON() }

func (s *Status) UnmarshalJSON(data []byte) error { return s.fields.UnmarshalJSON(data) }

// Get returns a field's raw JSON.
func (s *Status) Get(key string) (json.RawMessage, bool) { return s.fields.Get(key) }

// Set replaces or appends a field, keeping an existing key's position.
func (s *Status) Set(key string, value any) {
	data, err := Marshal(value)
	if err != nil {
		data = []byte("null")
	}
	s.fields.Set(key, data)
}

func (s *Status) truthy(key string) bool {
	value, _ := s.fields.Get(key)
	return truthy(value)
}

func (s *Status) isTrue(key string) bool {
	value, _ := s.fields.Get(key)
	return string(value) == "true"
}

func (s *Status) str(key string) string {
	value, _ := s.fields.Get(key)
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return ""
}

// Connected reports status['connected'] truthiness.
func (s *Status) Connected() bool { return s.truthy("connected") }

// Octane reports whether the Cinema 4D instance has Octane loaded.
func (s *Status) Octane() bool { return s.truthy("octane") }

// MapInstances reports status.get('mapInstances') is True.
func (s *Status) MapInstances() bool { return s.isTrue("mapInstances") }

// MapTiming24 reports support for the nonnegative, 24 FPS static-map timeline.
func (s *Status) MapTiming24() bool { return s.isTrue("mapTiming24") }

// Instance is the plugin instance identifier ("" when disconnected).
func (s *Status) Instance() string { return s.str("instance") }

// Message is the user-facing connection message.
func (s *Status) Message() string { return s.str("message") }

func (s *Status) version() string {
	value, _ := s.fields.Get("bridgeVersion")
	if !truthy(value) {
		return "unknown version"
	}
	return pyStr(value)
}

// truthy mirrors Python truthiness of a decoded JSON value.
func truthy(value json.RawMessage) bool {
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return false
	}
	switch value[0] {
	case 'n', 'f':
		return false
	case 't':
		return true
	case '"':
		return len(value) > 2
	case '[', '{':
		inner := bytes.TrimSpace(value[1 : len(value)-1])
		return len(inner) > 0
	default:
		number, err := strconv.ParseFloat(string(value), 64)
		return err != nil || number != 0
	}
}

// pyStr renders a decoded JSON value like Python's str().
func pyStr(value json.RawMessage) string {
	value = bytes.TrimSpace(value)
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	switch string(value) {
	case "true":
		return "True"
	case "false":
		return "False"
	case "null":
		return "None"
	}
	if bytes.ContainsAny(value, ".eE") {
		if number, err := strconv.ParseFloat(string(value), 64); err == nil {
			return string(appendPyFloat(nil, number))
		}
	}
	return string(value)
}

// number decodes a JSON number (or bool, which Python compares as 0/1).
func number(value json.RawMessage) (float64, bool) {
	switch string(bytes.TrimSpace(value)) {
	case "true":
		return 1, true
	case "false":
		return 0, true
	}
	var result float64
	if err := json.Unmarshal(value, &result); err != nil {
		return 0, false
	}
	return result, true
}

// Client sends export jobs to the Cinema 4D plugin through the mailbox.
type Client struct {
	Root string

	// Test hooks (Python's tests patch status, time.monotonic and time.sleep).
	statusFunc func() *Status
	monotonic  func() float64
	sleep      func(seconds float64)
	now        func() float64
}

// NewClient uses root, or QueueRoot() when root is "".
func NewClient(root string) *Client {
	if root == "" {
		root = QueueRoot()
	}
	return &Client{Root: root}
}

var processStart = time.Now()

func (c *Client) wallTime() float64 {
	if c.now != nil {
		return c.now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

func (c *Client) monotonicTime() float64 {
	if c.monotonic != nil {
		return c.monotonic()
	}
	return time.Since(processStart).Seconds()
}

func (c *Client) pause(seconds float64) {
	if c.sleep != nil {
		c.sleep(seconds)
		return
	}
	time.Sleep(time.Duration(seconds * float64(time.Second)))
}

// globNames lists names in dir matching prefix*suffix like pathlib's glob:
// case-insensitive on Windows, hidden files included, unreadable dirs empty.
func globNames(dir, prefix, suffix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		probe, wantPrefix, wantSuffix := name, prefix, suffix
		if runtime.GOOS == "windows" {
			probe, wantPrefix, wantSuffix = strings.ToLower(name), strings.ToLower(prefix), strings.ToLower(suffix)
		}
		if len(probe) >= len(wantPrefix)+len(wantSuffix) && strings.HasPrefix(probe, wantPrefix) && strings.HasSuffix(probe, wantSuffix) {
			names = append(names, name)
		}
	}
	return names
}

type liveStatus struct {
	fields    *ordered.Map[json.RawMessage]
	octane    bool
	maps      bool
	features  int
	heartbeat float64
}

// Status reports the best live Cinema 4D instance.
func (c *Client) Status() *Status { return c.liveStatus("") }

// liveStatus pins reviewed Stager requests to the same running Cinema instance.
func (c *Client) liveStatus(instanceID string) *Status {
	if c.statusFunc != nil {
		return c.statusFunc()
	}
	var live []liveStatus
	for _, name := range globNames(c.Root, "status-", ".json") {
		data, err := ReadJSON(filepath.Join(c.Root, name), 65536)
		if err != nil {
			continue
		}
		var fields ordered.Map[json.RawMessage]
		if bytes.TrimSpace(data)[0] != '{' || json.Unmarshal(data, &fields) != nil {
			continue
		}
		protocol, _ := fields.Get("protocol")
		if value, ok := number(protocol); !ok || value != Protocol {
			continue
		}
		instance, _ := fields.Get("instance")
		var text string
		if json.Unmarshal(instance, &text) != nil || text == "" || (instanceID != "" && text != instanceID) {
			continue
		}
		raw, ok := fields.Get("heartbeat")
		if !ok {
			continue
		}
		heartbeat, ok := number(raw)
		if !ok {
			continue
		}
		if age := c.wallTime() - heartbeat; !(0 <= age && age < 10) {
			continue
		}
		octane, _ := fields.Get("octane")
		maps, _ := fields.Get("mapInstances")
		features := 0
		for _, capability := range []string{"motionClips", "skeletonMotionClips", "combinedMotionClips", "exactFrameTimes", "optimizedGameplay1P", "povHeightZero", "mapTiming24", "stagerRig"} {
			value, _ := fields.Get(capability)
			if string(value) == "true" {
				features++
			}
		}
		live = append(live, liveStatus{fields: &fields, octane: truthy(octane), maps: string(maps) == "true",
			features: features, heartbeat: heartbeat})
	}
	if len(live) == 0 {
		status := &Status{}
		status.Set("connected", false)
		status.Set("message", SetupMessage)
		return status
	}
	// Prefer a capable Octane instance over a newer heartbeat from an older
	// bridge. Both preflight and job dispatch use this rule.
	best := live[0]
	for _, candidate := range live[1:] {
		if better(candidate, best) {
			best = candidate
		}
	}
	status := NewStatus(best.fields)
	version := status.version()
	var message string
	switch {
	case !best.octane:
		message = "Cinema 4D connected (bridge " + version + "), but Octane is not loaded. Enable Octane and restart Cinema 4D."
	case best.maps:
		message = "Cinema 4D connected (bridge " + version + "). Octane materials and map exports ready."
	default:
		message = "Cinema 4D connected (bridge " + version + "). Octane materials ready for animations. Update the Avolition C4D Bridge for map exports."
	}
	status.Set("connected", true)
	status.Set("message", message)
	return status
}

// better orders by Python's max key (bool(octane), mapInstances is True,
// supported feature count, heartbeat); max keeps the first of equal keys.
func better(a, b liveStatus) bool {
	if a.octane != b.octane {
		return a.octane
	}
	if a.maps != b.maps {
		return a.maps
	}
	if a.features != b.features {
		return a.features > b.features
	}
	return a.heartbeat > b.heartbeat
}

// ExportOptions are BridgeClient.export's optional arguments.
type ExportOptions struct {
	// Timeout in seconds; nil selects 30 minutes for map payloads, else 150 s.
	Timeout *float64
	// Stager requests return a result in-place, without publishing scene.c4d.
	Stager bool
	// Instance is the reviewed instance ID, required for scene mutations.
	Instance string
	// MapInstances must be bool(payload.get('mapInstances')). It is detected
	// automatically for map[string]any and ordered.Map payloads.
	MapInstances bool
	// Progress receives stage messages (may be nil).
	Progress func(string)
}

// RuntimeError is an export failure shown to the user verbatim.
type RuntimeError struct{ Message string }

func (e *RuntimeError) Error() string { return e.Message }

func runtimeError(message string) error { return &RuntimeError{message} }

func payloadMapInstances(payload any, options ExportOptions) bool {
	if options.MapInstances {
		return true
	}
	switch value := payload.(type) {
	case map[string]any:
		data, _ := Marshal(value["mapInstances"])
		return truthy(data)
	case *ordered.Map[any]:
		item, _ := value.Get("mapInstances")
		data, _ := Marshal(item)
		return truthy(data)
	case *ordered.Map[json.RawMessage]:
		item, _ := value.Get("mapInstances")
		return truthy(item)
	}
	return false
}

func requiredCapabilities(payload any) []string {
	if p, ok := payload.(interface{ RequiredBridgeCapabilities() []string }); ok {
		return p.RequiredBridgeCapabilities()
	}
	var flags []string
	for _, pair := range [][2]string{{"motionClip", "combinedMotionClips"}, {"motionClip", "exactFrameTimes"}, {"gameplay1P", "optimizedGameplay1P"}, {"gameplay1P", "povHeightZero"}, {"mapTiming24", "mapTiming24"}} {
		var raw []byte
		switch p := payload.(type) {
		case map[string]any:
			raw, _ = Marshal(p[pair[0]])
		case *ordered.Map[any]:
			v, _ := p.Get(pair[0])
			raw, _ = Marshal(v)
		case *ordered.Map[json.RawMessage]:
			raw, _ = p.Get(pair[0])
		}
		if truthy(raw) {
			flags = append(flags, pair[1])
		}
	}
	return flags
}

func newJobName() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40 // uuid4
	id[8] = id[8]&0x3f | 0x80
	return hex.EncodeToString(id[:]), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// formatG mirrors Python's format(value, 'g').
func formatG(value float64) string {
	return strconv.FormatFloat(value, 'g', 6, 64)
}

// Export sends payload to Cinema 4D, waits for the native scene and copies
// it to destination. It returns the plugin's result "data" as raw JSON.
func (c *Client) Export(payload any, destination, textureFolder string, options ExportOptions) (json.RawMessage, error) {
	mapInstances := payloadMapInstances(payload, options)
	timeout := float64(DefaultTimeout)
	if options.Timeout != nil {
		timeout = *options.Timeout
	} else if mapInstances {
		timeout = MapExportTimeout
	}
	status := c.liveStatus(options.Instance)
	if options.Instance != "" && status.Instance() != options.Instance {
		return nil, runtimeError("The reviewed Cinema 4D instance is no longer available. Review the destination again.")
	}
	if options.Stager && status.Connected() && !status.isTrue("stagerRig") {
		return nil, runtimeError("Create Stager rig needs Avolition C4D Bridge 0.7.14 and the Avolition Stager plugin. Open Cinema 4D with both plugins loaded.")
	}
	if !status.Connected() {
		return nil, runtimeError(SetupMessage)
	}
	if !status.Octane() {
		return nil, runtimeError(status.Message())
	}
	if mapInstances && !status.MapInstances() {
		return nil, runtimeError(MapBridgeMessage(status))
	}
	for _, capability := range requiredCapabilities(payload) {
		if !status.isTrue(capability) {
			return nil, runtimeError(FeatureBridgeMessage)
		}
	}
	name, err := newJobName()
	if err != nil {
		return nil, err
	}
	job := filepath.Join(c.Root, "jobs", name)
	if err := os.MkdirAll(filepath.Dir(job), 0o777); err != nil {
		return nil, pyOSError(err)
	}
	if err := os.Mkdir(job, 0o777); err != nil {
		return nil, pyOSError(err)
	}
	completed := false
	defer func() {
		if completed || !exists(filepath.Join(job, "processing")) {
			// job is always a newly created UUID directory under our queue.
			_ = os.RemoveAll(job)
		} else if file, err := os.OpenFile(filepath.Join(job, "cancelled"), os.O_CREATE|os.O_WRONLY, 0o666); err == nil {
			file.Close()
		}
	}()
	progress := func(message string) {
		if options.Progress != nil {
			options.Progress(message)
		}
	}
	progress("Sending map geometry and textures to Cinema 4D")
	if err := WriteJSON(filepath.Join(job, "payload.json"), payload); err != nil {
		return nil, err
	}
	if info, err := os.Stat(filepath.Join(job, "payload.json")); err != nil {
		return nil, pyOSError(err)
	} else if info.Size() > MaxPayload {
		return nil, errors.New("Cinema 4D export exceeds the 256 MB job limit.")
	}
	if exists(textureFolder) {
		if err := os.Mkdir(filepath.Join(job, "tex"), 0o777); err != nil {
			return nil, pyOSError(err)
		}
		for _, name := range globNames(textureFolder, "", ".png") {
			if err := copyFile(filepath.Join(textureFolder, name), filepath.Join(job, "tex", name), true); err != nil {
				return nil, err
			}
		}
	}
	request := ordered.New[any]()
	request.Set("protocol", Protocol)
	request.Set("instance", status.Instance())
	request.Set("expiresAt", Float(c.wallTime()+timeout))
	if err := WriteJSON(filepath.Join(job, "request.json"), request); err != nil {
		return nil, err
	}
	deadline := c.monotonicTime() + math.Min(timeout, QueueStartTimeout)
	started := false
	progress("Waiting for Cinema 4D to start the export")
	for c.monotonicTime() < deadline {
		if !started && exists(filepath.Join(job, "processing")) {
			started = true
			// Give a large map its own native-build budget, separately from
			// queue pickup and earlier parsing/texture baking.
			deadline = c.monotonicTime() + timeout
			progress("Cinema 4D is building, saving and verifying the map")
		}
		if isFile(filepath.Join(job, "result.json")) {
			data, err := ReadJSON(filepath.Join(job, "result.json"), 1024*1024)
			if err != nil {
				return nil, err
			}
			completed = true
			var result ordered.Map[json.RawMessage]
			if bytes.TrimSpace(data)[0] != '{' {
				return nil, errors.New("'" + jsonTypeName(data) + "' object has no attribute 'get'")
			}
			if err := json.Unmarshal(data, &result); err != nil {
				return nil, err
			}
			if ok, _ := result.Get("ok"); !truthy(ok) {
				message := "Cinema 4D export failed."
				if value, present := result.Get("error"); present {
					message = pyStr(value)
				}
				return nil, runtimeError(message)
			}
			if options.Stager {
				value, present := result.Get("data")
				if !present || len(value) == 0 || value[0] != '{' {
					return nil, runtimeError("The Cinema 4D bridge returned an invalid Stager result.")
				}
				return value, nil
			}
			scene := filepath.Join(job, "scene.c4d")
			if info, err := os.Stat(scene); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return nil, runtimeError("Cinema 4D did not save the native scene.")
			}
			if err := copyFile(scene, destination, false); err != nil {
				return nil, err
			}
			if value, present := result.Get("data"); present {
				return value, nil
			}
			return json.RawMessage("{}"), nil
		}
		c.pause(.1)
	}
	if !started {
		return nil, runtimeError("Cinema 4D did not start the export. Close any modal Cinema 4D dialog and check that the Avolition bridge is running.")
	}
	return nil, runtimeError("Cinema 4D did not finish the export within " + formatG(timeout/60) + " minutes.")
}

func jsonTypeName(data json.RawMessage) string {
	switch bytes.TrimSpace(data)[0] {
	case '[':
		return "list"
	case '"':
		return "str"
	case 't', 'f':
		return "bool"
	case 'n':
		return "NoneType"
	}
	if bytes.ContainsAny(data, ".eE") {
		return "float"
	}
	return "int"
}

// copyFile is shutil.copyfile, or shutil.copy2 when keepTimes is set (copy2
// also keeps permission bits and timestamps).
func copyFile(source, destination string, keepTimes bool) error {
	input, err := os.Open(source)
	if err != nil {
		return pyOSError(err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return pyOSError(err)
	}
	if info.IsDir() {
		return pyOSError(&os.PathError{Op: "open", Path: source, Err: errIsDir})
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		return pyOSError(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return pyOSError(err)
	}
	if err := output.Close(); err != nil {
		return pyOSError(err)
	}
	if keepTimes {
		_ = os.Chmod(destination, info.Mode().Perm())
		_ = os.Chtimes(destination, info.ModTime(), info.ModTime())
	}
	return nil
}
