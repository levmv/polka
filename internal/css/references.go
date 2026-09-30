package css

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Reference covers just the URL argument, including any quotes. StringImport
// lets a target which requires url() imports perform that rewrite explicitly.
type Reference struct {
	Values
	URL          string
	StringImport bool
}

func (c Component) Reference() (Reference, bool) {
	// CSS closes strings, URLs and functions implicitly at EOF. BadString and
	// BadURL remain distinct tokens and never become resource references.
	if c.Kind == URL {
		return Reference{Values: c.slice(c.value.Start, c.value.End), URL: c.Text()}, true
	}
	if c.Kind == Function && c.urlFunction {
		arg, ok := c.Children.Single()
		if ok && arg.Kind == String {
			return Reference{Values: arg.Values, URL: arg.Text()}, true
		}
	}
	return Reference{}, false
}

// Import returns the import target separately from media/layer/supports clauses.
// Their applicability is a conversion policy, not a syntax decision.
func (r Rule) Import() (Reference, Values, bool) {
	if !equalKeyword(r.Name, "import") || r.HasBlock {
		return Reference{}, Values{}, false
	}
	p := r.Prelude.Components()
	if !p.Significant() {
		return Reference{}, Values{}, false
	}
	ref, ok := p.Component.Reference()
	if p.Kind == String {
		ref, ok = Reference{Values: p.Values, URL: p.Text(), StringImport: true}, true
	}
	return ref, r.Prelude.slice(p.End, r.Prelude.End), ok
}

// References finds URLs in component values, string imports and image-set()
// alternatives. Other at-rule preludes are syntax/conditions, not dependencies:
// in particular @namespace url(...) identifies a namespace, not an asset.
// Unknown blocks and values are retained and their explicit url()s can still
// be relocated. This does not evaluate selectors, conditions or custom properties.
func (v Values) References(visit func(Reference) error) error {
	// A reference needs a literal '(' or a quoted at-rule argument. Ordinary
	// presentation attributes and sheets with only font/content strings need
	// no tokenization for resource discovery.
	raw := v.Raw()
	if !strings.Contains(raw, "(") && (!strings.Contains(raw, "@") || !strings.ContainsAny(raw, "\"'")) {
		return nil
	}
	type scope struct {
		at              string
		close           byte
		first, imageSet bool
		suppressed      bool
		inValue, custom bool // Component-value scope or a current --property.
	}
	var stack [maxDepth + 1]scope
	stack[0] = scope{first: true, inValue: v.inValue}
	depth := 0
	lex := v.lexer()
	for {
		lex.depth = v.depth + uint8(depth)
		t := lex.next()
		if t.Kind == EOF {
			return nil
		}
		if t.Trivia() || depth == 0 && !v.inValue && (t.Kind == CDO || t.Kind == CDC) {
			continue
		}
		current := &stack[depth]
		if depth > 0 && t.Is(current.close) {
			ruleEnded := current.close == '}' && !current.inValue
			depth--
			if ruleEnded {
				stack[depth].at, stack[depth].first, stack[depth].custom = "", true, false
			}
			continue
		}
		if t.Kind == AtKeyword && current.first && !current.inValue && !current.custom {
			current.at, current.first = lowerKeyword(t.Text()), true
			continue
		}
		if t.Is(';') && !current.inValue {
			current.at, current.first, current.custom = "", true, false
			continue
		}
		if t.Is(',') {
			current.first = current.imageSet
			continue
		}
		// At-keywords and {} blocks can be literal data in a custom property.
		// Neither they nor function arguments begin a stylesheet statement.
		if current.first && !current.inValue && current.at == "" && t.Kind == Ident {
			name := t.Raw()
			if strings.HasPrefix(name, "--") || strings.ContainsRune(name, '\\') && strings.HasPrefix(t.Text(), "--") {
				lookahead := lex
				next := lookahead.next()
				for next.Trivia() {
					next = lookahead.next()
				}
				current.custom = next.Is(':')
			}
		}
		allowed := !current.suppressed && (current.at == "" || current.at == "import" && current.first)
		var ref Reference
		found := false
		switch {
		case t.Kind == URL:
			if allowed {
				ref, found = (Component{Token: t}).Reference()
			}
		case t.Kind == String && current.first && (current.at == "import" || current.imageSet):
			ref, found = Reference{Values: t.Values, URL: t.Text(), StringImport: current.at == "import"}, true
		case t.Kind == Function && t.urlFunction:
			// Group just the URL to distinguish its argument from unsupported
			// modifiers, without regrouping the enclosing stylesheet.
			p := Components{lex: lex}
			if !p.consume(t) {
				return p.Err()
			}
			lex = p.lex
			if allowed {
				ref, found = p.Component.Reference()
			}
		default:
			if t.Kind == Function || t.Kind == Block {
				close := closing(t)
				inValue := current.inValue || current.custom || close != '}'
				if !inValue {
					current.at = ""
					allowed = !current.suppressed
				}
				imageSet := false
				if allowed && t.Kind == Function {
					name := t.Text()
					imageSet = equalKeyword(name, "image-set") || equalKeyword(name, "-webkit-image-set")
				}
				current.first = false
				depth++
				if depth+int(v.depth) > maxDepth {
					return ErrLimit
				}
				stack[depth] = scope{
					close: close, first: true, suppressed: !allowed,
					inValue: inValue, imageSet: imageSet,
				}
				continue
			}
		}
		if allowed && found {
			if err := visit(ref); err != nil {
				return err
			}
		}
		current.first = false
	}
}

