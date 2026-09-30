package converter

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/css"
	"golang.org/x/net/html"
)

// MOBI6 has no CSS cascade. Project the presentation it can express into its
// legacy tags/attributes; retain ordinary HTML semantics for unsupported layout.
type mobi6CSSRule struct {
	selector    []mobi6SelectorPart
	values      map[string]mobi6CSSValue
	specificity mobi6Specificity
	pseudo      string
}

// Specificity is a tuple: any number of class selectors still loses to an ID.
type mobi6Specificity [3]int // IDs, classes/attributes, types/pseudo-elements

type mobi6CSSPriority struct {
	important, inline bool
	specificity       mobi6Specificity
}

func (p mobi6CSSPriority) less(other mobi6CSSPriority) bool {
	if p.important != other.important {
		return !p.important
	}
	if p.inline != other.inline {
		return !p.inline
	}
	return slices.Compare(p.specificity[:], other.specificity[:]) < 0
}

type mobi6SelectorPart struct {
	tag        string
	attributes []mobi6AttributeMatch
	child      bool
}

type mobi6AttributeMatch struct {
	name, value string
	operator    string // empty means present; otherwise = or ~=
}

type mobi6Style struct {
	bold, italic, underline, strike, pre, hidden bool
	align, transform, family, indent             string
	before, after                                string
}

// Values are compiled once for the limited MOBI6 projection. The general CSS
// views retain every declaration, including properties this target cannot use.
type mobi6CSSValue struct {
	text      string
	important bool
	err       error
}

// Unsupported selectors fail as a whole; none may become a broader match.
func mobi6CompileSelector(selector css.Values) ([]mobi6SelectorPart, mobi6Specificity, string) {
	var parts []mobi6SelectorPart
	var specificity mobi6Specificity
	pseudo := ""
	gap, child, typeAllowed := true, false, false
	p := selector.Components()
	for p.Next() {
		c := p.Component
		if c.Kind == css.Comment {
			continue
		}
		if c.Kind == css.Whitespace {
			gap = true
			continue
		}
		if c.Is('>') {
			if len(parts) == 0 || child {
				return nil, mobi6Specificity{}, ""
			}
			gap, child = true, true
			continue
		}
		if gap {
			parts = append(parts, mobi6SelectorPart{child: child})
			gap, child, typeAllowed = false, false, true
		}
		if len(parts) > 64 {
			return nil, mobi6Specificity{}, ""
		}
		part := &parts[len(parts)-1]
		switch {
		case c.Kind == css.Ident || c.Is('*'):
			if !typeAllowed {
				return nil, mobi6Specificity{}, ""
			}
			if c.Kind == css.Ident {
				part.tag = strings.ToLower(c.Text())
				specificity[2]++
			}
		case c.Kind == css.Hash && c.ID:
			part.attributes = append(part.attributes, mobi6AttributeMatch{name: "id", value: c.Text(), operator: "="})
			specificity[0]++
		case c.Is('.'):
			for p.Next() && p.Kind == css.Comment {
			}
			if p.Kind != css.Ident {
				return nil, mobi6Specificity{}, ""
			}
			part.attributes = append(part.attributes, mobi6AttributeMatch{name: "class", value: p.Text(), operator: "~="})
			specificity[1]++
		case c.Kind == css.Block && c.Delim == '[' && c.Closed:
			attribute, ok := mobi6CompileAttribute(c.Children)
			if !ok {
				return nil, mobi6Specificity{}, ""
			}
			part.attributes = append(part.attributes, attribute)
			specificity[1]++
		case c.Is(':'):
			for p.Next() && p.Kind == css.Comment {
			}
			if p.Is(':') {
				for p.Next() && p.Kind == css.Comment {
				}
			}
			if p.Kind != css.Ident || !strings.EqualFold(p.Text(), "before") && !strings.EqualFold(p.Text(), "after") {
				return nil, mobi6Specificity{}, ""
			}
			pseudo = strings.ToLower(p.Text())
			if p.Significant() {
				return nil, mobi6Specificity{}, ""
			}
			specificity[2]++
		default:
			return nil, mobi6Specificity{}, ""
		}
		typeAllowed = false
	}
	if child || p.Err() != nil {
		return nil, mobi6Specificity{}, ""
	}
	return parts, specificity, pseudo
}

