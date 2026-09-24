package converter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// MOBI6 has no CSS cascade. Project the presentation it can express into its
// legacy tags/attributes; retain ordinary HTML semantics for unsupported layout.
type mobi6CSSRule struct {
	selector    []mobi6SelectorPart
	values      map[string]string
	specificity int
	pseudo      string
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

var mobi6SimpleSelector = regexp.MustCompile(`^(\*|[a-zA-Z][a-zA-Z0-9_-]*)?([.#][a-zA-Z_-][a-zA-Z0-9_-]*)*$`)
var mobi6IDClassSelector = regexp.MustCompile(`[.#][a-zA-Z_-][a-zA-Z0-9_-]*`)
var mobi6AttributeName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_:-]*$`)

func mobi6StripCSSComments(raw string) string {
	if !strings.Contains(raw, "/*") {
		return raw
	}
	var out strings.Builder
	quote := byte(0)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '\\' && i+1 < len(raw) {
			out.WriteString(raw[i : i+2])
			i++
			continue
		}
		if quote == 0 && strings.HasPrefix(raw[i:], "/*") {
			end := strings.Index(raw[i+2:], "*/")
			if end < 0 {
				break
			}
			i += end + 3
			continue
		}
		out.WriteByte(c)
		if quote == c {
			quote = 0
		} else if quote == 0 && (c == '\'' || c == '"') {
			quote = c
		}
	}
	return out.String()
}

func mobi6DeclarationValue(value string) (string, bool) {
	if i := strings.LastIndexByte(value, '!'); i >= 0 && strings.EqualFold(strings.TrimSpace(value[i+1:]), "important") {
		return strings.TrimSpace(value[:i]), true
	}
	return value, false
}

// Compile the supported selector subset once. Quoted attribute values remain
// intact, and an unsupported selector is never widened to a partial match.
func mobi6CompileSelector(selector string) ([]mobi6SelectorPart, int) {
	var parts []mobi6SelectorPart
	specificity, child := 0, false
	for pos := 0; pos < len(selector); {
		if strings.ContainsRune(" \t\r\n\f", rune(selector[pos])) {
			pos++
			continue
		}
		if selector[pos] == '>' {
			if len(parts) == 0 || child {
				return nil, 0
			}
			child = true
			pos++
			continue
		}
		end := mobi6CSSDelimiter(selector, pos, " >\t\r\n\f")
		token := selector[pos:end]
		pos = end
		part := mobi6SelectorPart{child: child}
		child = false
		var simple strings.Builder
		for pos := 0; pos < len(token); {
			open := mobi6CSSDelimiter(token, pos, "[")
			simple.WriteString(token[pos:open])
			if open == len(token) {
				break
			}
			close := mobi6CSSDelimiter(token, open+1, "]")
			if close == len(token) {
				return nil, 0
			}
			name, value, equals := strings.Cut(token[open+1:close], "=")
			name = strings.TrimSpace(name)
			attribute := mobi6AttributeMatch{}
			if equals {
				attribute.operator = "="
				if strings.HasSuffix(name, "~") {
					attribute.operator = "~="
					name = strings.TrimSpace(strings.TrimSuffix(name, "~"))
				}
				value = strings.TrimSpace(value)
				if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, `'`) {
					var err error
					value, err = mobi6CSSContent(value)
					if err != nil {
						return nil, 0
					}
				} else if !mobi6AttributeName.MatchString(value) {
					return nil, 0
				}
				attribute.value = value
			}
			attribute.name = strings.ToLower(strings.ReplaceAll(name, "|", ":"))
			if !mobi6AttributeName.MatchString(attribute.name) {
				return nil, 0
			}
			part.attributes = append(part.attributes, attribute)
			specificity += 10
			pos = close + 1
		}
		bare := simple.String()
		if !mobi6SimpleSelector.MatchString(bare) {
			return nil, 0
		}
		name := bare
		if i := strings.IndexAny(name, ".#"); i >= 0 {
			name = name[:i]
		}
		part.tag = strings.ToLower(name)
		if name != "" && name != "*" {
			specificity++
		}
		for _, term := range mobi6IDClassSelector.FindAllString(bare, -1) {
			attribute := mobi6AttributeMatch{name: "class", value: term[1:], operator: "~="}
			specificity += 10
			if term[0] == '#' {
				attribute.name, attribute.operator = "id", "="
				specificity += 90
			}
			part.attributes = append(part.attributes, attribute)
		}
		parts = append(parts, part)
		if len(parts) > 64 {
			return nil, 0
		}
	}
	if child {
		return nil, 0
	}
	return parts, specificity
}

