package covers

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

const (
	GeneratedStyleClassic = "classic"
	GeneratedStyleBands   = "bands"
	GeneratedStyleLabel   = "label"
	GeneratedStyleQuiet   = "quiet"
)

// Generated renders a deterministic generated cover variant for an explicit
// edit action. Unlike Placeholder, this is meant to become a stored user-chosen
// cover after Save, so the seed is allowed to change the visual result.
func Generated(title, author string, variant Variant, opts Options, seed int) (Encoded, error) {
	return GeneratedStyled(title, author, variant, opts, seed, GeneratedStyleClassic)
}

// GeneratedStyled renders one deterministic generated-cover style for the edit
// cover picker. Unknown style ids fall back to the classic style so old clients
// keep working if the style list changes.
func GeneratedStyled(title, author string, variant Variant, opts Options, seed int, style string) (Encoded, error) {
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
	if seed < 0 {
		seed = 0
	}

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	hue := math.Mod(hueFromText(title, author)+float64((seed%97)*37), 360)
	switch normalizeGeneratedStyle(style) {
	case GeneratedStyleBands:
		err = drawGeneratedBands(img, title, author, hue, seed)
	case GeneratedStyleLabel:
		err = drawGeneratedLabel(img, title, author, hue, seed)
	case GeneratedStyleQuiet:
		err = drawGeneratedQuiet(img, title, author, hue, seed)
	default:
		err = drawGeneratedClassic(img, title, author, hue, seed)
	}
	if err != nil {
		return Encoded{}, err
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

func normalizeGeneratedStyle(style string) string {
	switch strings.TrimSpace(strings.ToLower(style)) {
	case GeneratedStyleBands:
		return GeneratedStyleBands
	case GeneratedStyleLabel:
		return GeneratedStyleLabel
	case GeneratedStyleQuiet:
		return GeneratedStyleQuiet
	default:
		return GeneratedStyleClassic
	}
}

func drawGeneratedClassic(img *image.NRGBA, title, author string, hue float64, seed int) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	top := hslToNRGBA(hue, 0.40, 0.31)
	bottom := hslToNRGBA(math.Mod(hue+24, 360), 0.48, 0.17)
	fillVerticalGradient(img, top, bottom)

	drawGeneratedAccents(img, hue, seed)

	panelTop := h * 54 / 100
	draw.Draw(
		img,
		image.Rect(0, panelTop, w, h),
		&image.Uniform{C: color.NRGBA{R: 0, G: 0, B: 0, A: 86}},
		image.Point{},
		draw.Over,
	)

	return drawGeneratedTextBlock(img, generatedTextBlock{
		Title:          title,
		Author:         author,
		X:              w * 11 / 100,
		Y:              panelTop,
		Width:          w * 78 / 100,
		Height:         h - panelTop,
		TitleSize:      float64(w) * 0.087,
		AuthorSize:     float64(w) * 0.055,
		TitleColor:     color.NRGBA{R: 0xf8, G: 0xf6, B: 0xf0, A: 0xff},
		AuthorColor:    color.NRGBA{R: 0xf8, G: 0xf6, B: 0xf0, A: 0xd8},
		MaxTitleLines:  5,
		MaxAuthorLines: 2,
		Align:          generatedTextCenter,
	})
}

func drawGeneratedBands(img *image.NRGBA, title, author string, hue float64, seed int) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	bg := hslToNRGBA(math.Mod(hue+16, 360), 0.28, 0.88)
	draw.Draw(img, b, &image.Uniform{C: bg}, image.Point{}, draw.Src)

	spine := hslToNRGBA(hue, 0.50, 0.32)
	draw.Draw(img, image.Rect(0, 0, w*16/100, h), &image.Uniform{C: spine}, image.Point{}, draw.Src)

	accent := hslToNRGBA(math.Mod(hue+192, 360), 0.44, 0.43)
	accent2 := hslToNRGBA(math.Mod(hue+42+float64(seed%5)*6, 360), 0.58, 0.57)
	draw.Draw(img, image.Rect(w*16/100, h*13/100, w, h*20/100), &image.Uniform{C: accent}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(w*16/100, h*22/100, w*82/100, h*25/100), &image.Uniform{C: accent2}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(w*16/100, h*82/100, w, h*86/100), &image.Uniform{C: accent}, image.Point{}, draw.Src)

	return drawGeneratedTextBlock(img, generatedTextBlock{
		Title:          title,
		Author:         author,
		X:              w * 24 / 100,
		Y:              h * 29 / 100,
		Width:          w * 62 / 100,
		Height:         h * 42 / 100,
		TitleSize:      float64(w) * 0.080,
		AuthorSize:     float64(w) * 0.048,
		TitleColor:     color.NRGBA{R: 0x26, G: 0x28, B: 0x2c, A: 0xff},
		AuthorColor:    color.NRGBA{R: 0x4e, G: 0x53, B: 0x58, A: 0xff},
		MaxTitleLines:  5,
		MaxAuthorLines: 2,
		Align:          generatedTextLeft,
	})
}