func mobi6CompileAttribute(value css.Values) (mobi6AttributeMatch, bool) {
	p := value.Components()
	if !p.Significant() || p.Kind != css.Ident {
		return mobi6AttributeMatch{}, false
	}
	attribute := mobi6AttributeMatch{name: strings.ToLower(p.Text())}
	more, spaced := p.Next(), false
	for more && p.Trivia() {
		spaced = spaced || p.Kind == css.Whitespace
		more = p.Next()
	}
	if more && p.Is('|') {
		if spaced {
			return attribute, false
		}
		for p.Next() && p.Kind == css.Comment {
		}
		if p.Kind != css.Ident {
			return attribute, false
		}
		attribute.name += ":" + strings.ToLower(p.Text())
		more = p.Significant()
	}
	if !more {
		return attribute, p.Err() == nil
	}
	if p.Is('~') {
		attribute.operator = "~"
		// ~= is an adjacent pair, not two whitespace-separated operators.
		if !p.Next() {
			return attribute, false
		}
	}
	if !p.Is('=') || !p.Significant() || p.Kind != css.Ident && p.Kind != css.String || !p.Closed {
		return attribute, false
	}
	attribute.operator += "="
	attribute.value = p.Text()
	return attribute, !p.Significant() && p.Err() == nil
}

func (s *mobi6Source) readDocumentStyles(doc *kindleDocument) error {
	var load func(string, string, map[string]bool) ([]mobi6CSSRule, error)
	cycles := 0
	load = func(base, href string, active map[string]bool) ([]mobi6CSSRule, error) {
		file, _, external, err := s.reference(base, href)
		if err != nil {
			return nil, err
		}
		if external || file == nil {
			s.options.warn("Could not include stylesheet %.200q", href)
			return nil, nil
		}
		if active[file.Name] {
			cycles++
			return nil, nil
		}
		if rules, ok := s.styles[file]; ok {
			return rules, nil
		}
		if len(active) >= 32 {
			return nil, fmt.Errorf("CSS import nesting exceeds limit: %w", ErrResourceLimit)
		}
		active[file.Name] = true
		defer delete(active, file.Name)
		raw, err := kepubReadZipFile(s.ctx, file, maxConverterPackageBytes)
		if err != nil {
			return nil, err
		}
		before := cycles
		rules, err := mobi6ParseCSS(string(raw), func(href string) ([]mobi6CSSRule, error) { return load(file.Name, href, active) }, s.options)
		if err != nil {
			return nil, fmt.Errorf("stylesheet %s: %w", file.Name, err)
		}
		// A cycle cuts this expansion according to its active import chain.
		// Only cache results that are also valid for another entry stylesheet.
		if cycles == before {
			s.styles[file] = rules
		}
		return rules, nil
	}
	return walkKindleHTML(doc.root, func(n *html.Node) error {
		if err := checkContext(s.ctx); err != nil {
			return err
		}
		if n.Type != html.ElementNode {
			return nil
		}
		linked := n.Data == "link" && containsToken(attrValue(n, "rel"), "stylesheet")
		if !linked && n.Data != "style" {
			return nil
		}
		if linked && containsToken(attrValue(n, "rel"), "alternate") {
			return nil
		}
		mediaType, _, _ := strings.Cut(attrValue(n, "type"), ";")
		if mediaType = strings.TrimSpace(mediaType); mediaType != "" && !strings.EqualFold(mediaType, "text/css") {
			return nil
		}
		media := css.Parse(attrValue(n, "media"))
		if media.Keyword("print") {
			return nil
		}
		var rules []mobi6CSSRule
		var err error
		if linked {
			rules, err = load(doc.name, attrValue(n, "href"), map[string]bool{})
		} else {
			var raw strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				raw.WriteString(c.Data)
			}
			rules, err = mobi6ParseCSS(raw.String(), func(href string) ([]mobi6CSSRule, error) { return load(doc.name, href, map[string]bool{}) }, s.options)
		}
		if err != nil {
			if fatalConversionError(err) {
				return err
			}
			s.options.warn("%s: could not preserve stylesheet: %v", doc.name, err)
			return nil
		}
		combined := s.documentStyles[doc.root]
		if err := mobi6AppendMediaRules(&combined, rules, media, s.options); err != nil {
			return err
		}
		s.documentStyles[doc.root] = combined
		return nil
	})
}

