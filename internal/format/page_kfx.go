package format

import (
	"bytes"
	"context"
	"image"
	"strings"

	"github.com/levmv/polka/internal/format/kfx"
)

func kfxPageCount(ctx context.Context, doc *kfx.Document) (int, error) {
	if len(doc.FixedPages) != 0 {
		return len(doc.FixedPages), ctx.Err()
	}
	sizes := make(map[string]image.Config)
	for _, resource := range doc.Resources {
		if strings.HasPrefix(resource.MediaType, "image/") {
			config, _, err := image.DecodeConfig(bytes.NewReader(resource.Data[:min(len(resource.Data), 64<<10)]))
			if err == nil {
				sizes[resource.Href] = config
			}
		}
	}
	var extent pageExtent
	err := htmlPageExtent(ctx, []byte(doc.Body), &extent, func(attrs map[string]string) (int, int) {
		config := sizes[pageImageHref(attrs)]
		return config.Width, config.Height
	})
	if err != nil {
		return 0, err
	}
	return extent.Pages(), nil
}