func drawGeneratedLabel(img *image.NRGBA, title, author string, hue float64, seed int) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	top := hslToNRGBA(math.Mod(hue+8, 360), 0.46, 0.28)
	bottom := hslToNRGBA(math.Mod(hue+172, 360), 0.34, 0.22)
	fillVerticalGradient(img, top, bottom)

	drawGeneratedAccents(img, math.Mod(hue+float64(seed%9)*11, 360), seed+9)

	labelRect := image.Rect(w*13/100, h*18/100, w*87/100, h*78/100)
	shadowRect := labelRect.Add(image.Pt(w*2/100, h*2/100))
	draw.Draw(img, shadowRect, &image.Uniform{C: color.NRGBA{R: 0, G: 0, B: 0, A: 72}}, image.Point{}, draw.Over)
	draw.Draw(img, labelRect, &image.Uniform{C: color.NRGBA{R: 0xf7, G: 0xf4, B: 0xec, A: 0xff}}, image.Point{}, draw.Src)
	border := hslToNRGBA(math.Mod(hue+188, 360), 0.32, 0.42)
	borderH := max(2, h*1/100)
	draw.Draw(img, image.Rect(labelRect.Min.X, labelRect.Min.Y, labelRect.Max.X, labelRect.Min.Y+borderH), &image.Uniform{C: border}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(labelRect.Min.X, labelRect.Max.Y-borderH, labelRect.Max.X, labelRect.Max.Y), &image.Uniform{C: border}, image.Point{}, draw.Src)

	return drawGeneratedTextBlock(img, generatedTextBlock{
		Title:          title,
		Author:         author,
		X:              labelRect.Min.X + w*6/100,
		Y:              labelRect.Min.Y + h*5/100,
		Width:          labelRect.Dx() - w*12/100,
		Height:         labelRect.Dy() - h*10/100,
		TitleSize:      float64(w) * 0.074,
		AuthorSize:     float64(w) * 0.047,
		TitleColor:     color.NRGBA{R: 0x22, G: 0x22, B: 0x20, A: 0xff},
		AuthorColor:    color.NRGBA{R: 0x66, G: 0x5f, B: 0x55, A: 0xff},
		MaxTitleLines:  6,
		MaxAuthorLines: 2,
		Align:          generatedTextCenter,
	})
}

func drawGeneratedQuiet(img *image.NRGBA, title, author string, hue float64, seed int) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	bg := hslToNRGBA(math.Mod(hue+218, 360), 0.24, 0.18)
	draw.Draw(img, b, &image.Uniform{C: bg}, image.Point{}, draw.Src)

	rule := hslToNRGBA(math.Mod(hue+34, 360), 0.48, 0.58)
	rule.A = 210
	for i := range 4 {
		x := w*(20+i*13)/100 + positiveMod(seed*17+i*23, max(1, w/24))
		draw.Draw(img, image.Rect(x, h*10/100, x+w/160+1, h*90/100), &image.Uniform{C: rule}, image.Point{}, draw.Over)
	}
	topRule := hslToNRGBA(math.Mod(hue+182, 360), 0.36, 0.50)
	topRule.A = 160
	draw.Draw(img, image.Rect(w*15/100, h*18/100, w*85/100, h*19/100), &image.Uniform{C: topRule}, image.Point{}, draw.Over)
	draw.Draw(img, image.Rect(w*15/100, h*81/100, w*85/100, h*82/100), &image.Uniform{C: topRule}, image.Point{}, draw.Over)

	return drawGeneratedTextBlock(img, generatedTextBlock{
		Title:          title,
		Author:         author,
		X:              w * 17 / 100,
		Y:              h * 24 / 100,
		Width:          w * 66 / 100,
		Height:         h * 52 / 100,
		TitleSize:      float64(w) * 0.078,
		AuthorSize:     float64(w) * 0.050,
		TitleColor:     color.NRGBA{R: 0xf3, G: 0xf0, B: 0xe8, A: 0xff},
		AuthorColor:    color.NRGBA{R: 0xd8, G: 0xcf, B: 0xbd, A: 0xff},
		MaxTitleLines:  5,
		MaxAuthorLines: 2,
		Align:          generatedTextCenter,
	})
}

type generatedTextAlign int

const (
	generatedTextCenter generatedTextAlign = iota
	generatedTextLeft
)

type generatedTextBlock struct {
	Title          string
	Author         string
	X              int
	Y              int
	Width          int
	Height         int
	TitleSize      float64
	AuthorSize     float64
	TitleColor     color.NRGBA
	AuthorColor    color.NRGBA
	MaxTitleLines  int
	MaxAuthorLines int
	Align          generatedTextAlign
}

