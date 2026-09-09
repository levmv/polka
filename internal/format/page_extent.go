package format

import (
	"math"
	"unicode"

	"golang.org/x/text/width"
)

// pageExtent measures a stable reference layout, independent of the reader's
// viewport. Short paragraphs occupy their own lines; inline markup and archive
// boundaries occupy no space. Round once for the whole book.
type pageExtent struct {
	lines        int
	column       int
	wordColumns  int
	pendingSpace bool
}

const (
	// About 2,500 character cells per reference page, shared across formats.
	pageColumns = 70
	pageLines   = 36
)

func (e *pageExtent) Text(text string) {
	for _, r := range text {
		switch {
		case r >= '!' && r <= '~':
			e.wordColumns++
		case unicode.IsSpace(r):
			e.flushWord()
			e.pendingSpace = true
		case r == '\u00ad', unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r), unicode.Is(unicode.Cf, r), unicode.IsControl(r):
			// Combining marks and optional hyphenation do not add a glyph cell.
		default:
			e.wordColumns++
			if r >= '\u1100' {
				if kind := width.LookupRune(r).Kind(); kind == width.EastAsianWide || kind == width.EastAsianFullwidth {
					e.wordColumns++
				}
			}
		}
	}
}

func (e *pageExtent) flushWord() {
	if e.wordColumns == 0 {
		return
	}
	gap := 0
	if e.pendingSpace && e.column > 0 {
		gap = 1
	}
	if e.column > 0 && e.column+gap+e.wordColumns > pageColumns {
		e.lines++
		e.column = 0
		gap = 0
	}
	e.column += gap + e.wordColumns
	if e.column > pageColumns {
		e.lines += (e.column - 1) / pageColumns
		e.column = (e.column-1)%pageColumns + 1
	}
	e.wordColumns, e.pendingSpace = 0, false
}

func (e *pageExtent) Break() {
	e.flushWord()
	if e.column > 0 {
		e.lines++
	}
	e.column, e.pendingSpace = 0, false
}

func (e *pageExtent) Image(w, h int) {
	if w > 0 && h > 0 && (h <= 32 || w <= 64 && h <= 64) {
		// Inline formulae and ornaments must not become full illustrations.
		e.wordColumns += min(8, max(1, (w+7)/8))
		return
	}
	e.Break()
	// Unknown images take one third of a page. Known sizes fit a 480px column
	// at 20px per line, capped at one page.
	lines := 12
	if w > 0 && h > 0 {
		scale := math.Min(1, 480/float64(w))
		lines = min(pageLines, max(1, int(math.Ceil(float64(h)*scale/20))))
	}
	e.lines += lines
}

func (e pageExtent) Pages() int {
	e.Break()
	return (e.lines + pageLines - 1) / pageLines
}
