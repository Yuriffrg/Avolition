package toolchain

import (
	"fmt"
	"math/big"
	"net"
	"regexp"
	"strings"
	"time"

	"avolition/gocore/internal/ordered"
	"avolition/gocore/internal/pycompat"
)

// Package is the installed or latest package metadata dict. It stays a
// dynamic, insertion-ordered dict because active.json written by any
// earlier version is returned to the UI unchanged.
type Package = ordered.Map[any]

// Error is ToolchainError: an actionable installation or metadata error safe
// to show in the UI.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func toolchainError(message string) error { return &Error{message} }

// utcNow is datetime.now(timezone.utc).isoformat().replace('+00:00', 'Z').
var now = time.Now

func utcNow() string {
	t := now().UTC()
	if t.Nanosecond()/1000 == 0 {
		return t.Format("2006-01-02T15:04:05") + "Z"
	}
	return t.Format("2006-01-02T15:04:05.000000") + "Z"
}

// DownloadURL is _download_url: only official OWLib release asset URLs.
func DownloadURL(value any) (string, error) {
	invalid := toolchainError("The Toolchain release has an unexpected download URL. Check the official OWLib release.")
	text := ""
	if pycompat.Truthy(value) {
		s, ok := value.(string)
		if !ok {
			return "", invalid
		}
		text = s
	}
	parts, err := urlsplit(text)
	if err != nil {
		return "", err
	}
	host, hasHost := parts.hostname()
	user, password := parts.credentials()
	if parts.scheme != "https" || !hasHost || host != "github.com" ||
		!strings.HasPrefix(strings.ToLower(parts.path), "/overtools/owlib/releases/download/") ||
		user != "" || password != "" {
		return "", invalid
	}
	return text, nil
}

type splitURL struct{ scheme, netloc, path string }

// urlsplit ports urllib.parse.urlsplit far enough for _download_url.
func urlsplit(url string) (splitURL, error) {
	url = strings.TrimLeftFunc(url, func(r rune) bool { return r <= ' ' })
	url = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(url)
	var result splitURL
	if i := strings.IndexByte(url, ':'); i > 0 && isASCIIAlpha(url[0]) {
		valid := true
		for _, c := range url[:i] {
			if !(isASCIIAlpha(byte(c)) && c < 0x80 || c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
				valid = false
				break
			}
		}
		if valid {
			result.scheme, url = strings.ToLower(url[:i]), url[i+1:]
		}
	}
	if strings.HasPrefix(url, "//") {
		end := len(url)
		for _, c := range "/?#" {
			if i := strings.IndexRune(url[2:], c); i >= 0 && i+2 < end {
				end = i + 2
			}
		}
		result.netloc, url = url[2:end], url[end:]
		open, close := strings.Contains(result.netloc, "["), strings.Contains(result.netloc, "]")
		if open != close {
			return result, &pycompat.ValueError{Message: "Invalid IPv6 URL"}
		}
		if open && close {
			if err := checkBracketedNetloc(result.netloc); err != nil {
				return result, err
			}
		}
	}
	if i := strings.IndexByte(url, '#'); i >= 0 {
		url = url[:i]
	}
	if i := strings.IndexByte(url, '?'); i >= 0 {
		url = url[:i]
	}
	result.path = url
	return result, nil
}

func isASCIIAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

var ipvFuture = regexp.MustCompile(`\Av[a-fA-F0-9]+\..+\z`)

func checkBracketedNetloc(netloc string) error {
	hostPort := netloc[strings.LastIndex(netloc, "@")+1:]
	before, bracketed, found := strings.Cut(hostPort, "[")
	if !found {
		return nil
	}
	if before != "" {
		return &pycompat.ValueError{Message: "Invalid IPv6 URL"}
	}
	host, port, _ := strings.Cut(bracketed, "]")
	if port != "" && !strings.HasPrefix(port, ":") {
		return &pycompat.ValueError{Message: "Invalid IPv6 URL"}
	}
	if strings.HasPrefix(host, "v") {
		if !ipvFuture.MatchString(host) {
			return &pycompat.ValueError{Message: "IPvFuture address is invalid"}
		}
		return nil
	}
	address, _, _ := strings.Cut(host, "%")
	ip := net.ParseIP(address)
	if ip == nil {
		return &pycompat.ValueError{Message: fmt.Sprintf("%s does not appear to be an IPv4 or IPv6 address", pycompat.Repr(host))}
	}
	if ip.To4() != nil && !strings.Contains(address, ":") {
		return &pycompat.ValueError{Message: "An IPv4 address cannot be in brackets"}
	}
	return nil
}