func (s *mobi6Source) readDocumentStyles(doc *mobi6Document) error {
	var load func(string, string, map[string]bool) ([]mobi6CSSRule, error)
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
		rules, err := mobi6ParseCSS(string(raw), func(href string) ([]mobi6CSSRule, error) { return load(file.Name, href, active) }, s.options)
		if err != nil {
			return nil, fmt.Errorf("stylesheet %s: %w", file.Name, err)
		}
		s.styles[file] = rules
		return rules, nil
	}
	return mobi6Walk(doc.root, func(n *html.Node) error {
		if err := checkContext(s.ctx); err != nil {
			return err
		}
		if n.Type != html.ElementNode {
			return nil
		}
		var rules []mobi6CSSRule
		var err error
		if n.Data == "link" && containsToken(attrValue(n, "rel"), "stylesheet") {
			rules, err = load(doc.name, attrValue(n, "href"), map[string]bool{})
		} else if n.Data == "style" {
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
		doc.css = append(doc.css, rules...)
		if len(doc.css) > 4096 {
			return fmt.Errorf("too many MOBI6 style rules: %w", ErrResourceLimit)
		}
		return nil
	})
}

// The scanner respects strings and parentheses when locating block/statement
// boundaries. Unsupported selectors never become broader partially parsed ones.
func mobi6CSSDelimiter(s string, start int, delimiters string) int {
	quote := byte(0)
	parens := 0
	for i := start; i < len(s); i++ {
		c := s[i]
		if c == '\\' {
			i++
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if strings.HasPrefix(s[i:], "/*") {
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return len(s)
			}
			i += end + 3
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		if parens == 0 && strings.ContainsRune(delimiters, rune(c)) {
			return i
		}
		if c == '(' || c == '[' {
			parens++
		}
		if c == ')' || c == ']' {
			parens--
		}
	}
	return len(s)
}

func mobi6ParseCSS(raw string, imported func(string) ([]mobi6CSSRule, error), opts ConversionOptions) ([]mobi6CSSRule, error) {
	return mobi6ParseCSSRules(raw, imported, 0, opts)
}

func mobi6ParseCSSRules(raw string, imported func(string) ([]mobi6CSSRule, error), depth int, opts ConversionOptions) ([]mobi6CSSRule, error) {
	if depth > 32 {
		return nil, fmt.Errorf("CSS rule nesting exceeds limit: %w", ErrResourceLimit)
	}
	raw = mobi6StripCSSComments(raw)
	var rules []mobi6CSSRule
	for pos := 0; pos < len(raw); {
		end := mobi6CSSDelimiter(raw, pos, "{;")
		selector := strings.TrimSpace(raw[pos:end])
		if end == len(raw) {
			break
		}
		pos = end + 1
		if raw[end] == ';' {
			if len(selector) > 7 && strings.EqualFold(selector[:7], "@import") && strings.ContainsRune(" \t\r\n", rune(selector[7])) {
				href, media := strings.TrimSpace(selector[7:]), ""
				if strings.HasPrefix(href, "url(") {
					close := mobi6CSSDelimiter(href, 4, ")")
					if close == len(href) {
						return nil, fmt.Errorf("invalid CSS import")
					}
					media, href = strings.TrimSpace(href[close+1:]), strings.TrimSpace(href[4:close])
				} else if len(href) > 1 && (href[0] == '"' || href[0] == '\'') {
					end := mobi6CSSDelimiter(href, 0, " \t\r\n")
					media, href = strings.TrimSpace(href[end:]), href[:end]
				}
				if strings.EqualFold(media, "print") {
					continue
				}
				if strings.HasPrefix(href, `"`) || strings.HasPrefix(href, `'`) {
					var err error
					href, err = mobi6CSSContent(href)
					if err != nil {
						return nil, err
					}
				}
				more, err := imported(href)
				if err != nil {
					return nil, err
				}
				if err := mobi6AppendMediaRules(&rules, more, media, opts); err != nil {
					return nil, err
				}
			}
			continue
		}
		level, close := 1, pos
		for level > 0 && close < len(raw) {
			next := mobi6CSSDelimiter(raw, close, "{}")
			if next == len(raw) {
				close = next
				break
			}
			if raw[next] == '{' {
				level++
			} else {
				level--
			}
			close = next + 1
		}
		if level != 0 {
			return nil, fmt.Errorf("unterminated CSS block")
		}
		body := raw[pos : close-1]
		pos = close
		if strings.HasPrefix(selector, "@") {
			if strings.EqualFold(selector, "@font-face") || strings.EqualFold(selector, "@media print") {
				continue
			}
			more, err := mobi6ParseCSSRules(body, imported, depth+1, opts)
			if err != nil {
				return nil, err
			}
			media := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(selector), "@media"))
			if err := mobi6AppendMediaRules(&rules, more, media, opts); err != nil {
				return nil, err
			}
			continue
		}
		values := mobi6CSSDeclarations(body)
		if len(values) == 0 {
			continue
		}
		for pos := 0; pos < len(selector); {
			end := mobi6CSSDelimiter(selector, pos, ",")
			part := strings.TrimSpace(selector[pos:end])
			pos = end + 1
			if len(part) > 1024 {
				return nil, fmt.Errorf("CSS selector exceeds limit: %w", ErrResourceLimit)
			}
			pseudo := ""
			for _, name := range []string{"before", "after"} {
				if strings.HasSuffix(part, ":"+name) {
					part = strings.TrimSuffix(strings.TrimSuffix(part, ":"+name), ":")
					pseudo = name
				}
			}
			compiled, specificity := mobi6CompileSelector(part)
			if len(compiled) > 0 {
				if len(rules) >= 4096 {
					return nil, fmt.Errorf("too many CSS rules: %w", ErrResourceLimit)
				}
				rules = append(rules, mobi6CSSRule{selector: compiled, values: values, specificity: specificity, pseudo: pseudo})
			} else if err := mobi6UnprojectedCSS(values); err != nil {
				opts.warn("%v", err)
			}
		}
	}
	return rules, nil
}

