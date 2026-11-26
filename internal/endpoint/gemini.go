package endpoint

import (
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Internal paths never collide with the legacy /v1/ wildcard.
const (
	GeminiGenerate = "\x00gemini/generateContent"
	GeminiStream   = "\x00gemini/streamGenerateContent"
	geminiPrefix   = "/v1beta/models/"
)

func IsGemini(path string) bool { return path == GeminiGenerate || path == GeminiStream }

func APIFormat(path string) string {
	if IsGemini(path) {
		return path[1:]
	}
	return path
}

// InGeminiNamespace also catches malformed paths that ServeMux would otherwise redirect.
func InGeminiNamespace(u *url.URL) bool {
	path := strings.TrimLeft(u.Path, "/")
	if path == "v1beta" || strings.HasPrefix(path, "v1beta/") {
		return true
	}
	clean := strings.TrimLeft(cleanGeminiPath(u), "/")
	return clean == "v1beta" || strings.HasPrefix(clean, "v1beta/")
}

func cleanGeminiPath(u *url.URL) string { return path.Clean(u.Path) }

// ParseGeminiPath consumes exactly one raw segment and decodes only its model portion.
func ParseGeminiPath(u *url.URL) (path, model string) {
	escaped := u.EscapedPath()
	if u.RawPath != "" && escaped != u.RawPath {
		return "", ""
	}
	raw, ok := strings.CutPrefix(escaped, geminiPrefix)
	if !ok || strings.Contains(raw, "/") {
		return "", ""
	}
	switch {
	case strings.HasSuffix(raw, ":generateContent"):
		path, raw = GeminiGenerate, strings.TrimSuffix(raw, ":generateContent")
	case strings.HasSuffix(raw, ":streamGenerateContent"):
		path, raw = GeminiStream, strings.TrimSuffix(raw, ":streamGenerateContent")
	default:
		return "", ""
	}
	model, err := url.PathUnescape(raw)
	if err != nil || len(model) == 0 || len(model) > 256 || !utf8.ValidString(model) || strings.ContainsFunc(model, unicode.IsControl) {
		return "", ""
	}
	return path, model
}