func drawGeneratedTextBlock(img draw.Image, block generatedTextBlock) error {
	authorFace, err := newFace(fontRegular, block.AuthorSize)
	if err != nil {
		return err
	}
	defer authorFace.Close()

	textWidth := fixed.I(block.Width)
	var authorLines []string
	if block.Author != "" {
		authorLines = wrapText(authorFace, block.Author, textWidth, block.MaxAuthorLines)
	}

	authorM := authorFace.Metrics()
	authorLH := authorM.Height.Ceil()
	gap := max(8, block.Height*7/100)

	titleSize := block.TitleSize * generatedTitleScale(block.Title)
	minTitleSize := block.TitleSize * 0.82
	var titleFace font.Face
	var titleLines []string
	var titleM font.Metrics
	var titleLH int
	var total int
	for {
		face, err := newFace(fontBold, titleSize)
		if err != nil {
			return err
		}
		lines := wrapText(face, block.Title, textWidth, block.MaxTitleLines)
		metrics := face.Metrics()
		lineHeight := metrics.Height.Ceil()
		candidateTotal := len(lines) * lineHeight
		if len(authorLines) > 0 {
			candidateTotal += gap + len(authorLines)*authorLH
		}
		if candidateTotal <= block.Height || titleSize <= minTitleSize {
			titleFace = face
			titleLines = lines
			titleM = metrics
			titleLH = lineHeight
			total = candidateTotal
			break
		}
		face.Close()
		titleSize *= 0.92
		if titleSize < minTitleSize {
			titleSize = minTitleSize
		}
	}
	defer titleFace.Close()

	y := max(block.Y+(block.Height-total)/2, block.Y)

	for _, line := range titleLines {
		drawGeneratedLine(img, titleFace, line, block, y+titleM.Ascent.Ceil(), block.TitleColor)
		y += titleLH
	}
	if len(authorLines) > 0 {
		y += gap
		for _, line := range authorLines {
			drawGeneratedLine(img, authorFace, line, block, y+authorM.Ascent.Ceil(), block.AuthorColor)
			y += authorLH
		}
	}
	return nil
}

func generatedTitleScale(title string) float64 {
	words := strings.Fields(title)
	runes := utf8.RuneCountInString(strings.Join(words, ""))
	switch {
	case len(words) <= 1 && runes <= 10:
		return 1.44
	case len(words) <= 2 && runes <= 18:
		return 1.28
	case runes <= 32:
		return 1.15
	case runes <= 52:
		return 1.06
	case runes <= 76:
		return 0.96
	default:
		return 0.88
	}
}

func drawGeneratedLine(dst draw.Image, face font.Face, line string, block generatedTextBlock, baseline int, col color.NRGBA) {
	if block.Align == generatedTextCenter {
		drawCentered(dst, face, line, block.X*2+block.Width, baseline, col)
		return
	}
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(block.X, baseline),
	}
	d.DrawString(line)
}

func fillVerticalGradient(img *image.NRGBA, top, bottom color.NRGBA) {
	b := img.Bounds()
	h := max(1, b.Dy()-1)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		t := float64(y-b.Min.Y) / float64(h)
		c := color.NRGBA{
			R: lerp8(top.R, bottom.R, t),
			G: lerp8(top.G, bottom.G, t),
			B: lerp8(top.B, bottom.B, t),
			A: 255,
		}
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func drawGeneratedAccents(img draw.Image, hue float64, seed int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	palette := []color.NRGBA{
		hslToNRGBA(math.Mod(hue+42, 360), 0.55, 0.58),
		hslToNRGBA(math.Mod(hue+188, 360), 0.42, 0.52),
		hslToNRGBA(math.Mod(hue+310, 360), 0.48, 0.62),
	}
	for i := range 15 {
		r := w * (5 + (i+seed)%5) / 100
		x := positiveMod(seed*113+i*197, w+2*r) - r
		y := positiveMod(seed*71+i*149, h*56/100+2*r) - r
		c := palette[i%len(palette)]
		c.A = uint8(30 + (i%4)*12)
		fillCircle(img, x, y, r, c)
	}
}

func fillCircle(dst draw.Image, cx, cy, r int, col color.NRGBA) {
	if r <= 0 {
		return
	}
	b := dst.Bounds()
	src := &image.Uniform{C: col}
	rr := r * r
	for y := max(b.Min.Y, cy-r); y <= min(b.Max.Y-1, cy+r); y++ {
		dy := y - cy
		dx := int(math.Sqrt(float64(rr - dy*dy)))
		x0 := max(b.Min.X, cx-dx)
		x1 := min(b.Max.X, cx+dx+1)
		if x0 < x1 {
			draw.Draw(dst, image.Rect(x0, y, x1, y+1), src, image.Point{}, draw.Over)
		}
	}
}

func lerp8(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a)*(1-t) + float64(b)*t))
}

func positiveMod(v, m int) int {
	if m <= 0 {
		return 0
	}
	v %= m
	if v < 0 {
		v += m
	}
	return v
}