func mobi6AppendMediaRules(rules *[]mobi6CSSRule, more []mobi6CSSRule, media string, opts ConversionOptions) error {
	switch strings.ToLower(media) {
	case "", "all", "screen":
		if len(*rules)+len(more) > 4096 {
			return fmt.Errorf("too many CSS rules: %w", ErrResourceLimit)
		}
		*rules = append(*rules, more...)
	default:
		// Unknown conditions must not become unconditional. Warn when skipping
		// them could lose generated words or artwork.
		for _, rule := range more {
			if err := mobi6UnprojectedCSS(rule.values); err != nil {
				opts.warn("%v", err)
			}
		}
	}
	return nil
}

func mobi6UnprojectedCSS(values map[string]string) error {
	if value, ok := values["content"]; ok {
		value, _ = mobi6DeclarationValue(value)
		text, err := mobi6CSSContent(value)
		if err != nil || text != "" {
			return fmt.Errorf("MOBI6 cannot preserve content in unsupported CSS rule: %w", ErrUnsupportedContent)
		}
	}
	if strings.Contains(strings.ToLower(values["background-image"]), "url(") {
		return fmt.Errorf("MOBI6 cannot preserve artwork in unsupported CSS rule: %w", ErrUnsupportedContent)
	}
	return nil
}

