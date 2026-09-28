package format

import (
	"bytes"
	"context"
	"image"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/format/mobi"
)

func mobiPageCount(ctx context.Context, doc *mobi.Document) (int, error) {
	imageSize := mobiPageImages(doc.Resources)
	var extent pageExtent
	for _, flow := range doc.Flows {
		switch flow.MediaType {
		case "text/plain":
			if err := plainPageExtent(ctx, DecodeTextToUTF8(flow.Data), &extent); err != nil {
				return 0, err
			}
		case "application/xhtml+xml", "text/html":
			if err := htmlPageExtent(ctx, flow.Data, &extent, imageSize); err != nil {
				return 0, err
			}
		}
	}
	return extent.Pages(), nil
}

func mobiPageImages(resources []mobi.Resource) pageImageSize {
	byHref := make(map[string]*mobi.Resource)
	byEmbed := make(map[int]*mobi.Resource)
	var images []*mobi.Resource
	for i := range resources {
		resource := &resources[i]
		if !strings.HasPrefix(resource.MediaType, "image/") {
			continue
		}
		images = append(images, resource)
		byHref[resource.Href] = resource
		if resource.EmbedIndex > 0 && byEmbed[resource.EmbedIndex] == nil {
			byEmbed[resource.EmbedIndex] = resource
		}
	}
	sizes := make(map[*mobi.Resource]image.Config)
	return func(attrs map[string]string) (int, int) {
		href := pageImageHref(attrs)
		resource := byHref[href]
		// MOBI recindex numbers images; KF8 embed indices include non-image
		// resources too. Native flows have not had these links rewritten to Hrefs.
		if raw := attrs["recindex"]; raw != "" {
			index, err := strconv.Atoi(raw)
			resource = nil
			if err == nil && index > 0 && index <= len(images) {
				resource = images[index-1]
			}
		} else if raw, ok := strings.CutPrefix(strings.ToLower(href), "kindle:embed:"); ok {
			raw, _, _ = strings.Cut(raw, "?")
			index, err := strconv.ParseUint(raw, 32, 32)
			resource = nil
			if err == nil && index > 0 {
				resource = byEmbed[int(index)]
			}
		}
		if resource == nil {
			return 0, 0
		}
		config, ok := sizes[resource]
		if !ok {
			var err error
			config, _, err = image.DecodeConfig(bytes.NewReader(resource.Data[:min(len(resource.Data), 64<<10)]))
			if err != nil {
				config = image.Config{}
			}
			sizes[resource] = config
		}
		return config.Width, config.Height
	}
}
