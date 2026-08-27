package format

import (
	"net/http"
	"path"
	"slices"
	"strings"

	"github.com/levmv/polka/internal/imagecodec"
)

var conventionalCoverHrefs = []string{
	"cover.jpg",
	"cover.jpeg",
	"cover.png",
	"cover.gif",
	"cover.webp",
	"images/cover.jpg",
	"images/cover.jpeg",
	"images/cover.png",
	"images/cover.gif",
	"images/cover.webp",
}

func isConventionalCoverHref(href string) bool {
	clean := strings.TrimPrefix(path.Clean(strings.ReplaceAll(cleanOPFHref(href), "\\", "/")), "./")
	return slices.Contains(conventionalCoverHrefs, clean)
}

func coverImageExtensionFromBytes(data []byte) (string, bool) {
	_, ext, ok := coverImageMediaTypeFromBytes(data)
	return ext, ok
}

func coverImageMediaTypeFromBytes(data []byte) (string, string, bool) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "image/jpeg", ".jpg", true
	case "image/png":
		return "image/png", ".png", true
	case "image/gif":
		return "image/gif", ".gif", true
	case "image/webp":
		return "image/webp", ".webp", true
	default:
		return "", "", false
	}
}

// ComicImageTypeFromBytes returns the browser-readable raster image type used
// by comic archives. Keep this separate from generated EPUB resources: WebP
// and AVIF are useful comic page formats even though they are not EPUB core
// media types.
func ComicImageTypeFromBytes(data []byte) (string, string, bool) {
	if imagecodec.IsAVIF(data) {
		return "image/avif", ".avif", true
	}
	return coverImageMediaTypeFromBytes(data)
}

// EPUBImageTypeFromBytes returns the media type and extension for raster image
// formats Polka embeds in generated EPUB resources.
func EPUBImageTypeFromBytes(data []byte) (string, string, bool) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "image/jpeg", ".jpg", true
	case "image/png":
		return "image/png", ".png", true
	case "image/gif":
		return "image/gif", ".gif", true
	default:
		return "", "", false
	}
}

// EPUBImageResource returns the bytes, media type, and extension for image
// resources Polka embeds in generated EPUBs.
func EPUBImageResource(data []byte, name string) ([]byte, string, string, bool) {
	if mediaType, ext, ok := EPUBImageTypeFromBytes(data); ok {
		return data, mediaType, ext, true
	}
	if svg, ok := epubSafeSVGResource(data, name); ok {
		return svg, "image/svg+xml", ".svg", true
	}
	return nil, "", "", false
}

// EPUBImageMediaTypeForExtension returns the media type for image extensions
// Polka embeds in generated EPUB resources.
func EPUBImageMediaTypeForExtension(ext string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".png":
		return "image/png", true
	case ".gif":
		return "image/gif", true
	case ".svg":
		return "image/svg+xml", true
	default:
		return "", false
	}
}

func coverImageExtensionFromContentType(contentType string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/gif":
		return ".gif", true
	case "image/webp":
		return ".webp", true
	default:
		return "", false
	}
}

func coverImageExtensionFromFormatName(formatName string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(formatName)) {
	case "jpeg":
		return ".jpg", true
	case "png":
		return ".png", true
	case "gif":
		return ".gif", true
	case "webp":
		return ".webp", true
	default:
		return "", false
	}
}
