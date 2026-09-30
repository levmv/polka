package converter

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

type packageReference struct {
	path     string
	fragment string
	external bool
}

// Package links are URIs; archive member names are already decoded paths.
// Decode at this boundary once, before joining or checking for traversal.
func resolvePackageReference(base, href string) (packageReference, error) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return packageReference{}, err
	}
	if u.Scheme != "" || u.Host != "" {
		return packageReference{external: true}, nil
	}
	base = strings.ReplaceAll(base, "\\", "/")
	name := base
	if u.Path != "" {
		name = strings.ReplaceAll(u.Path, "\\", "/")
		if strings.HasPrefix(name, "/") {
			name = strings.TrimLeft(name, "/")
		} else {
			name = path.Join(path.Dir(base), name)
		}
	}
	if name != "" {
		name = path.Clean(name)
		if name == ".." || strings.HasPrefix(name, "../") {
			return packageReference{}, fmt.Errorf("reference leaves archive: %q", href)
		}
		if name == "." {
			name = ""
		}
	}
	return packageReference{path: name, fragment: u.Fragment}, nil
}

// Optional resources also accept a producer's literal percent sign when URI
// parsing fails. The fallback preserves all percent sequences as literal bytes;
// it never decodes an already parsed URI a second time.
func packageResourcePath(base, href string) string {
	if strings.TrimSpace(href) == "" {
		return ""
	}
	href, _, _ = strings.Cut(href, "#")
	ref, err := resolvePackageReference(base, href)
	var escapeErr url.EscapeError
	if errors.As(err, &escapeErr) {
		ref, err = resolvePackageReference(base, strings.ReplaceAll(href, "%", "%25"))
	}
	if err != nil || ref.external {
		return ""
	}
	return ref.path
}