// WithURL returns replacement CSS, preserving the original spelling on a no-op.
func (r Reference) WithURL(url string) string {
	if url == r.URL {
		return r.Raw()
	}
	quote := byte(0)
	if raw := r.Raw(); len(raw) > 0 && (raw[0] == '\'' || raw[0] == '"') {
		quote = raw[0]
	}
	return quoteURL(url, quote)
}

// AsURL converts a string import to function syntax for targets needing it.
func (r Reference) AsURL(url string) string { return "url(" + quoteURL(url, 0) + ")" }

func quoteURL(url string, quote byte) string {
	if quote == 0 {
		for i := range len(url) {
			c := url[i]
			if c <= ' ' || c == 127 || c == '(' || c == ')' || c == '\'' || c == '"' || c == '\\' {
				quote = '"'
				break
			}
		}
	}
	if quote == 0 {
		return url
	}
	var out strings.Builder
	out.Grow(len(url) + 2)
	out.WriteByte(quote)
	for _, r := range url {
		switch {
		case r == '\\' || r == rune(quote):
			out.WriteByte('\\')
			out.WriteRune(r)
		case r < 32 || r == 127:
			out.WriteByte('\\')
			out.WriteString(strconv.FormatInt(int64(r), 16))
			out.WriteByte(' ')
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte(quote)
	return out.String()
}

// RewriteReferences allocates output only when a callback changes a reference.
// The callback returns CSS, normally Reference.WithURL(resolvedURL).
func (v Values) RewriteReferences(replace func(Reference) (string, error)) (string, error) {
	var first [1]Edit // A single inline URL needs no heap-allocated edit list.
	edits := first[:0]
	err := v.References(func(ref Reference) error {
		value, err := replace(ref)
		if err != nil {
			return err
		}
		if value != ref.Raw() {
			edits = append(edits, Edit{Span: ref.Span, Text: value})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return v.Apply(edits)
}

// RewriteURLs relocates dependencies while leaving same-document fragments
// alone. References exposes those fragments to validation/dependency consumers.
func (v Values) RewriteURLs(resolve func(string) (string, error)) (string, error) {
	return v.RewriteReferences(func(ref Reference) (string, error) {
		if ref.URL == "" || strings.HasPrefix(ref.URL, "#") {
			return ref.Raw(), nil
		}
		url, err := resolve(ref.URL)
		return ref.WithURL(url), err
	})
}

type Edit struct {
	Span
	Text string
}

// Apply makes a single output allocation, preserving every untouched byte.
// Overlapping or out-of-range edits are errors, never silently dropped changes.
func (v Values) Apply(edits []Edit) (string, error) {
	if len(edits) == 0 {
		return v.Raw(), nil
	}
	slices.SortFunc(edits, func(a, b Edit) int { return a.Start - b.Start })
	end, size := v.Start, v.End-v.Start
	for _, edit := range edits {
		if edit.Start < end || edit.End < edit.Start || edit.End > v.End {
			return "", fmt.Errorf("invalid or overlapping CSS edit: %v", edit.Span)
		}
		size += len(edit.Text) - (edit.End - edit.Start)
		end = edit.End
	}
	var out strings.Builder
	out.Grow(size)
	end = v.Start
	for _, edit := range edits {
		out.WriteString(v.source[end:edit.Start])
		out.WriteString(edit.Text)
		end = edit.End
	}
	out.WriteString(v.source[end:v.End])
	return out.String(), nil
}
