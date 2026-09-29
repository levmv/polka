package kfx

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

var lengthProperties = map[int]string{
	16: "font-size", 31: "vertical-align", 32: "letter-spacing", 33: "word-spacing", 36: "text-indent", 42: "line-height",
	46: "margin",
	47: "margin-top", 48: "margin-left", 49: "margin-bottom", 50: "margin-right",
	51: "padding", 52: "padding-top", 53: "padding-left", 54: "padding-bottom", 55: "padding-right",
	56: "width", 57: "height", 62: "min-height", 63: "min-width", 64: "max-height", 65: "max-width",
	58: "top", 59: "left", 60: "bottom", 61: "right",
	93: "border-width", 94: "border-top-width", 95: "border-left-width", 96: "border-bottom-width", 97: "border-right-width",
}

var enumProperties = map[int]struct {
	name   string
	values map[string]string
}{
	12:  {"font-style", map[string]string{"$350": "normal", "$382": "italic", "$381": "oblique"}},
	13:  {"font-weight", map[string]string{"$350": "normal", "$361": "bold", "$355": "100", "$356": "200", "$357": "300", "$358": "400", "$359": "500", "$360": "600", "$362": "800", "$363": "900"}},
	34:  {"text-align", map[string]string{"$59": "left", "$61": "right", "$320": "center", "$321": "justify"}},
	41:  {"text-transform", map[string]string{"$372": "uppercase", "$373": "lowercase", "$374": "capitalize", "$349": "none"}},
	44:  {"vertical-align", map[string]string{"$370": "super", "$371": "sub", "$350": "baseline", "$58": "top", "$60": "bottom", "$320": "middle"}},
	100: {"list-style-type", map[string]string{"$343": "decimal", "$796": "decimal-leading-zero", "$344": "lower-roman", "$345": "upper-roman", "$346": "lower-alpha", "$347": "upper-alpha", "$340": "disc", "$341": "square", "$342": "circle", "$349": "none"}},
	127: {"hyphens", map[string]string{"$383": "auto", "$384": "manual", "$349": "none"}},
	133: {"break-after", map[string]string{"$352": "page", "$383": "auto", "$353": "avoid"}},
	134: {"break-before", map[string]string{"$352": "page", "$383": "auto", "$353": "avoid"}},
	135: {"break-inside", map[string]string{"$383": "auto", "$353": "avoid"}},
	140: {"float", map[string]string{"$59": "left", "$61": "right"}},
	546: {"box-sizing", map[string]string{"$378": "border-box", "$377": "content-box"}},
	583: {"font-variant", map[string]string{"$349": "normal", "$369": "small-caps"}},
	706: {"text-orientation", map[string]string{"$383": "mixed", "$778": "sideways", "$779": "upright"}},
	707: {"text-combine-upright", map[string]string{"$573": "all"}},
	717: {"text-emphasis-style", map[string]string{
		"$724": "filled", "$725": "open",
		"$726": "filled dot", "$727": "open dot",
		"$728": "filled circle", "$729": "open circle",
		"$730": "filled double-circle", "$731": "open double-circle",
		"$732": "filled triangle", "$733": "open triangle",
		"$734": "filled sesame", "$735": "open sesame",
	}},
	762: {"ruby-position", map[string]string{"$58": "over", "$60": "under"}},
	763: {"ruby-position", map[string]string{"$61": "over", "$59": "under"}},
	765: {"ruby-align", map[string]string{"$320": "center", "$773": "space-around", "$774": "space-between", "$680": "start"}},
	766: {"ruby-align", map[string]string{"$320": "center", "$773": "space-around", "$774": "space-between", "$680": "start"}},
	788: {"break-after", map[string]string{"$352": "page", "$383": "auto", "$353": "avoid"}},
	789: {"break-before", map[string]string{"$352": "page", "$383": "auto", "$353": "avoid"}},
}

const maxStyleBytes = 64 << 10

// Named styles carry both CSS and structural properties. Resolve both before
// choosing XHTML tags or attributes; a style reference is not CSS inheritance.
type contentStyle struct {
	textStyle
	source                            []field
	render, listStart, link           string
	headingLevel, columnSpan, rowSpan int64
	layout                            string
	fixedWidth, fixedHeight           float64
	dropcapLines, dropcapChars        int64
	boxAlign, tableVerticalAlign      string
}

type textStyle struct {
	properties          []attribute
	direction, language string
	decorations         [3]string
	emphasisPosition    [2]string
	bytes               int
}

