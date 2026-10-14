package toolchain

import (
	"archive/zip"
	"compress/bzip2"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"avolition/gocore/internal/pycompat"
)

// badZipError is zipfile.BadZipFile.
type badZipError struct{ err error }

func (e *badZipError) Error() string { return e.err.Error() }
func (e *badZipError) Unwrap() error { return e.err }

var hexDigits = regexp.MustCompile(`^[0-9a-fA-F]+$`)

func (m *Manager) download(metadata *Package, path string) (bool, error) {
	digest, _ := metadata.Get("digest")
	algorithm, expected := "sha256", ""
	if pycompat.Truthy(digest) {
		invalid := toolchainError("GitHub supplied an unsupported or invalid package digest; installation was stopped.")
		text, ok := digest.(string)
		if !ok {
			return false, invalid
		}
		name, value, found := strings.Cut(text, ":")
		if !found || (name != "sha256" && name != "sha512") || !hexDigits.MatchString(value) {
			return false, invalid
		}
		if (name == "sha256" && len(value) != 64) || (name == "sha512" && len(value) != 128) {
			return false, invalid
		}
		algorithm, expected = name, value
	}
	var checksum hash.Hash
	if algorithm == "sha512" {
		checksum = sha512.New()
	} else {
		checksum = sha256.New()
	}
	assetSizeValue, _ := metadata.Get("assetSize")
	assetSizeBig, _ := pycompat.Int(assetSizeValue)
	if assetSizeBig == nil || !assetSizeBig.IsInt64() {
		return false, toolchainError("GitHub reported an invalid Toolchain package size.")
	}
	assetSize := assetSizeBig.Int64()
	address, _ := metadata.Get("downloadUrl")
	addressText, _ := address.(string)
	response, err := m.request(addressText, false)
	if err != nil {
		return false, err
	}
	defer response.Close()
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return false, err
	}
	defer output.Close()
	var size int64
	buffer := make([]byte, chunkSize)
	for {
		if err := m.checkCancelled(); err != nil {
			return false, err
		}
		n, readErr := response.Read(buffer)
		if n > 0 {
			size += int64(n)
			if size > MaxDownload || size > assetSize {
				return false, toolchainError("The download size does not match the Toolchain release. Check for updates and retry.")
			}
			if _, err := output.Write(buffer[:n]); err != nil {
				return false, err
			}
			checksum.Write(buffer[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return false, readErr
		}
	}
	if err := output.Sync(); err != nil {
		return false, err
	}
	if err := output.Close(); err != nil {
		return false, err
	}
	if size != assetSize {
		return false, toolchainError("The Toolchain download is incomplete. Try installing again; the installed copy is unchanged.")
	}
	if expected != "" && !strings.EqualFold(hex.EncodeToString(checksum.Sum(nil)), expected) {
		return false, toolchainError("The Toolchain package digest does not match GitHub. The download was rejected; check for updates and retry.")
	}
	return expected != "", nil
}

// cp437 decodes bytes 0x80-0xFF of names without the UTF-8 flag, as zipfile does.
var cp437 = []rune("ÇüéâäàåçêëèïîìÄÅÉæÆôöòûùÿÖÜ¢£¥₧ƒáíóúñÑªº¿⌐¬½¼¡«»░▒▓│┤╡╢╖╕╣║╗╝╜╛┐└┴┬├─┼╞╟╚╔╩╦╠═╬╧╨╤╥╙╘╒╓╫╪┘┌█▄▌▐▀αßΓπΣσµτΦΘΩδ∞φε∩≡±≥≤⌠⌡÷≈°∙·√ⁿ²■ ")

// memberName is ZipInfo.orig_filename.
func memberName(file *zip.File) (string, error) {
	if file.Flags&0x800 != 0 {
		if !utf8.ValidString(file.Name) {
			for i := 0; i < len(file.Name); {
				r, size := utf8.DecodeRuneInString(file.Name[i:])
				if r == utf8.RuneError && size == 1 {
					return "", fmt.Errorf("'utf-8' codec can't decode byte 0x%02x in position %d: invalid start byte", file.Name[i], i)
				}
				i += size
			}
		}
		return file.Name, nil
	}
	var b strings.Builder
	for i := 0; i < len(file.Name); i++ {
		if c := file.Name[i]; c < 0x80 {
			b.WriteByte(c)
		} else {
			b.WriteRune(cp437[c-0x80])
		}
	}
	return b.String(), nil
}

type verifiedMember struct {
	file        *zip.File
	destination string
	dir         bool
}

func (m *Manager) extract(archive, target string) error {
	if err := os.Mkdir(target, 0o777); err != nil {
		return err
	}
	reader, err := zip.OpenReader(archive)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && reader != nil) {
		if errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrChecksum) || errors.Is(err, io.ErrUnexpectedEOF) {
			return &badZipError{err}
		}
		return err
	}
	defer reader.Close()
	reader.RegisterDecompressor(12, func(r io.Reader) io.ReadCloser { return io.NopCloser(bzip2.NewReader(r)) })
	members := reader.File
	var expanded uint64
	for _, member := range members {
		expanded += member.UncompressedSize64
	}
	if len(members) == 0 || len(members) > MaxEntries || expanded > MaxExpanded {
		return toolchainError("The Toolchain archive is empty or exceeds the supported extraction limits.")
	}
	resolvedTarget := pycompat.Resolve(target)
	seen := map[string]bool{}
	verified := make([]verifiedMember, 0, len(members))
	for _, member := range members {
		name, err := memberName(member)
		if err != nil {
			return err
		}
		parts, err := SafeMember(name)
		if err != nil {
			return err
		}
		key := pycompat.Casefold(strings.Join(parts, "/"))
		if seen[key] {
			return toolchainError("The Toolchain archive contains duplicate or conflicting paths.")
		}
		seen[key] = true
		mode := member.ExternalAttrs >> 16
		if kind := mode & 0o170000; kind != 0 && kind != 0o100000 && kind != 0o040000 {
			return toolchainError("Toolchain archives cannot contain symbolic links or special files.")
		}
		destination := pycompat.Resolve(pycompat.Join(target, parts...))
		if !pycompat.IsRelativeTo(destination, resolvedTarget) {
			return toolchainError("A Toolchain archive path escapes the extraction directory.")
		}
		// ZipInfo.is_dir() reads the sanitized name (os.sep becomes "/").
		sanitized := name
		if filepath.Separator != '/' {
			sanitized = strings.ReplaceAll(sanitized, string(filepath.Separator), "/")
		}
		verified = append(verified, verifiedMember{member, destination, strings.HasSuffix(sanitized, "/")})
	}
	// Validate every path before writing the first member.
	for _, item := range verified {
		if err := m.checkCancelled(); err != nil {
			return err
		}
		if item.dir {
			if err := os.MkdirAll(item.destination, 0o777); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(item.destination), 0o777); err != nil {
			return err
		}
		if err := m.extractMember(item); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) extractMember(item verifiedMember) error {
	if item.file.Flags&0x1 != 0 {
		name, _ := memberName(item.file)
		return fmt.Errorf("File %s is encrypted, password required for extraction", pycompat.Repr(name))
	}
	source, err := item.file.Open()
	if err != nil {
		if errors.Is(err, zip.ErrAlgorithm) {
			return errors.New("That compression method is not supported")
		}
		if errors.Is(err, zip.ErrFormat) {
			return &badZipError{err}
		}
		return err
	}
	defer source.Close()
	output, err := os.OpenFile(item.destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	defer output.Close()
	buffer := make([]byte, chunkSize)
	for {
		n, readErr := source.Read(buffer)
		if n > 0 {
			if err := m.checkCancelled(); err != nil {
				return err
			}
			if _, err := output.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if errors.Is(readErr, zip.ErrChecksum) || errors.Is(readErr, zip.ErrFormat) || errors.Is(readErr, io.ErrUnexpectedEOF) {
				return &badZipError{readErr}
			}
			return readErr
		}
	}
	return output.Close()
}

// findDataTool lists files named DataTool.exe (any case) below root, like
// root.rglob('*'). With excludeManaged, any path through a .staging or
// packages folder is skipped.
func findDataTool(root string, excludeManaged bool) []string {
	var candidates []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || path == root {
			return nil
		}
		if strings.ToLower(entry.Name()) != "datatool.exe" {
			return nil
		}
		if !pycompat.IsFile(path) {
			return nil
		}
		if excludeManaged {
			relative, _ := filepath.Rel(root, path)
			for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
				if part == ".staging" || part == "packages" {
					return nil
				}
			}
		}
		candidates = append(candidates, path)
		return nil
	})
	return candidates
}