func mobi6CSSDeclarations(raw string) map[string]string {
	raw = mobi6StripCSSComments(raw)
	values := map[string]string{}
	for pos := 0; pos < len(raw); {
		end := mobi6CSSDelimiter(raw, pos, ";")
		key, value, found := strings.Cut(raw[pos:end], ":")
		pos = end + 1
		if !found {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "font-weight", "font-style", "font-family", "text-align", "text-transform", "text-decoration", "white-space", "display", "text-indent":
			value = strings.ToLower(value)
		case "background", "background-image":
			key = "background-image"
		case "content":
		default:
			continue
		}
		_, previousImportant := mobi6DeclarationValue(values[key])
		_, important := mobi6DeclarationValue(value)
		if !previousImportant || important {
			values[key] = value
		}
	}
	return values
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
	values, ranks := map[string]string{}, map[string]int{}
	apply := func(declarations map[string]string, rank int) {
		for key, value := range declarations {
			priority := rank
			value, important := mobi6DeclarationValue(value)
			if important {
				priority += 1000000
			}
			if previous, ok := ranks[key]; !ok || priority >= previous {
				values[key], ranks[key] = value, priority
			}
		}
	}
	for _, rule := range rules {
		if mobi6Matches(n, rule.selector, work) {
			if rule.pseudo != "" {
				if content, ok := rule.values["content"]; ok {
					apply(map[string]string{rule.pseudo: content}, rule.specificity)
				}
			} else {
				apply(rule.values, rule.specificity)
			}
		}
	}
	if *work > 20000000 {
		return style, fmt.Errorf("MOBI6 style work exceeds limit: %w", ErrResourceLimit)
	}
	inline := mobi6CSSDeclarations(attrValue(n, "style"))
	apply(inline, 1000)
	for key, value := range values {
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
			if number, err := strconv.Atoi(value); err == nil {
				style.bold = number >= 600
			} else if value == "bold" || value == "bolder" {
				style.bold = true
			} else if value == "normal" || value == "lighter" {
				style.bold = false
			}
		case "font-style":
			style.italic = value == "italic" || value == "oblique"
		case "font-family":
			if strings.Contains(value, "monospace") {
				style.family = "monospace"
			} else {
				style.family = ""
			}
		case "text-decoration":
			style.underline, style.strike = strings.Contains(value, "underline"), strings.Contains(value, "line-through")
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
			text, err := mobi6CSSContent(value)
			if err != nil {
				opts.warn("%v", err)
				continue
			}
			if key == "before" {
				style.before = text
			} else {
				style.after = text
			}
		}
	}
	if !style.hidden && strings.Contains(strings.ToLower(values["background-image"]), "url(") {
		opts.warn("MOBI6 cannot preserve CSS background images")
	}
	return style, nil
}

func mobi6CSSContent(value string) (string, error) {
	if value == "none" || value == "normal" {
		return "", nil
	}
	var out strings.Builder
	for value = strings.TrimSpace(value); value != ""; value = strings.TrimSpace(value) {
		if value[0] != '\'' && value[0] != '"' {
			return "", fmt.Errorf("MOBI6 cannot preserve generated CSS content %q: %w", value, ErrUnsupportedContent)
		}
		quote := value[0]
		value = value[1:]
		closed := false
		for len(value) > 0 {
			if value[0] == quote {
				value = value[1:]
				closed = true
				break
			}
			if value[0] != '\\' {
				out.WriteByte(value[0])
				value = value[1:]
				continue
			}
			value = value[1:]
			i := 0
			for i < len(value) && i < 6 && strings.ContainsRune("0123456789abcdefABCDEF", rune(value[i])) {
				i++
			}
			if i > 0 {
				n, _ := strconv.ParseUint(value[:i], 16, 32)
				out.WriteRune(rune(n))
				value = value[i:]
				if len(value) > 0 && strings.ContainsRune(" \n\r\t", rune(value[0])) {
					value = value[1:]
				}
			} else if len(value) > 0 {
				out.WriteByte(value[0])
				value = value[1:]
			}
		}
		if !closed {
			return "", fmt.Errorf("unterminated CSS generated string")
		}
	}
	return out.String(), nil
}
