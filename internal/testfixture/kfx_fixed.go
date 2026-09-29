package testfixture

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// KFXFixed uses the page/story structure found in image-based KFX comics:
// a single cover, paired portrait pages and a centered landscape spread.
func KFXFixed(t testing.TB) []byte {
	t.Helper()
	f := func(kv ...any) kfxFields { return kv }
	s := func(n int) kfxSymbol { return kfxSymbol(n) }
	var entries []kfxEntity
	add := func(id, kind int, v any) {
		entries = append(entries, kfxEntity{uint32(id), uint32(kind), kfxStream(v)})
	}
	add(348, 490, f(491, []any{
		f(495, "kindle_title_metadata", 258, []any{f(492, "title", 307, "Polka fixed pages"), f(492, "language", 307, "en"), f(492, "cover_image", 307, "image-0")}),
		f(495, "kindle_capability_metadata", 258, []any{f(492, "yj_fixed_layout", 307, 1)}),
	}))
	add(348, 538, f(192, s(376), 560, s(559), 169, []any{f(170, []any{s(860), s(861), s(862)})}))
	page := func(n, width int) kfxFields {
		return f(155, 100+n, 159, s(270), 156, s(326), 66, width, 67, 160,
			146, []any{f(155, 200+n, 159, s(270), 156, s(323), 56, width, 57, 160,
				146, []any{f(155, 300+n, 159, s(271), 175, s(863+n), 56, width, 57, 160, 58, 0, 59, 0, 584, fmt.Sprintf("Page %d", n+1))})})
	}
	add(860, 260, f(141, []any{page(0, 120)}))
	add(861, 260, f(141, []any{f(155, 10, 159, s(270), 156, s(437), 176, s(871))}))
	add(871, 259, f(176, s(871), 146, []any{page(1, 120), page(2, 120)}))
	add(862, 260, f(141, []any{f(155, 11, 159, s(270), 156, s(323), 656, true, 655, 2, 176, s(872))}))
	add(872, 259, f(176, s(872), 146, []any{page(3, 240)}))
	var nav []any
	for n := range 4 {
		width := 120
		if n == 3 {
			width = 240
		}
		img := image.NewRGBA(image.Rect(0, 0, width, 160))
		for y := range 160 {
			for x := range width {
				ink := color.RGBA{uint8(80 + n*40), uint8(180 - n*35), uint8(80 + x/3), 255}
				if x < 4 || x >= width-4 || y < 4 || y >= 156 || x/15 == n && y/15 == n {
					ink = color.RGBA{25, 25, 25, 255}
				}
				img.SetRGBA(x, y, ink)
			}
		}
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		add(863+n, 164, f(175, s(863+n), 161, s(284), 165, fmt.Sprintf("raw-%d", n), 422, width, 423, 160))
		entries = append(entries, kfxEntity{uint32(867 + n), 417, data.Bytes()})
		nav = append(nav, f(241, f(244, fmt.Sprintf("Page %d", n+1)), 246, f(155, 200+n, 143, 0)))
	}
	add(348, 389, []any{f(392, []any{f(235, s(212), 247, nav), f(235, s(237), 247, nav)})})
	symbols := []any{"cover", "pair", "wide", "image-0", "image-1", "image-2", "image-3", "raw-0", "raw-1", "raw-2", "raw-3", "pair-story", "wide-story"}
	return kfxContainer(entries, kfxSymbolTable(symbols))
}