func (s splitURL) hostname() (string, bool) {
	info := s.netloc[strings.LastIndex(s.netloc, "@")+1:]
	var host string
	if _, bracketed, found := strings.Cut(info, "["); found {
		host, _, _ = strings.Cut(bracketed, "]")
	} else {
		host, _, _ = strings.Cut(info, ":")
	}
	if host == "" {
		return "", false
	}
	name, zone, hasZone := strings.Cut(host, "%")
	if hasZone {
		return strings.ToLower(name) + "%" + zone, true
	}
	return strings.ToLower(name), true
}

func (s splitURL) credentials() (string, string) {
	i := strings.LastIndex(s.netloc, "@")
	if i < 0 {
		return "", ""
	}
	user, password, _ := strings.Cut(s.netloc[:i], ":")
	return user, password
}

var maxDownload = big.NewInt(MaxDownload)

// ReleaseMetadata is release_metadata: keep release dates and asset dates
// distinct; never substitute local time. release is a decoded JSON value
// (pycompat.Loads).
func ReleaseMetadata(release any) (*Package, error) {
	tag, _ := pycompat.Get(release, "tag_name").(string)
	if _, ok := pycompat.AsDict(release); !ok || pycompat.Strip(tag) == "" {
		return nil, toolchainError("GitHub returned invalid Toolchain release metadata. Try checking again later.")
	}
	assets := any([]any{})
	if pycompat.Has(release, "assets") {
		assets = pycompat.Get(release, "assets")
	}
	items, err := pycompat.Iterate(assets)
	if err != nil {
		return nil, err
	}
	var matches []any
	for _, asset := range items {
		if _, ok := pycompat.AsDict(asset); ok && pycompat.Equal(pycompat.Get(asset, "name"), AssetName) {
			matches = append(matches, asset)
		}
	}
	if len(matches) != 1 {
		return nil, toolchainError("The latest OWLib release does not contain exactly one toolchain-release.zip package.")
	}
	asset := matches[0]
	size := pycompat.Get(asset, "size")
	value, isInt := pycompat.Int(size)
	if !isInt || value.Sign() <= 0 || value.Cmp(maxDownload) > 0 {
		return nil, toolchainError("GitHub reported an invalid Toolchain package size.")
	}
	digest := pycompat.Get(asset, "digest")
	if !pycompat.Truthy(digest) {
		digest = nil
	}
	url, err := DownloadURL(pycompat.Get(asset, "browser_download_url"))
	if err != nil {
		return nil, err
	}
	metadata := ordered.New[any]()
	metadata.Set("version", tag)
	metadata.Set("releaseId", pycompat.Get(release, "id"))
	metadata.Set("releaseDate", pycompat.Get(release, "published_at"))
	metadata.Set("packageUpdatedAt", pycompat.Get(asset, "updated_at"))
	metadata.Set("installedAt", nil)
	metadata.Set("assetId", pycompat.Get(asset, "id"))
	metadata.Set("assetName", AssetName)
	metadata.Set("assetSize", size)
	metadata.Set("digest", digest)
	metadata.Set("downloadUrl", url)
	return metadata, nil
}

// PackageDiffers is package_differs(installed, available). Both are dicts
// (*Package or map[string]any) or nil.
func PackageDiffers(installed, available any) bool {
	if _, ok := pycompat.AsDict(installed); !ok || pycompat.Truthy(pycompat.Get(installed, "legacy")) {
		return true
	}
	// updated_at and size also catch replacement assets when GitHub supplies no
	// digest. No repository commit or last-check timestamp is used here.
	for _, key := range []string{"version", "releaseId", "assetId", "digest", "assetSize", "packageUpdatedAt"} {
		if !pycompat.Equal(pycompat.Get(installed, key), pycompat.Get(available, key)) {
			return true
		}
	}
	return false
}

var reservedName = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|\n?\z)`)

// SafeMember is _safe_member: archive or manifest paths must be relative,
// without traversal, drive or stream syntax, or Windows device names.
func SafeMember(name string) ([]string, error) {
	normalized := strings.ReplaceAll(name, `\`, "/")
	parts := strings.Split(strings.TrimRight(normalized, "/"), "/")
	unsafe := normalized == "" || strings.Contains(normalized, "\x00") || strings.HasPrefix(normalized, "/")
	for _, part := range parts {
		if unsafe {
			break
		}
		unsafe = part == "" || part == "." || part == ".." || strings.Contains(part, ":") ||
			strings.HasSuffix(part, " ") || strings.HasSuffix(part, ".") || reservedName.MatchString(part)
	}
	if unsafe {
		return nil, toolchainError(fmt.Sprintf("Toolchain archive contains an unsafe path: %s.", pycompat.Repr(name)))
	}
	return parts, nil
}