func runtimeExecutable(root string) (string, error) {
	candidates := findDataTool(root, false)
	if len(candidates) != 1 {
		return "", toolchainError("Toolchain must contain exactly one DataTool.exe runtime. The downloaded package is incomplete or ambiguous.")
	}
	executable := candidates[0]
	valid, err := startsWithMZ(executable)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", toolchainError("The Toolchain package does not contain a valid Windows DataTool executable.")
	}
	if err := validateRuntime(executable); err != nil {
		return "", err
	}
	return executable, nil
}

func nonEmptyFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

var errInvalidRuntime = errors.New("invalid runtime configuration")

func readRuntimeJSON(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err // OSError propagates unchanged
	}
	value, err := pycompat.Loads(pycompat.StripBOM(data))
	if err != nil {
		return nil, errInvalidRuntime
	}
	return value, nil
}

// validateRuntime validates the released .NET apphost and its dependencies
// from its own manifest.
func validateRuntime(executable string) error {
	folder := filepath.Dir(executable)
	required := map[string]bool{"DataTool.dll": true, "DataTool.deps.json": true, "DataTool.runtimeconfig.json": true}
	var missing []string
	for name := range required {
		if !nonEmptyFile(filepath.Join(folder, name)) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return toolchainError("The DataTool runtime is incomplete; missing " + strings.Join(missing, ", ") + ".")
	}
	invalid := toolchainError("The DataTool runtime has invalid .NET dependency or runtime configuration files.")
	err := func() error {
		config, err := readRuntimeJSON(filepath.Join(folder, "DataTool.runtimeconfig.json"))
		if err != nil {
			return err
		}
		dependencies, err := readRuntimeJSON(filepath.Join(folder, "DataTool.deps.json"))
		if err != nil {
			return err
		}
		targets, ok := pycompat.AsDict(pycompat.Get(dependencies, "targets"))
		targetName, isName := pycompat.Get(pycompat.Get(dependencies, "runtimeTarget"), "name").(string)
		if !ok || !isName {
			return errInvalidRuntime
		}
		target, found := targets.Get(targetName)
		options, optionsFound := pycompat.AsDict(pycompat.Get(config, "runtimeOptions"))
		targetDict, targetIsDict := pycompat.AsDict(target)
		if !found || !optionsFound || !targetIsDict {
			return errInvalidRuntime
		}
		// OWLib's official Windows package currently includes its .NET runtime.
		// A framework-dependent future package need not carry these host files.
		if value, _ := options.Get("includedFrameworks"); pycompat.Truthy(value) {
			for _, name := range []string{"hostfxr.dll", "hostpolicy.dll", "coreclr.dll"} {
				required[name] = true
			}
		}
		for _, key := range targetDict.Keys() {
			library, _ := targetDict.Get(key)
			if _, ok := pycompat.AsDict(library); !ok {
				return errInvalidRuntime
			}
			for _, kind := range []string{"runtime", "native"} {
				entries := any(&Package{})
				if pycompat.Has(library, kind) {
					entries = pycompat.Get(library, kind)
				}
				names, err := pycompat.Iterate(entries)
				if err != nil {
					return errInvalidRuntime
				}
				for _, item := range names {
					name, ok := item.(string)
					if !ok {
						return errInvalidRuntime
					}
					// .NET publishes package-relative dependency names either
					// at their relative path or flattened beside the apphost.
					parts, err := SafeMember(name)
					if err != nil {
						return err
					}
					if !nonEmptyFile(pycompat.Join(folder, parts...)) && !nonEmptyFile(filepath.Join(folder, parts[len(parts)-1])) {
						missing = append(missing, name)
					}
				}
			}
		}
		for name := range required {
			if !nonEmptyFile(filepath.Join(folder, name)) {
				missing = append(missing, name)
			}
		}
		return nil
	}()
	if errors.Is(err, errInvalidRuntime) {
		return invalid
	}
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		unique := map[string]bool{}
		var names []string
		for _, name := range missing {
			if !unique[name] {
				unique[name] = true
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) > 8 {
			names = names[:8]
		}
		return toolchainError("The DataTool runtime is incomplete; missing " + strings.Join(names, ", ") + ".")
	}
	return nil
}
