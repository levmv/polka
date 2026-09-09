package format

import (
	"archive/zip"
	"context"
	"fmt"
	"image"
	"io"
	"net/url"
	"path"
	"strings"
)

const (
	maxPageZIPEntries = 16384
	maxPageSpineItems = 8192
)

func pageZIPEntry(files *zipEntryIndex, base, href string) (*zip.File, string) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil || u.IsAbs() || u.Host != "" || u.Path == "" && u.Fragment == "" {
		return nil, ""
	}
	name := path.Join(path.Dir(base), u.Path)
	if u.Path == "" {
		name = base
	}
	if strings.HasPrefix(u.Path, "/") {
		name = strings.TrimPrefix(u.Path, "/")
	}
	entry, ambiguous := files.resolve(name)
	if ambiguous || entry == nil {
		return nil, ""
	}
	return entry, u.Fragment
}

func epubFixedPageCount(files *zipEntryIndex, opf epubOPFRead) int {
	if len(files.files) > maxPageZIPEntries || len(opf.doc.Spine.Itemrefs) > maxPageSpineItems {
		return 0
	}
	items := make(map[string]opfItem, len(opf.doc.Manifest.Items))
	for _, item := range opf.doc.Manifest.Items {
		items[item.ID] = item
	}
	fixed := false
	for _, meta := range opf.doc.Metadata.Meta {
		if meta.Refines == "" && meta.Property == "rendition:layout" {
			fixed = strings.TrimSpace(meta.Text) == "pre-paginated"
		}
		if meta.Name == "fixed-layout" && meta.Content == "true" {
			fixed = true
		}
	}
	pages := 0
	for _, ref := range opf.doc.Spine.Itemrefs {
		if ref.Linear == "no" {
			continue
		}
		isFixed := fixed || hasOPFProperty(ref.Properties, "rendition:layout-pre-paginated")
		if hasOPFProperty(ref.Properties, "rendition:layout-reflowable") {
			isFixed = false
		}
		if !isFixed {
			return 0
		}
		item, ok := items[ref.IDRef]
		entry, _ := pageZIPEntry(files, opf.path, item.Href)
		if !ok || entry == nil {
			return 0
		}
		pages++
	}
	return pages
}

func metadataFromEPUB(zr *zip.Reader, opf epubOPFRead) *Metadata {
	meta := metadataFromOPF(opf.doc)
	if fixed := epubFixedPageCount(&zipEntryIndex{files: zr.File}, opf); fixed > 0 {
		meta.PageCount = fixed
		meta.FixedLayout = true
	}
	return meta
}

func estimateEPUBPages(ctx context.Context, r io.ReaderAt, size int64) (int, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return 0, err
	}
	if len(zr.File) > maxPageZIPEntries {
		return 0, fmt.Errorf("page count: EPUB has too many entries")
	}
	opf, ok, err := readEPUBOPF(zr)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	files := &zipEntryIndex{files: zr.File}
	if fixed := epubFixedPageCount(files, opf); fixed > 0 {
		return fixed, nil
	}
	if declared := opfDeclaredPageCount(opf.doc.Metadata.Meta); declared > 0 {
		return declared, nil
	}
	if len(opf.doc.Spine.Itemrefs) > maxPageSpineItems {
		return 0, fmt.Errorf("page count: EPUB has too many entries")
	}
	items := make(map[string]opfItem, len(opf.doc.Manifest.Items))
	for _, item := range opf.doc.Manifest.Items {
		items[item.ID] = item
	}
	maps := readEPUBPageMaps(ctx, files, opf)
	var extent pageExtent
	var total int64
	images := make(map[*zip.File]image.Config)
	for _, ref := range opf.doc.Spine.Itemrefs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if ref.Linear == "no" {
			continue
		}
		item, ok := items[ref.IDRef]
		if !ok {
			return 0, fmt.Errorf("page count: missing EPUB spine item")
		}
		entry, _ := pageZIPEntry(files, opf.path, item.Href)
		if entry == nil {
			return 0, fmt.Errorf("page count: missing EPUB spine content")
		}
		if hasOPFProperty(item.Properties, "nav") {
			continue
		}
		anchor := maps.anchorObserver(entry)
		if strings.HasPrefix(item.MediaType, "image/") {
			extent.Break()
			extent.lines += pageLines
			continue
		}
		if item.MediaType != "application/xhtml+xml" && item.MediaType != "text/html" && item.MediaType != "" {
			return 0, fmt.Errorf("page count: unsupported spine media type %q", item.MediaType)
		}
		raw, err := readZipFileLimited(entry, min(32<<20, maxPageCountBytes-total))
		if err != nil {
			return 0, err
		}
		total += int64(len(raw))
		imageSize := func(attrs map[string]string) (int, int) {
			file, _ := pageZIPEntry(files, entry.Name, pageImageHref(attrs))
			if file == nil || total >= maxPageCountBytes {
				return 0, 0
			}
			if config, ok := images[file]; ok {
				return config.Width, config.Height
			}
			r, err := file.Open()
			if err != nil {
				return 0, 0
			}
			defer r.Close()
			limited := &io.LimitedReader{R: r, N: min(64<<10, maxPageCountBytes-total)}
			before := limited.N
			config, _, err := image.DecodeConfig(limited)
			total += before - limited.N
			if err != nil {
				config = image.Config{}
			}
			images[file] = config
			return config.Width, config.Height
		}
		if err := htmlPageExtentAndAnchors(ctx, raw, &extent, imageSize, anchor); err != nil {
			return 0, err
		}
		if total >= maxPageCountBytes {
			return 0, fmt.Errorf("page count: EPUB exceeds expanded byte limit")
		}
	}
	return maps.chooseCount(extent.Pages()), nil
}