// KFX gives each line its own property, including an explicit "none".
// Keep them separate until projection so overlapping ranges can disable one
// decoration without losing the others.
func (s *textStyle) decorate(index int, pattern string) error {
	if pattern == "" {
		return nil
	}
	s.decorations[index] = pattern
	var lines []string
	lineStyle := "solid"
	for i, pattern := range s.decorations {
		if pattern != "" && pattern != "none" {
			lines = append(lines, [3]string{"underline", "line-through", "overline"}[i])
			lineStyle = pattern
		}
	}
	line := strings.Join(lines, " ")
	if line == "" {
		line = "none"
	}
	if err := s.set("text-decoration-line", line); err != nil {
		return err
	}
	return s.set("text-decoration-style", lineStyle)
}

// KFX stores each axis separately. A partial style range must retain the
// other axis when merged with an earlier range.
func (s *textStyle) positionEmphasis(axis int, position string) error {
	if position == "" {
		return nil
	}
	s.emphasisPosition[axis] = position
	resolved := s.emphasisPosition
	for i, fallback := range [2]string{"over", "right"} {
		if resolved[i] == "" {
			resolved[i] = fallback
		}
	}
	return s.set("text-emphasis-position", resolved[0]+" "+resolved[1])
}

func (s textStyle) get(name string) string {
	for _, property := range s.properties {
		if property.name == name {
			return property.value
		}
	}
	return ""
}

// Replacing an existing property keeps overlapping ranges from multiplying CSS.
func (s *textStyle) set(name, value string) error {
	if name == "" || value == "" {
		return nil
	}
	// Older Chromium and reading engines require the prefixed declaration.
	if strings.HasPrefix(name, "text-emphasis-") {
		if err := s.set("-webkit-"+name, value); err != nil {
			return err
		}
	}
	for i, property := range s.properties {
		if property.name == name {
			if i == len(s.properties)-1 && property.value == value {
				return nil
			}
			s.bytes -= len(name) + len(property.value) + 2
			s.properties = slices.Delete(s.properties, i, i+1)
			break
		}
	}
	n := len(name) + len(value) + 2
	if n > maxStyleBytes-s.bytes {
		return fmt.Errorf("%w: generated style is too large", ErrLimit)
	}
	s.bytes += n
	s.properties = append(s.properties, attribute{name, value})
	return nil
}

func (s *textStyle) merge(other textStyle) error {
	for _, property := range other.properties {
		if property.name == "text-decoration-line" || property.name == "text-decoration-style" ||
			property.name == "text-emphasis-position" || strings.HasPrefix(property.name, "-webkit-text-emphasis-") {
			continue
		}
		if err := s.set(property.name, property.value); err != nil {
			return err
		}
	}
	for i, pattern := range other.decorations {
		if err := s.decorate(i, pattern); err != nil {
			return err
		}
	}
	for i, position := range other.emphasisPosition {
		if err := s.positionEmphasis(i, position); err != nil {
			return err
		}
	}
	if other.direction != "" {
		s.direction = other.direction
	}
	if other.language != "" {
		s.language = other.language
	}
	return nil
}

func (s textStyle) css() string {
	var css strings.Builder
	css.Grow(s.bytes)
	for _, property := range s.properties {
		css.WriteString(property.name)
		css.WriteByte(':')
		css.WriteString(property.value)
		css.WriteByte(';')
	}
	return css.String()
}

func (c *contentReader) applyStyle(e *element, s textStyle) error {
	if err := c.claimGenerated(s.bytes + len(s.direction) + len(s.language)); err != nil {
		return err
	}
	e.attr("style", s.css())
	e.attr("dir", s.direction)
	e.attr("lang", s.language)
	return nil
}

