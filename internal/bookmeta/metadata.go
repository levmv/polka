package bookmeta

import (
	"strings"
	"unicode"
)

// Metadata represents book metadata extracted from a file.
type Metadata struct {
	Title       string
	SortTitle   string
	Authors     []AuthorMeta
	Language    string
	Description string
	Publisher   string
	Date        string
	Identifier  string
	Series      string
	SeriesIndex float64
	Tags        []string
}

// AuthorMeta holds information about an author.
type AuthorMeta struct {
	Name     string
	SortName string
	Role     string
}

// Merge overlays o onto m: every field o sets non-empty wins. Used to let a
// curated metadata sidecar take precedence over metadata embedded in the book
// file.
func (m *Metadata) Merge(o *Metadata) {
	if o == nil {
		return
	}
	if o.Title != "" {
		m.Title = o.Title
	}
	if o.SortTitle != "" {
		m.SortTitle = o.SortTitle
	}
	if len(o.Authors) > 0 {
		m.Authors = o.Authors
	}
	if o.Language != "" {
		m.Language = o.Language
	}
	if o.Description != "" {
		m.Description = o.Description
	}
	if o.Publisher != "" {
		m.Publisher = o.Publisher
	}
	if o.Date != "" {
		m.Date = o.Date
	}
	if o.Identifier != "" {
		m.Identifier = o.Identifier
	}
	if o.Series != "" {
		m.Series = o.Series
	}
	if o.SeriesIndex != 0 {
		m.SeriesIndex = o.SeriesIndex
	}
	if len(o.Tags) > 0 {
		m.Tags = o.Tags
	}
}

// Keep these author-sort heuristic sets synchronized with frontend/src/authors.ts.
// They are duplicated deliberately: Go uses them during import/maintenance, while
// the frontend uses them for the edit form's explicit Auto action.
var authorNameCopyWords = map[string]struct{}{
	"agency": {}, "association": {}, "bureau": {}, "center": {},
	"centre": {}, "co": {}, "co.": {}, "collective": {}, "college": {},
	"committee": {}, "company": {}, "corp": {}, "corp.": {},
	"corporation": {}, "council": {}, "department": {}, "foundation": {},
	"group": {}, "guild": {}, "inc": {}, "inc.": {}, "institute": {},
	"laboratory": {}, "labs": {}, "llc": {}, "ltd": {}, "ltd.": {},
	"media": {}, "ministry": {}, "office": {}, "organization": {},
	"organisation": {}, "press": {}, "project": {}, "publisher": {},
	"publishers": {}, "publishing": {}, "society": {}, "studio": {},
	"studios": {}, "team": {}, "university": {},
}

var authorNamePrefixes = map[string]struct{}{
	"mr": {}, "mr.": {}, "mrs": {}, "mrs.": {}, "ms": {}, "ms.": {},
	"miss": {}, "miss.": {}, "dr": {}, "dr.": {}, "prof": {}, "prof.": {},
	"sir": {}, "dame": {},
}

var authorNameSuffixes = map[string]struct{}{
	"jr": {}, "jr.": {}, "sr": {}, "sr.": {}, "esq": {}, "esq.": {},
	"ph.d": {}, "ph.d.": {}, "phd": {}, "phd.": {}, "md": {}, "md.": {},
	"m.d": {}, "m.d.": {}, "i": {}, "i.": {}, "ii": {}, "ii.": {},
	"iii": {}, "iii.": {}, "iv": {}, "iv.": {}, "v": {}, "v.": {},
	"junior": {}, "junior.": {}, "senior": {}, "senior.": {},
}

// AuthorSort normalizes a single author name to the common library "Last, First"
// form. Existing comma forms and obvious organization names are copied,
// honorific prefixes are ignored, and common suffixes are preserved after the
// inverted name.
func AuthorSort(name string) string {
	author := strings.TrimSpace(name)
	if author == "" {
		return ""
	}
	if author == "Unknown" || author == "Unknown Author" {
		return "Unknown Author"
	}

	sortSource := strings.TrimSpace(removeBracketedAuthorText(author))
	if strings.Contains(sortSource, ",") {
		return author
	}

	tokens := strings.Fields(sortSource)
	if len(tokens) <= 1 {
		return author
	}

	for _, token := range tokens {
		if _, ok := authorNameCopyWords[strings.ToLower(token)]; ok {
			return author
		}
	}

	first := 0
	for ; first < len(tokens); first++ {
		if _, ok := authorNamePrefixes[strings.ToLower(tokens[first])]; !ok {
			break
		}
	}
	if first == len(tokens) {
		return author
	}

	last := len(tokens) - 1
	for ; last >= first; last-- {
		if _, ok := authorNameSuffixes[strings.ToLower(tokens[last])]; !ok {
			break
		}
	}
	if last < first {
		return author
	}

	suffix := strings.Join(tokens[last+1:], " ")
	sortTokens := append([]string{tokens[last]}, tokens[first:last]...)
	if len(sortTokens) > 1 {
		sortTokens[0] += ","
	}
	if suffix != "" {
		sortTokens = append(sortTokens, suffix)
	}
	return strings.Join(sortTokens, " ")
}

func removeBracketedAuthorText(src string) string {
	brackets := map[rune]rune{'(': ')', '[': ']', '{': '}'}
	reverse := map[rune]rune{')': '(', ']': '[', '}': '{'}
	counts := map[rune]int{}
	depth := 0
	var b strings.Builder
	for _, r := range src {
		if _, ok := brackets[r]; ok {
			counts[r]++
			depth++
			continue
		}
		if opener, ok := reverse[r]; ok {
			if counts[opener] > 0 {
				counts[opener]--
				depth--
				continue
			}
		}
		if depth == 0 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseAuthorList parses the text-compatible author editor value. Commas are
// valid inside author names, so only semicolons and delimiter-like ampersands
// split authors. A doubled ampersand is kept as a literal ampersand.
func ParseAuthorList(raw string) []string {
	var authors []string
	var b strings.Builder
	runes := []rune(raw)

	flush := func() {
		name := strings.TrimSpace(b.String())
		b.Reset()
		if name != "" {
			authors = append(authors, name)
		}
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == ';':
			flush()
		case r == '&' && i+1 < len(runes) && runes[i+1] == '&':
			b.WriteRune('&')
			i++
		case r == '&' && isAuthorAmpersandDelimiter(runes, i):
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()

	return authors
}

// FormatAuthorList serializes author names for the editor. Semicolon is the
// canonical separator; ampersands inside names are escaped so a later parse does
// not treat "A & B" as two authors.
func FormatAuthorList(names []string) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		parts = append(parts, strings.ReplaceAll(name, "&", "&&"))
	}
	return strings.Join(parts, "; ")
}

func isAuthorAmpersandDelimiter(runes []rune, i int) bool {
	prevSpace := i == 0 || unicode.IsSpace(runes[i-1])
	nextSpace := i == len(runes)-1 || unicode.IsSpace(runes[i+1])
	return prevSpace && nextSpace
}