func mobi6ParseCSS(raw string, imported func(string) ([]mobi6CSSRule, error), opts ConversionOptions) ([]mobi6CSSRule, error) {
	return mobi6ParseCSSRules(css.Parse(raw), imported, opts, true)
}

func mobi6ParseCSSRules(body css.Values, imported func(string) ([]mobi6CSSRule, error), opts ConversionOptions, importsAllowed bool) ([]mobi6CSSRule, error) {
	var rules []mobi6CSSRule
	input := body.Rules()
	for input.Next() {
		rule := input.Rule
		if ref, conditions, ok := rule.Import(); ok {
			if !importsAllowed || conditions.Keyword("print") {
				continue
			}
			more, err := imported(ref.URL)
			if err != nil {
				return nil, err
			}
			if err := mobi6AppendMediaRules(&rules, more, conditions, opts); err != nil {
				return nil, err
			}
			continue
		}
		// Imports belong before style rules and block at-rules, and are never
		// allowed inside a group. A layer-order statement may precede them.
		if !strings.EqualFold(rule.Name, "charset") && !strings.EqualFold(rule.Name, "import") && !(strings.EqualFold(rule.Name, "layer") && !rule.HasBlock) {
			importsAllowed = false
		}
		if !rule.HasBlock {
			continue
		}
		if rule.Name != "" {
			if strings.EqualFold(rule.Name, "font-face") || strings.EqualFold(rule.Name, "media") && rule.Prelude.Keyword("print") {
				continue
			}
			more, err := mobi6ParseCSSRules(rule.Body, imported, opts, false)
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(rule.Name, "media") {
				if err := mobi6AppendMediaRules(&rules, more, rule.Prelude, opts); err != nil {
					return nil, err
				}
			} else {
				// A condition or cascade layer we cannot evaluate must not
				// silently become unconditional or lose its ordering semantics.
				for _, skipped := range more {
					mobi6WarnUnprojectedCSS(skipped.values, opts)
				}
			}
			continue
		}
		values, err := mobi6CSSDeclarations(rule.Body)
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			continue
		}
		selectors := rule.Prelude.Split(',')
		for selectors.Next() {
			if selectors.End-selectors.Start > 1024 {
				return nil, fmt.Errorf("CSS selector exceeds limit: %w", ErrResourceLimit)
			}
			compiled, specificity, pseudo := mobi6CompileSelector(selectors.Values)
			if len(compiled) == 0 {
				mobi6WarnUnprojectedCSS(values, opts)
				continue
			}
			if len(rules) >= 4096 {
				return nil, fmt.Errorf("too many CSS rules: %w", ErrResourceLimit)
			}
			rules = append(rules, mobi6CSSRule{selector: compiled, values: values, specificity: specificity, pseudo: pseudo})
		}
		if err := selectors.Err(); err != nil {
			return nil, err
		}
	}
	return rules, input.Err()
}

func mobi6AppendMediaRules(rules *[]mobi6CSSRule, more []mobi6CSSRule, media css.Values, opts ConversionOptions) error {
	unconditional := media.Empty()
	queries := media.Split(',')
	for queries.Next() {
		unconditional = unconditional || queries.Values.Keyword("all") || queries.Values.Keyword("screen")
	}
	if err := queries.Err(); err != nil {
		return err
	}
	if unconditional {
		if len(*rules)+len(more) > 4096 {
			return fmt.Errorf("too many CSS rules: %w", ErrResourceLimit)
		}
		*rules = append(*rules, more...)
	} else {
		for _, rule := range more {
			mobi6WarnUnprojectedCSS(rule.values, opts)
		}
	}
	return nil
}

func mobi6WarnUnprojectedCSS(values map[string]mobi6CSSValue, opts ConversionOptions) {
	if value := values["content"]; value.err != nil || value.text != "" {
		opts.warn("MOBI6 cannot preserve content in unsupported CSS rule")
	}
	if values["background-image"].text != "" {
		opts.warn("MOBI6 cannot preserve artwork in unsupported CSS rule")
	}
}

