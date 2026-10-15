// Command analyze-library produces the same local inventory as Analyze
// Library without launching the UI. It is the Go port of
// tools/analyze_library.py, with the same flags (parsed like argparse:
// "--root X" or "--root=X", unique prefixes such as "--ro"), messages, exit
// codes (2 for usage errors) and output files.
//
//	analyze-library --root ROOT --output OUTPUT [--catalog CATALOG] [--toolchain TOOLCHAIN]
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"avolition/gocore/internal/analysis"
	"avolition/gocore/internal/formats"
	"avolition/gocore/internal/pyjson"
	"avolition/gocore/internal/pypath"
)

const description = "Produce the same local inventory as Analyze Library without launching the UI."

type option struct {
	name, metavar, help string
	required            bool
}

var options = []option{
	{"--root", "ROOT", "", true},
	{"--output", "OUTPUT", "", true},
	{"--catalog", "CATALOG", "Optional existing Whitehound catalog; read only.", false},
	{"--toolchain", "TOOLCHAIN", "Optional existing toolchain/active.json; not historical export provenance.", false},
}

type usageError string

func (e usageError) Error() string { return string(e) }

func prog() string { return filepath.Base(os.Args[0]) }

func terminalWidth() int {
	if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns > 0 {
		return columns
	}
	return 80
}

// usage formats argparse's usage line, wrapping like HelpFormatter.
func usage() string {
	textWidth := max(terminalWidth()-2, 11)
	parts := []string{"[-h]"}
	for _, o := range options {
		part := o.name + " " + o.metavar
		if !o.required {
			part = "[" + part + "]"
		}
		parts = append(parts, part)
	}
	prefix := "usage: "
	text := prog() + " " + strings.Join(parts, " ")
	if len(prefix)+len(text) <= textWidth {
		return prefix + text + "\n"
	}
	indent := strings.Repeat(" ", len(prefix)+len(prog())+1)
	var lines []string
	line := []string{prog()}
	lineLen := len(prefix) + len(prog())
	if len(prefix)+len(prog()) > textWidth*3/4 {
		indent = strings.Repeat(" ", len(prefix))
		lines = append(lines, prog())
		line, lineLen = nil, len(indent)-1
	}
	for _, part := range parts {
		if lineLen+1+len(part) > textWidth && len(line) > 0 {
			lines = append(lines, strings.Join(line, " "))
			line, lineLen = nil, len(indent)-1
		}
		line = append(line, part)
		lineLen += len(part) + 1
	}
	if len(line) > 0 {
		lines = append(lines, strings.Join(line, " "))
	}
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return prefix + strings.Join(lines, "\n") + "\n"
}

func help() string {
	return usage() + "\n" + description + "\n\n" + `options:
  -h, --help            show this help message and exit
  --root ROOT
  --output OUTPUT
  --catalog CATALOG     Optional existing Whitehound catalog; read only.
  --toolchain TOOLCHAIN
                        Optional existing toolchain/active.json; not
                        historical export provenance.
`
}

type arguments struct {
	values map[string]string
}

var negativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// optionStrings are the parser's option strings, in definition order.
func optionStrings() []string {
	all := []string{"-h", "--help"}
	for _, o := range options {
		all = append(all, o.name)
	}
	return all
}

func canonical(option string) string {
	if option == "-h" {
		return "--help"
	}
	return option
}

// classified is argparse's _parse_optional result for one argument.
type classified struct {
	positional bool   // an 'A' argument
	option     string // the matched option ("" for an unknown optional)
	value      string // explicit "=value" or short-option tail
	hasValue   bool
}

// classify follows argparse's _parse_optional: exact names, "name=value",
// unique prefixes, then negative numbers and arguments with spaces are
// positional, anything else starting with "-" is an unknown optional.
func classify(arg string) (classified, error) {
	if arg == "" || arg[0] != '-' {
		return classified{positional: true}, nil
	}
	all := optionStrings()
	for _, o := range all {
		if arg == o {
			return classified{option: canonical(o)}, nil
		}
	}
	if len(arg) == 1 {
		return classified{positional: true}, nil
	}
	if name, value, ok := strings.Cut(arg, "="); ok {
		for _, o := range all {
			if name == o {
				return classified{option: canonical(o), value: value, hasValue: true}, nil
			}
		}
	}
	type match struct {
		option, value string
		hasValue      bool
	}
	var matches []match
	if strings.HasPrefix(arg, "--") {
		prefix, value, hasValue := strings.Cut(arg, "=")
		for _, o := range all {
			if strings.HasPrefix(o, prefix) {
				matches = append(matches, match{o, value, hasValue})
			}
		}
	} else {
		short, tail := arg[:2], arg[2:]
		for _, o := range all {
			if o == short {
				matches = append(matches, match{o, tail, true})
			} else if strings.HasPrefix(o, arg) {
				matches = append(matches, match{o, "", false})
			}
		}
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.option
		}
		return classified{}, usageError(fmt.Sprintf("ambiguous option: %s could match %s",
			strings.SplitN(arg, "=", 2)[0], strings.Join(names, ", ")))
	}
	if len(matches) == 1 {
		return classified{option: canonical(matches[0].option), value: matches[0].value, hasValue: matches[0].hasValue}, nil
	}
	if negativeNumber.MatchString(arg) || strings.Contains(arg, " ") {
		return classified{positional: true}, nil
	}
	return classified{}, nil
}

