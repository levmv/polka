// Package css reads CSS syntax without discarding source bytes. Rules and
// values are views into the input, not a normalized CSS DOM. Format policy,
// selector matching and the cascade belong to its callers.
package css

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrLimit bounds work and stack use on hostile, deeply nested input.
var ErrLimit = errors.New("CSS nesting exceeds limit")

const maxDepth = 64

type Kind uint8

const (
	EOF Kind = iota
	Whitespace
	Comment
	Ident
	AtKeyword
	Hash
	String
	URL
	Number
	Dimension
	Percentage
	Delim
	Function
	Block
	BadString
	BadURL
	CDO
	CDC
)

// Span uses byte offsets in the original UTF-8 input, including escapes.
type Span struct{ Start, End int }

// Values is a reusable view of component values. Constructing one does no work;
// only the requested rules, declarations or components are read. Retaining a
// view retains the source string. Input decoding belongs to the container reader.
type Values struct {
	Span
	source  string
	depth   uint8 // Bounded by maxDepth.
	inValue bool  // At-keywords in a declaration/function argument are data.
}

func Parse(source string) Values { return Values{Span: Span{0, len(source)}, source: source} }
func (v Values) Raw() string     { return v.source[v.Start:v.End] }

func (v Values) slice(start, end int) Values {
	v.Span = Span{start, end}
	return v
}

// Token.Text decodes names, strings and URLs on demand. Delim is the punctuation
// byte (or the opening bracket of a block). ID distinguishes an identifier hash
// such as #chapter from an unrestricted hash such as #123.
type Token struct {
	Values
	Kind   Kind
	Delim  byte
	ID     bool
	Closed bool
	// The lexer already distinguishes quoted url() from other functions.
	urlFunction bool
	value       Span // Name or value, excluding its syntactic delimiters.
}

func (t Token) Text() string { return decode(t.source[t.value.Start:t.value.End]) }

func (t Token) Is(c byte) bool { return t.Kind == Delim && t.Delim == c }
func (t Token) Trivia() bool   { return t.Kind == Whitespace || t.Kind == Comment }