func mobi6CSSDeclarations(body css.Values) (map[string]mobi6CSSValue, error) {
	var values map[string]mobi6CSSValue
	p := body.Declarations()
	for p.Next() {
		decl := p.Declaration
		key := decl.Name
		value := mobi6CSSValue{important: decl.Important}
		switch key {
		case "font-family":
			if decl.Value.Keyword("inherit") || decl.Value.Keyword("unset") {
				value.text = "inherit"
			} else {
				families := decl.Value.Split(',')
				for families.Next() {
					if families.Values.Keyword("monospace") {
						value.text = "monospace"
					}
				}
				if err := families.Err(); err != nil {
					return nil, err
				}
			}
		case "font-weight", "font-style", "text-align", "text-transform", "text-decoration", "white-space", "display", "text-indent":
			var ok bool
			value.text, ok = mobi6CSSPresentationValue(key, decl.Value)
			if !ok {
				continue
			}
		case "background", "background-image":
			key = "background-image"
			if err := decl.Value.References(func(css.Reference) error { value.text = "image"; return nil }); err != nil {
				return nil, err
			}
		case "content":
			value.text, value.err = mobi6CSSContent(decl.Value)
		default:
			continue
		}
		if values == nil {
			values = make(map[string]mobi6CSSValue)
		}
		if !values[key].important || value.important {
			values[key] = value
		}
	}
	return values, p.Err()
}

// Reject invalid/unsupported values before the cascade so they cannot erase a
// usable fallback declaration. This is only the legacy target's projection,
// not a property grammar for stylesheets preserved by other formats.
func mobi6CSSPresentationValue(key string, value css.Values) (string, bool) {
	if key == "display" || key == "text-decoration" {
		// These properties can contain several keywords. Only none hides
		// content; unfamiliar layout modes keep ordinary HTML presentation.
		var text strings.Builder
		p := value.Components()
		for p.Significant() {
			if p.Kind != css.Ident {
				return "", false
			}
			if text.Len() > 0 {
				text.WriteByte(' ')
			}
			text.WriteString(strings.ToLower(p.Text()))
		}
		v := text.String()
		if p.Err() != nil {
			return "", false
		}
		if key == "display" {
			return v, v != ""
		}
		return v, containsToken("none inherit unset initial", v) || containsToken(v, "underline") ||
			containsToken(v, "overline") || containsToken(v, "line-through") || containsToken(v, "blink")
	}
	c, ok := value.Single()
	if !ok {
		return "", false
	}
	text := strings.ToLower(c.Text())
	if c.Kind == css.Ident && containsToken("inherit unset initial", text) {
		return text, true
	}
	switch key {
	case "font-weight":
		if c.Kind == css.Ident {
			return text, containsToken("normal bold bolder lighter", text)
		}
		weight, err := strconv.Atoi(text)
		if c.Kind == css.Number && err == nil && weight >= 1 && weight <= 1000 {
			if weight >= 600 {
				return "bold", true
			}
			return "normal", true
		}
	case "font-style":
		return text, c.Kind == css.Ident && containsToken("normal italic oblique", text)
	case "text-align":
		return text, c.Kind == css.Ident && containsToken("left right center justify start end", text)
	case "text-transform":
		return text, c.Kind == css.Ident && containsToken("none capitalize uppercase lowercase", text)
	case "white-space":
		return text, c.Kind == css.Ident && containsToken("normal pre nowrap pre-wrap pre-line break-spaces", text)
	case "text-indent":
		return text, c.Kind == css.Dimension || c.Kind == css.Percentage || c.Kind == css.Number && text == "0"
	}
	return "", false
}

func mobi6Matches(n *html.Node, parts []mobi6SelectorPart, work *int) bool {
	type state struct {
		node  *html.Node
		index int
	}
	cache := map[state]bool{}
	var match func(*html.Node, int) bool
	match = func(n *html.Node, index int) (result bool) {
		*work++
		if *work > 20000000 {
			return false
		}
		if n == nil || n.Type != html.ElementNode || index < 0 {
			return false
		}
		key := state{n, index}
		if result, found := cache[key]; found {
			return result
		}
		defer func() { cache[key] = result }()
		part := parts[index]
		if part.tag != "" && part.tag != "*" && part.tag != n.Data {
			return false
		}
		for _, attribute := range part.attributes {
			if !hasKEPUBAttr(n, attribute.name) {
				return false
			}
			actual := attrValue(n, attribute.name)
			if attribute.operator == "=" && actual != attribute.value ||
				attribute.operator == "~=" && !containsToken(actual, attribute.value) {
				return false
			}
		}
		if index == 0 {
			return true
		}
		if part.child {
			return match(n.Parent, index-1)
		}
		for p := n.Parent; p != nil; p = p.Parent {
			if match(p, index-1) {
				return true
			}
		}
		return false
	}
	return match(n, len(parts)-1)
}

