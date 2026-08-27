package storage

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

// DefaultBookPathTemplate is the layout of a book file within the managed books
// root. The root *is* the books tree, so rendered paths are relative to the root
// itself (bucket directories sit directly under it).
const DefaultBookPathTemplate = "{author_bucket}/{author_sort}/{title} [{asset_id}]{dot_ext}"

type BookPathData struct {
	Title            string
	SortTitle        string
	Author           string
	AuthorSort       string
	Series           string
	SeriesIndex      string
	AssetID          string
	WorkID           string
	Ext              string
	OriginalFilename string
}

type BookPathCandidate struct {
	AssetID string
	Path    string
}

type BookPathCollision struct {
	Path     string
	AssetIDs []string
}

func RenderBookPathTemplate(template string, data BookPathData) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", fmt.Errorf("book path template is empty")
	}
	if path.IsAbs(template) {
		return "", fmt.Errorf("book path template must be relative")
	}

	rawSegments := strings.Split(template, "/")
	segments := make([]string, 0, len(rawSegments))
	for _, raw := range rawSegments {
		if raw == "" {
			return "", fmt.Errorf("book path template contains an empty segment")
		}
		rendered, err := renderBookPathSegment(raw, data)
		if err != nil {
			return "", err
		}
		safe := sanitizePathSegment(rendered)
		// sanitizePathSegment strips leading dots, so a segment can never render to
		// "." or ".." (they trim to empty); the empty guard below is the whole
		// ./.. rejection.
		if safe == "" {
			return "", fmt.Errorf("book path segment %q rendered empty", raw)
		}
		segments = append(segments, safe)
	}
	return path.Join(segments...), nil
}

func DetectBookPathCollisions(candidates []BookPathCandidate) []BookPathCollision {
	byPath := make(map[string][]string)
	for _, c := range candidates {
		rel := strings.TrimSpace(c.Path)
		if rel == "" {
			continue
		}
		byPath[rel] = append(byPath[rel], c.AssetID)
	}

	paths := make([]string, 0, len(byPath))
	for rel, ids := range byPath {
		if len(ids) > 1 {
			paths = append(paths, rel)
		}
	}
	slices.Sort(paths)

	collisions := make([]BookPathCollision, 0, len(paths))
	for _, rel := range paths {
		ids := append([]string(nil), byPath[rel]...)
		slices.Sort(ids)
		collisions = append(collisions, BookPathCollision{
			Path:     rel,
			AssetIDs: ids,
		})
	}
	return collisions
}

func renderBookPathSegment(segment string, data BookPathData) (string, error) {
	var b strings.Builder
	for len(segment) > 0 {
		start := strings.IndexByte(segment, '{')
		if start < 0 {
			if strings.Contains(segment, "}") {
				return "", fmt.Errorf("book path template has unmatched }")
			}
			b.WriteString(segment)
			break
		}
		literal := segment[:start]
		if strings.Contains(literal, "}") {
			return "", fmt.Errorf("book path template has unmatched }")
		}
		b.WriteString(literal)
		segment = segment[start+1:]

		end := strings.IndexByte(segment, '}')
		if end < 0 {
			return "", fmt.Errorf("book path template has unmatched {")
		}
		token := strings.TrimSpace(segment[:end])
		if token == "" {
			return "", fmt.Errorf("book path template has an empty field")
		}
		value, err := renderBookPathField(token, data)
		if err != nil {
			return "", err
		}
		b.WriteString(value)
		segment = segment[end+1:]
	}
	return b.String(), nil
}

func renderBookPathField(token string, data BookPathData) (string, error) {
	name := token
	fallback := ""
	hasFallback := false
	if before, after, ok := strings.Cut(token, "|"); ok {
		name = strings.TrimSpace(before)
		fallback = strings.TrimSpace(after)
		hasFallback = true
	}
	if name == "" {
		return "", fmt.Errorf("book path template has an empty field")
	}

	value, ok := data.bookPathField(name, !hasFallback)
	if !ok {
		return "", fmt.Errorf("unknown book path field %q", name)
	}
	if strings.TrimSpace(value) == "" && hasFallback {
		value = fallback
	}
	return value, nil
}

func (d BookPathData) bookPathField(name string, useDefault bool) (string, bool) {
	switch name {
	case "title":
		return defaulted(d.Title, "Untitled", useDefault), true
	case "sort_title":
		v := strings.TrimSpace(d.SortTitle)
		if v == "" && useDefault {
			v = defaulted(d.Title, "Untitled", true)
		}
		return v, true
	case "author":
		return defaulted(d.Author, "Unknown Author", useDefault), true
	case "author_sort":
		if !useDefault {
			return strings.TrimSpace(d.AuthorSort), true
		}
		return effectiveAuthorSort(d.AuthorSort, d.Author), true
	case "author_bucket":
		v := strings.TrimSpace(d.AuthorSort)
		if v == "" && !useDefault {
			return "", true
		}
		if v == "" {
			v = effectiveAuthorSort(d.AuthorSort, d.Author)
		}
		return authorBucket(v), true
	case "series":
		return defaulted(d.Series, "_No Series", useDefault), true
	case "series_index":
		return strings.TrimSpace(d.SeriesIndex), true
	case "series_bucket":
		v := strings.TrimSpace(d.Series)
		if v == "" && !useDefault {
			return "", true
		}
		if v == "" {
			return "_No Series", true
		}
		return authorBucket(v), true
	case "asset_id":
		return strings.TrimSpace(d.AssetID), true
	case "work_id":
		return strings.TrimSpace(d.WorkID), true
	case "original_filename":
		return strings.TrimSpace(d.OriginalFilename), true
	case "ext":
		return normalizedExt(d.Ext), true
	case "dot_ext":
		ext := normalizedExt(d.Ext)
		if ext == "" {
			return "", true
		}
		return "." + ext, true
	}
	return "", false
}

func defaulted(value, fallback string, useDefault bool) string {
	value = strings.TrimSpace(value)
	if value == "" && useDefault {
		return fallback
	}
	return value
}

func effectiveAuthorSort(authorSort, author string) string {
	if v := strings.TrimSpace(authorSort); v != "" {
		return v
	}
	author = strings.TrimSpace(author)
	if author == "" || author == "Unknown" || author == "Unknown Author" {
		return "Unknown Author"
	}
	sort := bookmeta.AuthorSort(author)
	if sort == "" {
		return "Unknown Author"
	}
	return sort
}

func normalizedExt(ext string) string {
	ext = strings.TrimSpace(ext)
	ext = strings.TrimPrefix(ext, ".")
	return strings.ToLower(ext)
}
