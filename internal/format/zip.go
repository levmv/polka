package format

import (
	"archive/zip"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var zipEntryNameFold = cases.Fold()

// NormalizeZipName returns a clean relative zip entry path, or an empty string
// for absolute, traversal, or empty names.
func NormalizeZipName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Clean(name)
	if name == "." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
		return ""
	}
	return name
}

// ResolveZIPEntry prefers one exact member name. If none exists, a single
// normalized match is a compatibility fallback. Ambiguity is local to this
// requested path: unrelated colliding names do not affect the result.
func ResolveZIPEntry(zr *zip.Reader, name string) (*zip.File, bool) {
	var exact *zip.File
	exactCount := 0
	for _, file := range zr.File {
		if file.Name != name {
			continue
		}
		if exact == nil {
			exact = file
		}
		exactCount++
	}
	if exact != nil {
		return exact, exactCount > 1
	}

	want := zipEntryLookupKey(name)
	var fallback *zip.File
	for _, file := range zr.File {
		if zipEntryLookupKey(file.Name) != want {
			continue
		}
		if fallback != nil {
			return nil, true
		}
		fallback = file
	}
	return fallback, false
}

func zipEntryNameCollisionKey(name string) string {
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded
	}
	return zipEntryLookupKey(name)
}

func zipEntryLookupKey(name string) string {
	name = path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if !utf8.ValidString(name) {
		return foldASCIIBytes(name)
	}
	name = norm.NFC.String(name)
	return norm.NFC.String(zipEntryNameFold.String(name))
}

func foldASCIIBytes(value string) string {
	bytes := []byte(value)
	for i, b := range bytes {
		if b >= 'A' && b <= 'Z' {
			bytes[i] = b + ('a' - 'A')
		}
	}
	return string(bytes)
}