func mobi6ComputedStyle(n *html.Node, rules []mobi6CSSRule, inherited mobi6Style, work *int, opts ConversionOptions) (mobi6Style, error) {
	style := inherited
	style.before, style.after = "", ""
	switch n.Data {
	case "b", "strong":
		style.bold = true
	case "i", "em", "cite":
		style.italic = true
	case "u":
		style.underline = true
	case "s", "strike", "del":
		style.strike = true
	case "pre":
		style.pre, style.family = true, "monospace"
	case "code", "tt", "kbd", "samp":
		style.family = "monospace"
	}
	type rankedValue struct {
		mobi6CSSValue
		priority mobi6CSSPriority
	}
	values := map[string]rankedValue{}
	apply := func(key string, value mobi6CSSValue, priority mobi6CSSPriority) {
		priority.important = value.important
		if previous, ok := values[key]; !ok || !priority.less(previous.priority) {
			values[key] = rankedValue{value, priority}
		}
	}
	for _, rule := range rules {
		if mobi6Matches(n, rule.selector, work) {
			if rule.pseudo != "" {
				if content, ok := rule.values["content"]; ok {
					apply(rule.pseudo, content, mobi6CSSPriority{specificity: rule.specificity})
				}
			} else {
				for key, value := range rule.values {
					apply(key, value, mobi6CSSPriority{specificity: rule.specificity})
				}
			}
		}
	}
	if *work > 20000000 {
		return style, fmt.Errorf("MOBI6 style work exceeds limit: %w", ErrResourceLimit)
	}
	inline, err := mobi6CSSDeclarations(css.Parse(attrValue(n, "style")))
	if err != nil {
		return style, err
	}
	for key, value := range inline {
		apply(key, value, mobi6CSSPriority{inline: true})
	}
	for key, declaration := range values {
		value := declaration.text
		if value == "inherit" || value == "unset" {
			switch key {
			case "font-weight":
				style.bold = inherited.bold
			case "font-style":
				style.italic = inherited.italic
			case "font-family":
				style.family = inherited.family
			case "white-space":
				style.pre = inherited.pre
			}
			continue
		}
		switch key {
		case "font-weight":
			style.bold = value == "bold" || value == "bolder"
		case "font-style":
			style.italic = value == "italic" || value == "oblique"
		case "font-family":
			if value == "monospace" {
				style.family = "monospace"
			} else {
				style.family = ""
			}
		case "text-decoration":
			style.underline, style.strike = containsToken(value, "underline"), containsToken(value, "line-through")
		case "white-space":
			style.pre = value == "pre" || value == "pre-wrap" || value == "break-spaces"
		case "text-transform":
			style.transform = value
		case "text-align":
			style.align = value
		case "text-indent":
			style.indent = value
		case "display":
			style.hidden = style.hidden || value == "none"
		case "before", "after":
			if declaration.err != nil {
				opts.warn("%v", declaration.err)
				continue
			}
			if key == "before" {
				style.before = value
			} else {
				style.after = value
			}
		}
	}
	if !style.hidden && values["background-image"].text != "" {
		opts.warn("MOBI6 cannot preserve CSS background images")
	}
	return style, nil
}

func mobi6CSSContent(value css.Values) (string, error) {
	if value.Keyword("none") || value.Keyword("normal") {
		return "", nil
	}
	var out strings.Builder
	p := value.Components()
	for p.Significant() {
		if p.Kind != css.String || !p.Closed {
			return "", fmt.Errorf("MOBI6 cannot preserve generated CSS content %.200q: %w", value.Raw(), ErrUnsupportedContent)
		}
		out.WriteString(p.Text())
	}
	return out.String(), p.Err()
}
