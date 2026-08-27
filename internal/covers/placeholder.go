package covers

// Generated fallback covers. A work with no stored cover image gets a
// deterministic placeholder rendered from its title and author instead of an
// empty "No Cover" block. The render is a pure function of (title, author):
// the background colour is hashed from the text and the title/author are laid
// out with the Go font at the variant's 2:3 bounds. It is a *render* fallback,
// not a stored cover — has_cover stays false in the DB.

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Fonts are parsed once; faces (size-specific) are cheap to build per render.
var (
	fontRegular = mustParseFont(goregular.TTF)
	fontBold    = mustParseFont(gobold.TTF)
)

func mustParseFont(ttf []byte) *opentype.Font {
	f, err := opentype.Parse(ttf)
	if err != nil {
		panic("covers: parse embedded font: " + err.Error())
	}
	return f
}

// Placeholder renders a deterministic fallback cover for a work with no stored
// cover. The output is always exactly the variant's 2:3 bounds, JPEG-encoded.
func Placeholder(title, author string, variant Variant, opts Options) (Encoded, error) {
	opts = normalizeOptions(opts)
	w, h, err := variantBounds(variant, opts)
	if err != nil {
		return Encoded{}, err
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled"
	}
	author = strings.TrimSpace(author)

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	hue := hueFromText(title, author)
	bg := hslToNRGBA(hue, 0.42, 0.38)
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	// A darker spine band on the left hints at a book without claiming detail.
	spineW := w * 6 / 100
	if spineW > 0 {
		spine := hslToNRGBA(hue, 0.42, 0.30)
		draw.Draw(img, image.Rect(0, 0, spineW, h), &image.Uniform{C: spine}, image.Point{}, draw.Src)
	}

	titleSize := float64(w) * 0.095
	authorSize := float64(w) * 0.060
	titleFace, err := newFace(fontBold, titleSize)
	if err != nil {
		return Encoded{}, err
	}
	defer titleFace.Close()
	authorFace, err := newFace(fontRegular, authorSize)
	if err != nil {
		return Encoded{}, err
	}
	defer authorFace.Close()

	marginX := w * 9 / 100
	textLeft := spineW + marginX
	textWidth := fixed.I(w - textLeft - marginX)

	titleLines := wrapText(titleFace, title, textWidth, 7)
	var authorLines []string
	if author != "" {
		authorLines = wrapText(authorFace, author, textWidth, 2)
	}

	titleM := titleFace.Metrics()
	authorM := authorFace.Metrics()
	titleLH := titleM.Height.Ceil()
	authorLH := authorM.Height.Ceil()
	gap := h * 4 / 100

	total := len(titleLines) * titleLH
	if len(authorLines) > 0 {
		total += gap + len(authorLines)*authorLH
	}

	y := max((h-total)/2, h/8)

	titleCol := color.NRGBA{R: 0xf5, G: 0xf5, B: 0xf2, A: 0xff}
	authorCol := color.NRGBA{R: 0xf5, G: 0xf5, B: 0xf2, A: 0xcc}

	for _, line := range titleLines {
		drawCentered(img, titleFace, line, w, y+titleM.Ascent.Ceil(), titleCol)
		y += titleLH
	}
	if len(authorLines) > 0 {
		y += gap
		for _, line := range authorLines {
			drawCentered(img, authorFace, line, w, y+authorM.Ascent.Ceil(), authorCol)
			y += authorLH
		}
	}

	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: opts.JPEGQuality}); err != nil {
		return Encoded{}, fmt.Errorf("encode jpeg: %w", err)
	}
	return Encoded{
		Bytes:       out.Bytes(),
		Width:       w,
		Height:      h,
		ContentType: ContentTypeJPEG,
	}, nil
}

func newFace(f *opentype.Font, sizePx float64) (font.Face, error) {
	// DPI 72 makes Size in points equal to pixels.
	return opentype.NewFace(f, &opentype.FaceOptions{Size: sizePx, DPI: 72, Hinting: font.HintingFull})
}

func drawCentered(dst draw.Image, face font.Face, s string, imgW, baseline int, col color.Color) {
	lineW := font.MeasureString(face, s)
	x := (fixed.I(imgW) - lineW) / 2
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.Point26_6{X: x, Y: fixed.I(baseline)},
	}
	d.DrawString(s)
}

// wrapText greedily wraps text to fit maxWidth, breaking over-long words by
// rune, and truncates to maxLines with an ellipsis on the last kept line.
func wrapText(face font.Face, text string, maxWidth fixed.Int26_6, maxLines int) []string {
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for word := range strings.FieldsSeq(text) {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if font.MeasureString(face, cand) <= maxWidth {
			cur = cand
			continue
		}
		flush()
		// The word alone may still overflow; hard-break it by rune.
		if font.MeasureString(face, word) > maxWidth {
			chunks := breakWord(face, word, maxWidth)
			lines = append(lines, chunks[:len(chunks)-1]...)
			cur = chunks[len(chunks)-1]
		} else {
			cur = word
		}
	}
	flush()

	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = ellipsize(face, lines[maxLines-1], maxWidth)
	}
	return lines
}

// breakWord splits a single token into chunks that each fit maxWidth. The last
// chunk may be the only one for a short remainder; always returns ≥1 chunk.
func breakWord(face font.Face, word string, maxWidth fixed.Int26_6) []string {
	var chunks []string
	cur := ""
	for _, r := range word {
		next := cur + string(r)
		if cur != "" && font.MeasureString(face, next) > maxWidth {
			chunks = append(chunks, cur)
			cur = string(r)
		} else {
			cur = next
		}
	}
	chunks = append(chunks, cur)
	return chunks
}

// ellipsize trims runes off the end until the string plus an ellipsis fits.
func ellipsize(face font.Face, s string, maxWidth fixed.Int26_6) string {
	runes := []rune(strings.TrimRight(s, " "))
	for len(runes) > 0 {
		cand := string(runes) + "…"
		if font.MeasureString(face, cand) <= maxWidth {
			return cand
		}
		runes = runes[:len(runes)-1]
	}
	return "…"
}

func hueFromText(title, author string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(title))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(author))
	return float64(h.Sum32() % 360)
}

// hslToNRGBA converts HSL (h in [0,360), s,l in [0,1]) to opaque NRGBA.
func hslToNRGBA(h, s, l float64) color.NRGBA {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return color.NRGBA{
		R: uint8(math.Round((r + m) * 255)),
		G: uint8(math.Round((g + m) * 255)),
		B: uint8(math.Round((b + m) * 255)),
		A: 255,
	}
}