func (c *contentReader) style(v value, depth int) (contentStyle, error) {
	if depth > 64 {
		return contentStyle{}, invalid("recursive style reference")
	}
	if err := c.book.ctx.Err(); err != nil {
		return contentStyle{}, err
	}
	var result contentStyle
	if name := v.get(fieldStyle).text; name != "" {
		inherited, ok := c.styles[name]
		if !ok {
			style, err := c.book.find(fragmentStyle, name)
			if err != nil {
				return contentStyle{}, err
			}
			inherited, err = c.style(style, depth+1)
			if err != nil {
				return contentStyle{}, err
			}
			if len(inherited.source) > maxContentNodes-c.styleFields {
				return contentStyle{}, fmt.Errorf("%w: too many inherited style properties", ErrLimit)
			}
			c.styleFields += len(inherited.source)
			c.styles[name] = inherited
		}
		result = inherited
		result.source = slices.Clone(inherited.source)
	}
	changed := false
	for _, f := range v.fields {
		id, _ := strconv.Atoi(strings.TrimPrefix(f.name, "$"))
		if !styleProperty(id) {
			continue
		}
		changed = true
		found := false
		for i := range result.source {
			if result.source[i].name == f.name {
				result.source[i] = f
				found = true
				break
			}
		}
		if !found {
			result.source = append(result.source, f)
		}
	}
	if !changed {
		result.properties = slices.Clone(result.properties)
		return result, nil
	}
	result = contentStyle{source: result.source}
	// Ion structs have no declaration order. Resolve KFX fields first, then
	// project in wire-ID order: side-specific properties follow shorthands.
	slices.SortFunc(result.source, func(a, b field) int {
		x, _ := strconv.Atoi(strings.TrimPrefix(a.name, "$"))
		y, _ := strconv.Atoi(strings.TrimPrefix(b.name, "$"))
		return x - y
	})
	var spacing [2]string
	for _, f := range result.source {
		id, _ := strconv.Atoi(strings.TrimPrefix(f.name, "$"))
		var name, text string
		if property := lengthProperties[id]; property != "" {
			name, text = property, cssLength(property, f.value)
		} else if property, ok := enumProperties[id]; ok {
			name, text = property.name, property.values[f.value.text]
		} else {
			switch id {
			case 23, 27, 554:
				pattern := map[string]string{"$349": "none", "$328": "solid", "$329": "double", "$330": "dashed", "$331": "dotted"}[f.value.text]
				if err := result.decorate(map[int]int{23: 0, 27: 1, 554: 2}[id], pattern); err != nil {
					return contentStyle{}, err
				}
			case 456, 457:
				spacing[id-456] = cssLength("border-spacing", f.value)
			case 719, 720:
				position := [2]map[string]string{{"$58": "over", "$60": "under"}, {"$59": "left", "$61": "right"}}[id-719][f.value.text]
				if err := result.positionEmphasis(id-719, position); err != nil {
					return contentStyle{}, err
				}
			case 125:
				result.dropcapLines = f.value.integer
			case 126:
				result.dropcapChars = f.value.integer
			case fieldLayout:
				result.layout = f.value.text
			case fieldFixedWidth:
				result.fixedWidth = f.value.numeric()
			case fieldFixedHeight:
				result.fixedHeight = f.value.numeric()
			case fieldRender:
				result.render = f.value.text
			case fieldBoxAlign:
				result.boxAlign = map[string]string{"$59": "left", "$61": "right", "$320": "center"}[f.value.text]
			case fieldTableVerticalAlign:
				result.tableVerticalAlign = map[string]string{"$58": "top", "$60": "bottom", "$320": "middle", "$350": "baseline"}[f.value.text]
			case fieldListStart:
				if f.value.kind == ionPositiveInt || f.value.kind == ionNegativeInt {
					result.listStart = f.value.id()
				}
			case fieldHeadingLevel:
				result.headingLevel = f.value.integer
			case fieldColumnSpan:
				result.columnSpan = f.value.integer
			case fieldRowSpan:
				result.rowSpan = f.value.integer
			case fieldLink:
				result.link = f.value.id()
			case fieldWritingMode:
				properties := value{fields: result.source}
				// Combined digits retain their vertical context in CSS; KFX
				// instead describes their internal horizontal layout explicitly.
				if f.value.text != "$557" || properties.get(707).text != "$573" {
					name, text = "writing-mode", map[string]string{"$557": "horizontal-tb", "$558": "vertical-lr", "$559": "vertical-rl"}[f.value.text]
				}
			case fieldLanguage:
				result.language = f.value.text
			case fieldDirection, fieldTextDirection:
				result.direction = map[string]string{symbolRTL: "rtl", symbolLTR: "ltr"}[f.value.text]
			case fieldFontFamily:
				family := f.value.text
				if len(family) > maxStyleBytes {
					return contentStyle{}, fmt.Errorf("%w: font family is too large", ErrLimit)
				}
				if family == "default" {
					family = "serif"
				}
				var names []string
				for _, name := range strings.Split(family, ",") {
					name = strings.TrimSpace(name)
					switch strings.ToLower(name) {
					case "serif", "sans-serif", "monospace", "cursive", "fantasy":
						names = append(names, strings.ToLower(name))
					default:
						names = append(names, cssString(name))
					}
				}
				if family != "" {
					name, text = "font-family", strings.Join(names, ",")
				}
			case 19, 21, 24, 28, 70, 83, 84, 85, 86, 87, 555, 718:
				name = map[int]string{19: "color", 21: "background-color", 24: "text-decoration-color", 28: "text-decoration-color", 70: "background-color", 83: "border-color", 84: "border-top-color", 85: "border-left-color", 86: "border-bottom-color", 87: "border-right-color", 555: "text-decoration-color", 718: "text-emphasis-color"}[id]
				text = cssColor(f.value)
				if text == "" {
					c.warning("A KFX color could not be decoded; kept the surrounding text style.")
				}
			case 88, 89, 90, 91, 92:
				name = map[int]string{88: "border-style", 89: "border-top-style", 90: "border-left-style", 91: "border-bottom-style", 92: "border-right-style"}[id]
				text = map[string]string{"$349": "none", "$328": "solid", "$329": "double", "$330": "dashed", "$331": "dotted", "$334": "groove", "$335": "ridge", "$336": "inset", "$337": "outset"}[f.value.text]
			case 45:
				if f.value.kind == ionBool {
					name, text = "white-space", "normal"
					if f.value.integer == 1 {
						text = "nowrap"
					}
				}
			}
		}
		if err := result.set(name, text); err != nil {
			return contentStyle{}, err
		}
	}
	if spacing[0] != "" || spacing[1] != "" {
		for i := range spacing {
			if spacing[i] == "" {
				spacing[i] = "0"
			}
		}
		if err := result.set("border-spacing", spacing[1]+" "+spacing[0]); err != nil {
			return contentStyle{}, err
		}
	}
	return result, nil
}