func parse(args []string, stdout io.Writer) (*arguments, error) {
	parsed := &arguments{values: map[string]string{}}
	var extras []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			extras = append(extras, args[i+1:]...)
			break
		}
		c, err := classify(arg)
		if err != nil {
			return nil, err
		}
		if c.positional || c.option == "" {
			extras = append(extras, arg)
			continue
		}
		if c.option == "--help" {
			if c.hasValue {
				return nil, usageError("argument -h/--help: ignored explicit argument " + pyRepr(c.value))
			}
			fmt.Fprint(stdout, help())
			os.Exit(0)
		}
		value := c.value
		if !c.hasValue {
			if i+1 >= len(args) || args[i+1] == "--" {
				return nil, usageError("argument " + c.option + ": expected one argument")
			}
			next, err := classify(args[i+1])
			if err != nil {
				return nil, err
			}
			if !next.positional {
				return nil, usageError("argument " + c.option + ": expected one argument")
			}
			i++
			value = args[i]
		}
		parsed.values[c.option] = value
	}
	var missing []string
	for _, o := range options {
		if _, ok := parsed.values[o.name]; o.required && !ok {
			missing = append(missing, o.name)
		}
	}
	if len(missing) > 0 {
		return nil, usageError("the following arguments are required: " + strings.Join(missing, ", "))
	}
	if len(extras) > 0 {
		return nil, usageError("unrecognized arguments: " + strings.Join(extras, " "))
	}
	return parsed, nil
}

func pyRepr(s string) string { return formats.PythonRepr(s) }

func usageFail(message string) {
	fmt.Fprint(os.Stderr, usage())
	fmt.Fprintf(os.Stderr, "%s: error: %s\n", prog(), message)
	os.Exit(2)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "Error:", err)
	os.Exit(1)
}

func readJSON(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, formats.PythonOSError(err)
	}
	var raw json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, fmt.Errorf("Extra data")
	}
	return raw, nil
}

func isObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func main() {
	args, err := parse(os.Args[1:], os.Stdout)
	if err != nil {
		usageFail(err.Error())
	}
	root := pypath.Resolve(args.values["--root"])
	output := pypath.Resolve(args.values["--output"])
	if pypath.IsRelativeTo(output, root) {
		usageFail("Write the report outside the analyzed export to keep its assets read-only.")
	}
	var annotations any = json.RawMessage("{}")
	var installed any
	var warnings []string
	labelsAvailable := false
	if catalog, ok := args.values["--catalog"]; ok && catalog != "" {
		raw, err := readJSON(pypath.Clean(catalog))
		if err == nil {
			var object map[string]json.RawMessage
			if !isObject(raw) {
				fatal(fmt.Errorf("catalog must be a JSON object"))
			}
			_ = json.Unmarshal(raw, &object)
			value, found := object["annotations"]
			switch {
			case !found:
				err = fmt.Errorf("'annotations'")
			case !isObject(value):
				err = fmt.Errorf("Annotations must be an object.")
			default:
				annotations, labelsAvailable = value, true
			}
		}
		if err != nil {
			warnings = append(warnings, "User-label coverage unavailable: "+err.Error())
		}
	}
	if toolchain, ok := args.values["--toolchain"]; ok && toolchain != "" {
		raw, err := readJSON(pypath.Clean(toolchain))
		if err != nil {
			fatal(err)
		}
		if !isObject(raw) {
			fatal(fmt.Errorf("toolchain file must be a JSON object"))
		}
		var object map[string]json.RawMessage
		_ = json.Unmarshal(raw, &object)
		if value, found := object["package"]; found {
			installed = value
		}
	}
	result, err := analysis.Analyze(root, analysis.Options{Annotations: annotations, InstalledToolchain: installed,
		LabelsUnavailable: !labelsAvailable})
	if err != nil {
		fatal(err)
	}
	result.Limitations = append(result.Limitations, warnings...)
	if err := os.MkdirAll(pypath.Parent(output), 0o777); err != nil {
		fatal(formats.PythonOSError(err))
	}
	if err := analysis.WriteFile(output, result); err != nil {
		fatal(err)
	}
	summary := struct {
		Output      string            `json:"output"`
		Assets      int               `json:"assets"`
		Occurrences int               `json:"occurrences"`
		Coverage    analysis.Coverage `json:"coverage"`
		ParseErrors int               `json:"parseErrors"`
	}{output, result.AssetCount, result.OccurrenceCount, result.Coverage, len(result.ParseErrors)}
	text, err := pyjson.Dumps(summary, pyjson.Options{Indent: 2, EnsureASCII: true})
	if err != nil {
		fatal(err)
	}
	os.Stdout.Write(append(text, '\n'))
}