// CSS keyword matching is ASCII-insensitive, unlike Unicode case folding.
func equalKeyword(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func lowerKeyword(s string) string {
	for i := range len(s) {
		if s[i] >= 'A' && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

type lexer struct {
	Values
	pos int
}

func (v Values) lexer() lexer { return lexer{Values: v, pos: v.Start} }

func (l *lexer) next() Token {
	s, start := l.source[:l.End], l.pos
	t := Token{Values: l.slice(start, start), Closed: true}
	if start >= len(s) {
		return t
	}
	i, c := start+1, s[start]
	switch {
	case start == 0 && strings.HasPrefix(s, "\ufeff"):
		t.Kind, i = Whitespace, 3
	case space(c):
		t.Kind = Whitespace
		for i < len(s) && space(s[i]) {
			i++
		}
	case c == '/' && i < len(s) && s[i] == '*':
		t.Kind = Comment
		end := strings.Index(s[i+1:], "*/")
		t.Closed = end >= 0
		if t.Closed {
			i += end + 3
		} else {
			i = len(s)
		}
	case c == '\'' || c == '"':
		t.Kind = String
		trailingEscape := false
		for i < len(s) && s[i] != c && !newline(s[i]) {
			if s[i] == '\\' {
				trailingEscape = i+1 == len(s)
				i = escapeEnd(s, i)
			} else {
				i++
			}
		}
		t.value = Span{start + 1, i}
		if trailingEscape {
			t.value.End-- // Escaped EOF contributes no character to a string.
		}
		t.Closed = i < len(s) && s[i] == c
		if t.Closed {
			i++
		} else if i < len(s) {
			t.Kind = BadString // Leave the newline for the next token.
		}
	case strings.HasPrefix(s[start:], "<!--"):
		t.Kind, i = CDO, start+4
	case strings.HasPrefix(s[start:], "-->"):
		t.Kind, i = CDC, start+3
	case startsNumber(s, start):
		t.Kind, i = Number, numberEnd(s, start)
		t.value = Span{start, i}
		if startsIdent(s, i) {
			t.Kind, i = Dimension, nameEnd(s, i)
			t.value = Span{start, i}
		} else if i < len(s) && s[i] == '%' {
			t.Kind, i = Percentage, i+1
			t.value = Span{start, i}
		}
	case c == '@' && startsIdent(s, i):
		t.Kind, i = AtKeyword, nameEnd(s, i)
		t.value = Span{start + 1, i}
	case c == '#' && i < len(s) && (nameByte(s[i]) || validEscape(s, i)):
		t.Kind, t.ID = Hash, startsIdent(s, i)
		i = nameEnd(s, i)
		t.value = Span{start + 1, i}
	case startsIdent(s, start):
		t.Kind, i = Ident, nameEnd(s, start)
		t.value = Span{start, i}
		if i < len(s) && s[i] == '(' {
			t.Kind, i = Function, i+1
			if equalKeyword(t.Text(), "url") {
				arg := i
				for arg < len(s) && space(s[arg]) {
					arg++
				}
				// Quoted url() is a function in CSS Syntax. Its string and any
				// modifiers are read by the component parser like other functions.
				if arg == len(s) || s[arg] != '\'' && s[arg] != '"' {
					t, i = consumeURL(t, s, arg)
				} else {
					t.urlFunction = true
				}
			}
		}
	default:
		t.Kind, t.Delim = Delim, c
		if c == '(' || c == '[' || c == '{' {
			t.Kind = Block
		}
		if c >= utf8.RuneSelf {
			_, size := utf8.DecodeRuneInString(s[start:])
			i = start + size
		}
	}
	t.End, l.pos = i, i
	return t
}

func consumeURL(t Token, s string, start int) (Token, int) {
	t.Kind = URL
	i, end := start, start
	for i < len(s) {
		switch {
		case s[i] == ')':
			t.value = Span{start, end}
			return t, i + 1
		case space(s[i]):
			for i < len(s) && space(s[i]) {
				i++
			}
			if i == len(s) || s[i] == ')' {
				continue
			}
			t.Kind = BadURL
		case s[i] == '\\' && validEscape(s, i):
			i = escapeEnd(s, i)
			end = i
			continue
		case s[i] == '\'' || s[i] == '"' || s[i] == '(' || s[i] == '\\' || s[i] < 32 && s[i] != 0 || s[i] == 127:
			t.Kind = BadURL
		default:
			i++
			end = i
			continue
		}
		// A bad URL consumes through the next unescaped closing parenthesis;
		// otherwise its contents could be mistaken for real resource references.
		for i < len(s) && s[i] != ')' {
			if validEscape(s, i) {
				i = escapeEnd(s, i)
			} else {
				i++
			}
		}
		return t, min(i+1, len(s))
	}
	t.Closed = false
	t.value = Span{start, end}
	return t, i
}

func space(c byte) bool   { return c == ' ' || c == '\t' || newline(c) }
func newline(c byte) bool { return c == '\r' || c == '\n' || c == '\f' }
func digit(c byte) bool   { return c >= '0' && c <= '9' }
func hex(c byte) bool     { return digit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
func nameStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 128 || c == 0
}
func nameByte(c byte) bool { return nameStart(c) || digit(c) || c == '-' }
func validEscape(s string, i int) bool {
	return i < len(s) && s[i] == '\\' && (i+1 == len(s) || !newline(s[i+1]))
}
func startsIdent(s string, i int) bool {
	if i == len(s) {
		return false
	}
	if s[i] == '-' {
		return i+1 < len(s) && (s[i+1] == '-' || nameStart(s[i+1]) || validEscape(s, i+1))
	}
	return nameStart(s[i]) || validEscape(s, i)
}
func nameEnd(s string, i int) int {
	for i < len(s) {
		if nameByte(s[i]) {
			i++
		} else if validEscape(s, i) {
			i = escapeEnd(s, i)
		} else {
			break
		}
	}
	return i
}
func startsNumber(s string, i int) bool {
	if s[i] == '+' || s[i] == '-' {
		i++
	}
	return i < len(s) && (digit(s[i]) || s[i] == '.' && i+1 < len(s) && digit(s[i+1]))
}
func numberEnd(s string, i int) int {
	if s[i] == '+' || s[i] == '-' {
		i++
	}
	for i < len(s) && digit(s[i]) {
		i++
	}
	if i+1 < len(s) && s[i] == '.' && digit(s[i+1]) {
		for i++; i < len(s) && digit(s[i]); i++ {
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		end := i + 1
		if end < len(s) && (s[end] == '+' || s[end] == '-') {
			end++
		}
		if end < len(s) && digit(s[end]) {
			for i = end + 1; i < len(s) && digit(s[i]); i++ {
			}
		}
	}
	return i
}

func escapeEnd(s string, start int) int {
	i := start + 1
	for i < len(s) && i <= start+6 && hex(s[i]) {
		i++
	}
	if i == start+1 {
		if i == len(s) {
			return i
		}
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			return i + 2
		}
		_, n := utf8.DecodeRuneInString(s[i:])
		return i + n
	}
	if i < len(s) && space(s[i]) {
		if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
			i++
		}
		i++
	}
	return i
}

func decode(s string) string {
	if !strings.ContainsAny(s, "\\\x00") && utf8.ValidString(s) {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\\' {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == 0 {
				r = utf8.RuneError
			}
			out.WriteRune(r)
			i += size
			continue
		}
		end := escapeEnd(s, i)
		if i+1 == len(s) {
			out.WriteRune(utf8.RuneError)
		} else if hex(s[i+1]) {
			n, _ := strconv.ParseUint(strings.TrimRight(s[i+1:end], " \t\r\n\f"), 16, 32)
			r := rune(n)
			if r == 0 || !utf8.ValidRune(r) {
				r = utf8.RuneError
			}
			out.WriteRune(r)
		} else if !newline(s[i+1]) {
			r, _ := utf8.DecodeRuneInString(s[i+1 : end])
			if r == 0 {
				r = utf8.RuneError
			}
			out.WriteRune(r)
		}
		i = end
	}
	return out.String()
}