func styleProperty(id int) bool {
	if lengthProperties[id] != "" || enumProperties[id].name != "" || id >= 83 && id <= 92 {
		return true
	}
	switch id {
	case fieldRender, fieldListStart, fieldHeadingLevel, fieldColumnSpan, fieldRowSpan,
		fieldLayout, fieldFixedWidth, fieldFixedHeight, fieldBoxAlign, fieldTableVerticalAlign,
		fieldLink, fieldWritingMode, fieldLanguage, fieldDirection, fieldTextDirection,
		fieldFontFamily, fieldColor, 21, 23, 24, 27, 28, 70, 45, 125, 126, 456, 457, 554, 555, 718, 719, 720:
		return true
	}
	return false
}

func cssLength(property string, v value) string {
	if v.kind == ionSymbol && v.text == "$383" {
		switch property {
		case "line-height", "letter-spacing", "word-spacing":
			return "normal"
		case "width", "height", "margin", "margin-top", "margin-left", "margin-bottom", "margin-right":
			return "auto"
		}
	}
	unit := ""
	scale := 1.0
	if v.kind == ionStruct {
		unit = map[string]string{"$308": "em", "$309": "ex", "$310": "em", "$314": "%", "$315": "cm", "$316": "mm", "$317": "in", "$318": "pt", "$319": "px", "$505": "rem", "$506": "ch", "$311": "vw", "$312": "vh"}[v.get(fieldUnit).text]
		if unit == "" {
			return ""
		}
		// KFX's line-relative unit uses its default 1.2 line-height. EPUB 3's
		// portable CSS subset lacks lh units.
		if v.get(fieldUnit).text == "$310" {
			scale = 1.2
		}
		v = v.get(fieldValue)
	} else if property != "line-height" && v.numeric() != 0 {
		unit = "px"
	}
	if v.kind != ionPositiveInt && v.kind != ionNegativeInt && v.kind != ionFloat && v.kind != ionDecimal {
		return ""
	}
	number := v.numeric() * scale
	if math.IsNaN(number) || math.IsInf(number, 0) || math.Abs(number) > 1e6 {
		return ""
	}
	return strconv.FormatFloat(number, 'f', -1, 64) + unit
}

func cssColor(v value) string {
	if v.kind == ionStruct {
		v = v.get(fieldColor)
	}
	if v.kind != ionPositiveInt && v.kind != ionNegativeInt || v.integer < math.MinInt32 || v.integer > math.MaxUint32 {
		return ""
	}
	color := uint32(v.integer)
	return fmt.Sprintf("rgba(%d,%d,%d,%.3f)", color>>16&255, color>>8&255, color&255, float64(color>>24)/255)
}

func cssString(s string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' || r < 32 || r == '<' {
			fmt.Fprintf(&out, "\\%x ", r)
		} else {
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}
