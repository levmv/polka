package converter

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/levmv/polka/internal/format/kfx"
)

func convertKFXSourceToEPUB(ctx context.Context, w io.Writer, src io.ReaderAt, size int64, opts ConversionOptions) error {
	doc, err := kfx.ExtractDocument(ctx, src, size, opts.OnWarning)
	if err != nil {
		switch {
		case errors.Is(err, kfx.ErrUnsupported):
			return fmt.Errorf("%w: %v", ErrUnsupportedContent, err)
		case errors.Is(err, kfx.ErrLimit):
			return fmt.Errorf("%w: %v", ErrResourceLimit, err)
		default:
			return err
		}
	}
	if err := claimConversionDecodedBytes(ctx, int64(len(doc.Body)), "KFX text"); err != nil {
		return err
	}
	assets := make([]epubAsset, 0, len(doc.Resources))
	for _, resource := range doc.Resources {
		assets = append(assets, epubAsset{ID: resource.ID, Href: resource.Href, MediaType: resource.MediaType, Data: resource.Data, Cover: resource.Cover})
	}
	meta := epubMetadataForOutput(toEPUBMetadata(doc.Metadata), opts)
	meta.PageProgression = doc.Direction
	if len(doc.FixedPages) != 0 {
		return writeKFXFixedEPUB(ctx, w, doc, meta, assets)
	}
	return writeSimpleEPUBWithNav(ctx, w, doc.Body, meta, epubNavigation{Contents: kfxEPUBNav(doc.Navigation), Pages: kfxEPUBNav(doc.Pages)}, assets...)
}

func kfxEPUBNav(items []kfx.NavItem) []epubNavItem {
	result := make([]epubNavItem, 0, len(items))
	for _, item := range items {
		href := item.Href
		if strings.HasPrefix(item.Href, "#") {
			href = "text.xhtml" + item.Href
		}
		result = append(result, epubNavItem{Title: item.Label, Href: href, Children: kfxEPUBNav(item.Children)})
	}
	return result
}

func writeKFXFixedEPUB(ctx context.Context, w io.Writer, doc *kfx.Document, meta epubMetadata, assets []epubAsset) error {
	if err := claimConversionResources(ctx, len(assets)+len(doc.FixedPages), "KFX pages and resources"); err != nil {
		return err
	}
	// Image-only books can have identical XHTML. Their generated publication
	// identity must also include the artwork, including pages after the cover.
	identity := sha256.New()
	for _, asset := range assets {
		if err := claimConversionDecodedBytes(ctx, int64(len(asset.Data)), "KFX resource"); err != nil {
			return err
		}
		identity.Write(asset.Data)
	}
	var documents []epubContentDocument
	for i, page := range doc.FixedPages {
		if err := claimConversionDecodedBytes(ctx, int64(len(page.Body)), "KFX page"); err != nil {
			return err
		}
		io.WriteString(identity, page.Body)
		spread := "page-spread-" + page.Spread
		if page.Spread == "center" {
			spread = "rendition:" + spread
		}
		documents = append(documents, epubContentDocument{ID: fmt.Sprintf("page-%d", i+1), Href: page.Href, SpineProperties: spread})
	}
	meta = normalizeEPUBMetadata(meta, fmt.Sprintf("%x", identity.Sum(nil)))
	nav := epubNavigation{Contents: kfxEPUBNav(doc.Navigation), Pages: kfxEPUBNav(doc.Pages)}
	if len(nav.Contents) == 0 {
		nav.Contents = []epubNavItem{{Title: meta.Title, Href: doc.FixedPages[0].Href}}
	}
	zw := zip.NewWriter(w)
	defer zw.Close()
	if err := addEPUBFile(zw, "mimetype", zip.Store, "application/epub+zip"); err != nil {
		return err
	}
	for _, file := range []struct{ name, body string }{
		{"META-INF/container.xml", epubContainerXML()},
		{"OEBPS/content.opf", epubContentOPF(meta, assets, documents, true)},
		{"OEBPS/nav.xhtml", epubNavXHTML(meta, nav)},
	} {
		if err := addEPUBFile(zw, file.name, zip.Deflate, file.body); err != nil {
			return err
		}
	}
	for _, page := range doc.FixedPages {
		body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html>
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="%s" lang="%s">
<head><title>%s</title><meta name="viewport" content="width=%d, height=%d"/>
<style>html,body{margin:0;padding:0;} body{position:relative;} img{max-width:none;}</style>
</head><body>%s</body></html>
`, html.EscapeString(meta.Language), html.EscapeString(meta.Language), html.EscapeString(meta.Title), page.Width, page.Height, page.Body)
		if err := addEPUBFile(zw, "OEBPS/"+page.Href, zip.Deflate, body); err != nil {
			return err
		}
	}
	for _, asset := range assets {
		if err := addEPUBBytes(zw, "OEBPS/"+asset.Href, zip.Deflate, asset.Data); err != nil {
			return err
		}
	}
	return zw.Close()
}
