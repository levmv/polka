package converter

import (
	"archive/zip"
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/levmv/polka/internal/css"
	"github.com/levmv/polka/internal/format"
)

type htmlZResources struct {
	ctx     context.Context
	archive *zip.Reader
	assets  *[]epubAsset
	seen    map[*zip.File]int
	options ConversionOptions
	err     error
}

func (r *htmlZResources) image(base, src string) (string, bool) {
	return r.reference(base, src, true)
}

func (r *htmlZResources) reference(base, src string, imageOnly bool) (string, bool) {
	if r.err != nil {
		return "", false
	}
	name, fragment, hasFragment := strings.Cut(src, "#")
	href, err := r.add(base, name, imageOnly, 0)
	if err != nil {
		if fatalConversionError(err) {
			r.err = err
		}
		return "", false
	}
	if hasFragment {
		href += "#" + fragment
	}
	return href, true
}

func (r *htmlZResources) add(base, src string, imageOnly bool, depth int) (string, error) {
	name := packageResourcePath(base, src)
	file := htmlZOptionalZIPEntry(r.archive, name)
	if name == "" || file == nil || file.FileInfo().IsDir() {
		return "", fmt.Errorf("HTMLZ resource is not packaged: %.200q", src)
	}
	if index, ok := r.seen[file]; ok {
		if imageOnly && !strings.HasPrefix((*r.assets)[index].MediaType, "image/") {
			return "", ErrUnsupportedContent
		}
		return (*r.assets)[index].Href, nil
	}
	if depth > 64 {
		return "", fmt.Errorf("HTMLZ resource nesting exceeds limit: %w", ErrResourceLimit)
	}
	raw, err := readZipFileContextLimited(r.ctx, file, maxConverterResourceBytes, "HTMLZ resource")
	if err != nil {
		return "", err
	}
	data, media, ext, ok := format.EPUBImageResource(raw, file.Name)
	if !ok && !imageOnly {
		ext = strings.ToLower(path.Ext(file.Name))
		switch ext {
		case ".css":
			media = "text/css"
		case ".ttf", ".otf", ".woff", ".woff2":
			media = "font/" + ext[1:]
		}
		data, ok = raw, media != ""
	}
	if !ok {
		return "", fmt.Errorf("unsupported HTMLZ resource: %.200q", src)
	}
	if err := claimConversionResources(r.ctx, 1, "HTMLZ resource"); err != nil {
		return "", err
	}
	index := len(*r.assets)
	href := fmt.Sprintf("images/image%d%s", index+1, ext)
	r.seen[file] = index
	*r.assets = append(*r.assets, epubAsset{ID: fmt.Sprintf("img%d", index+1), Href: href, MediaType: media, Data: data})
	resolve := func(ref string) (string, error) {
		if strings.HasPrefix(ref, "data:") {
			return ref, nil
		}
		name, fragment, hasFragment := strings.Cut(ref, "#")
		target, err := r.add(file.Name, name, false, depth+1)
		if fatalConversionError(err) {
			return "", err
		}
		if err != nil {
			r.options.warn("Could not preserve resource %.200q in %.200s: %v", ref, file.Name, err)
			return ref, nil
		}
		ref = epubRelativeAssetHref(href, target)
		if hasFragment {
			ref += "#" + fragment
		}
		return ref, nil
	}
	switch media {
	case "image/svg+xml":
		data, err = rewriteSVGResources(r.ctx, data, func(_ string, ref string) (string, error) { return resolve(ref) })
	case "text/css":
		var rewritten string
		rewritten, err = css.Parse(string(data)).RewriteURLs(resolve)
		data = []byte(rewritten)
	}
	if fatalConversionError(err) {
		return "", err
	}
	if err != nil {
		r.options.warn("Could not rewrite resources in %.200s: %v", file.Name, err)
	} else {
		(*r.assets)[index].Data = data
	}
	return href, nil
}
