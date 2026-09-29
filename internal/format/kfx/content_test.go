package kfx

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
)

type sourceSymbol string

func sourceValue(v any) value {
	switch v := v.(type) {
	case value:
		return v
	case sourceSymbol:
		return value{kind: ionSymbol, text: string(v)}
	case string:
		return value{kind: ionString, text: v}
	case int:
		kind := byte(ionPositiveInt)
		if v < 0 {
			kind = ionNegativeInt
		}
		return value{kind: kind, integer: int64(v)}
	case float64:
		return value{kind: ionFloat, number: v}
	case bool:
		n := int64(0)
		if v {
			n = 1
		}
		return value{kind: ionBool, integer: n}
	case []value:
		return value{kind: ionList, list: v}
	default:
		panic("unknown test value")
	}
}

func sourceStruct(kv ...any) value {
	v := value{kind: ionStruct}
	for i := 0; i < len(kv); i += 2 {
		v.fields = append(v.fields, field{"$" + strconv.Itoa(kv[i].(int)), sourceValue(kv[i+1])})
	}
	return v
}

func contentTestReader(t *testing.T) *contentReader {
	t.Helper()
	return newContentReader(&book{ctx: t.Context(), fragments: make(map[fragmentKey]*fragment)}, &Document{}, "", nil)
}

func addSourceFragment(c *contentReader, kind int, id string, v value) {
	f := &fragment{key: fragmentKey{kind, id}, value: v, loaded: true}
	c.book.fragments[f.key] = f
	c.book.ordered = append(c.book.ordered, f)
}

func TestKFXStyleProjection(t *testing.T) {
	for _, tc := range []struct {
		source value
		css    string
	}{
		{sourceStruct(100, sourceSymbol("$344")), "list-style-type:lower-roman;"},
		{sourceStruct(100, sourceSymbol("$796")), "list-style-type:decimal-leading-zero;"},
		{sourceStruct(88, sourceSymbol("$329")), "border-style:double;"},
		{sourceStruct(88, sourceSymbol("$336")), "border-style:inset;"},
		{sourceStruct(45, true), "white-space:nowrap;"},
		{sourceStruct(45, false), "white-space:normal;"},
		{sourceStruct(16, 24, 48, 20), "font-size:24px;margin-left:20px;"},
		{sourceStruct(42, 1.5, 48, 0), "line-height:1.5;margin-left:0;"},
		{sourceStruct(16, sourceStruct(307, 1.5, 306, sourceSymbol("$308"))), "font-size:1.5em;"},
		{sourceStruct(16, sourceStruct(307, "bad", 306, sourceSymbol("$319"))), ""},
		{sourceStruct(42, sourceSymbol("$383"), 46, 0, 48, sourceSymbol("$383")), "line-height:normal;margin:0;margin-left:auto;"},
		{sourceStruct(134, sourceSymbol("$352"), 135, sourceSymbol("$353")), "break-before:page;break-inside:avoid;"},
		{sourceStruct(19, int(0xffff0000)), "color:rgba(255,0,0,1.000);"},
		{sourceStruct(19, sourceStruct(19, int(0xffff0000))), "color:rgba(255,0,0,1.000);"},
		{sourceStruct(70, sourceStruct(19, int(0x800000ff))), "background-color:rgba(0,0,255,0.502);"},
		{sourceStruct(83, -65536), "border-color:rgba(255,0,0,1.000);"},
		{sourceStruct(19, false), ""},
		{sourceStruct(23, sourceSymbol("$328"), 27, sourceSymbol("$328"), 28, int(0xffa00000)), "text-decoration-line:underline line-through;text-decoration-style:solid;text-decoration-color:rgba(160,0,0,1.000);"},
		{sourceStruct(554, sourceSymbol("$329")), "text-decoration-line:overline;text-decoration-style:double;"},
		{sourceStruct(456, 4, 457, 8), "border-spacing:8px 4px;"},
	} {
		t.Run(tc.css, func(t *testing.T) {
			style, err := contentTestReader(t).style(tc.source, 0)
			if err != nil || style.css() != tc.css {
				t.Fatalf("style=%s error=%v", style.css(), err)
			}
		})
	}
	c := contentTestReader(t)
	addSourceFragment(c, 157, "base", sourceStruct(51, 0, 53, 1, 45, true))
	style, err := c.style(sourceStruct(157, "base", 51, 2, 45, false), 0)
	if err != nil || style.css() != "white-space:normal;padding:2px;padding-left:1px;" {
		t.Fatalf("inherited style=%s error=%v", style.css(), err)
	}
	if c.styles["base"].css() != "white-space:nowrap;padding:0;padding-left:1px;" {
		t.Fatal("mutated a shared style")
	}
	// Ion fields are unordered; a side-specific value overrides the general
	// padding even when it appeared earlier or came from the named style.
	for _, source := range []value{sourceStruct(53, 1, 51, 2, 45, false), sourceStruct(45, false, 51, 2, 53, 1)} {
		other, err := c.style(source, 0)
		if err != nil || other.css() != style.css() {
			t.Fatalf("field order changed style: %s error=%v", other.css(), err)
		}
	}
}

func TestKFXOverlappingDecorations(t *testing.T) {
	c := contentTestReader(t)
	addSourceFragment(c, fragmentStyle, "decorated", sourceStruct(23, sourceSymbol("$328"), 27, sourceSymbol("$328")))
	e, err := c.content(sourceStruct(159, sourceSymbol(symbolText), 145, "ABC", 142, []value{
		sourceStruct(143, 0, 144, 3, 157, "decorated"),
		sourceStruct(143, 1, 144, 1, 27, sourceSymbol("$349")),
	}), "section", 0)
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderBody(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`text-decoration-line:underline line-through;text-decoration-style:solid;">A</span>`,
		`text-decoration-line:underline;text-decoration-style:solid;">B</span>`,
		`text-decoration-line:underline line-through;text-decoration-style:solid;">C</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

func TestKFXOverlappingEmphasisPositions(t *testing.T) {
	c := contentTestReader(t)
	addSourceFragment(c, fragmentStyle, "marked", sourceStruct(717, sourceSymbol("$734"), 719, sourceSymbol("$60"), 720, sourceSymbol("$59")))
	e, err := c.content(sourceStruct(159, sourceSymbol(symbolText), 145, "日本語", 142, []value{
		sourceStruct(143, 0, 144, 3, 157, "marked"),
		sourceStruct(143, 1, 144, 1, 720, sourceSymbol("$61")),
	}), "section", 0)
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderBody(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`text-emphasis-position:under left;">日</span>`,
		`text-emphasis-position:under right;">本</span>`,
		`text-emphasis-position:under left;">語</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

func TestKFXRubyPreservesBasePositions(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(strconv.FormatBool(grouped), func(t *testing.T) {
			c := contentTestReader(t)
			addSourceFragment(c, fragmentRuby, "readings", sourceStruct(146, []value{
				sourceStruct(758, 1, 159, sourceSymbol(symbolText), 145, "にほん"),
				sourceStruct(758, 2, 159, sourceSymbol(symbolText), 145, "に"),
				sourceStruct(758, 3, 159, sourceSymbol(symbolText), 145, "ほん"),
			}))
			event := sourceStruct(143, 2, 144, 2, 757, "readings", 758, 1)
			if grouped {
				event = sourceStruct(143, 2, 144, 2, 757, "readings", 759, []value{
					sourceStruct(143, 0, 144, 1, 758, 2), sourceStruct(143, 1, 144, 1, 758, 3),
				})
			}
			e, err := c.content(sourceStruct(155, 100, 159, sourceSymbol(symbolText), 145, "😀 日本 end", 142, []value{
				event, sourceStruct(143, 3, 144, 1, 13, sourceSymbol("$361")),
			}), "section", 0)
			if err != nil {
				t.Fatal(err)
			}
			marker := c.anchor(position{"100", 5})
			if err := c.placeAnchors(e); err != nil {
				t.Fatal(err)
			}
			body, err := renderBody(t.Context(), e)
			if err != nil || !strings.Contains(body, `font-weight:bold;">本</span>`) || !strings.Contains(body, `id="`+marker+`"></span>end`) {
				t.Fatalf("ruby shifted emphasis or destination: %s, %v", body, err)
			}
			if !strings.Contains(body, "<ruby") || !strings.Contains(body, "<rt>") || !strings.Contains(body, "ほん</rt>") {
				t.Fatalf("lost annotation: %s", body)
			}
		})
	}
}

func TestKFXDropCapKeepsOverlappingEmphasis(t *testing.T) {
	c := contentTestReader(t)
	addSourceFragment(c, fragmentStyle, "initial", sourceStruct(125, 2, 126, 1))
	e, err := c.content(sourceStruct(159, sourceSymbol(symbolText), 157, "initial", 145, "Hello",
		142, []value{sourceStruct(143, 0, 144, 1, 13, sourceSymbol("$361"))}), "section", 0)
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderBody(t.Context(), e)
	if err != nil || !strings.Contains(body, `float:left;font-size:2em;line-height:1;margin:0 0.1em 0 0;font-weight:bold;">H</span>ello`) {
		t.Fatalf("drop cap or emphasis changed: %s, %v", body, err)
	}
}

func TestKFXFixedLayoutRejectsContentItCannotPreserve(t *testing.T) {
	for _, source := range []value{
		sourceStruct(159, sourceSymbol(symbolContainer), 156, sourceSymbol(symbolScaleFit), 66, 0, 67, 160),
		sourceStruct(159, sourceSymbol(symbolContainer), 156, sourceSymbol(symbolScaleFit), 66, 120, 67, 160,
			146, []value{sourceStruct(159, sourceSymbol(symbolText), 145, "Text overlay")}),
	} {
		c := contentTestReader(t)
		c.fixed = true
		if err := c.fixedTemplate(&element{tag: "div"}, source, "center", 0); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported fixed page succeeded: %v", err)
		}
	}
}

func TestKFXContentStyleInheritance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		style  value
		source value
		want   string
	}{
		{"inline text", sourceStruct(601, sourceSymbol("$283"), 13, sourceSymbol("$361")),
			sourceStruct(159, sourceSymbol("$269"), 146, []value{
				sourceValue("Before "), sourceStruct(159, sourceSymbol("$269"), 157, "derived", 145, "inline"), sourceValue(" after."),
			}), `<div>Before <span style="font-weight:bold;">inline</span> after.</div>`},
		{"ordered list", sourceStruct(100, sourceSymbol("$343"), 104, 5),
			sourceStruct(159, sourceSymbol("$276"), 157, "derived", 146, []value{sourceStruct(159, sourceSymbol("$277"), 145, "Item")}),
			`<ol start="5" style="list-style-type:decimal;"><li>Item</li></ol>`},
		{"local override", sourceStruct(100, sourceSymbol("$343"), 104, 5),
			sourceStruct(159, sourceSymbol("$276"), 157, "derived", 104, 8, 146, []value{sourceStruct(159, sourceSymbol("$277"), 145, "Item")}),
			`<ol start="8" style="list-style-type:decimal;"><li>Item</li></ol>`},
		{"heading", sourceStruct(790, 2), sourceStruct(159, sourceSymbol("$269"), 157, "derived", 145, "Heading"), `<h2>Heading</h2>`},
		{"table cell", sourceStruct(148, 2, 149, 3, 633, sourceSymbol("$58")),
			sourceStruct(159, sourceSymbol("$279"), 146, []value{sourceStruct(159, sourceSymbol("$270"), 157, "derived", 145, "Cell")}),
			`<tr><td style="vertical-align:top;" colspan="2" rowspan="3">Cell</td></tr>`},
		{"paragraph cell", sourceStruct(633, sourceSymbol("$58")),
			sourceStruct(159, sourceSymbol("$279"), 146, []value{sourceStruct(159, sourceSymbol("$269"), 157, "derived", 145, "Cell")}),
			`<tr><td style="vertical-align:top;"><p>Cell</p></td></tr>`},
		{"centered block with justified text", sourceStruct(580, sourceSymbol("$320"), 56, 200, 34, sourceSymbol("$321")),
			sourceStruct(159, sourceSymbol("$269"), 157, "derived", 145, "Text"),
			`<p style="text-align:justify;width:200px;margin-left:auto;margin-right:auto;">Text</p>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := contentTestReader(t)
			addSourceFragment(c, 157, "base", tc.style)
			addSourceFragment(c, 157, "derived", sourceStruct(157, "base"))
			e, err := c.content(tc.source, "section", 0)
			if err != nil {
				t.Fatal(err)
			}
			body, err := renderBody(t.Context(), e)
			if err != nil || body != tc.want {
				t.Fatalf("body=%s error=%v; want %s", body, err, tc.want)
			}
		})
	}
}

func TestKFXLinksPreserveStructure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source value
		want   string
	}{
		{"table", sourceStruct(159, sourceSymbol("$278"), 179, "outer", 146, []value{
			sourceStruct(159, sourceSymbol("$454"), 146, []value{
				sourceStruct(159, sourceSymbol("$279"), 146, []value{sourceStruct(159, sourceSymbol("$270"), 145, "Cell")}),
			}),
		}), `<table><tbody><tr><td><a href="https://example.com/outer">Cell</a></td></tr></tbody></table>`},
		{"list with own link", sourceStruct(159, sourceSymbol("$276"), 179, "outer", 146, []value{
			sourceStruct(159, sourceSymbol("$277"), 145, "One"),
			sourceStruct(159, sourceSymbol("$277"), 145, "Two", 179, "inner"),
		}), `<ul><li><a href="https://example.com/outer">One</a></li><li><a href="https://example.com/inner">Two</a></li></ul>`},
		{"centered image", sourceStruct(159, sourceSymbol("$271"), 175, "image", 580, sourceSymbol("$320"), 179, "outer"),
			`<div style="text-align:center;"><a href="https://example.com/outer"><img src="image.png" alt=""/></a></div>`},
		{"inline image", sourceStruct(159, sourceSymbol("$271"), 175, "image", 601, sourceSymbol("$283"), 580, sourceSymbol("$320"), 179, "outer"),
			`<a href="https://example.com/outer"><img src="image.png" alt=""/></a>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := contentTestReader(t)
			c.externalLinks["outer"] = "https://example.com/outer"
			c.externalLinks["inner"] = "https://example.com/inner"
			c.resources["image"] = 0
			c.doc.Resources = []Resource{{Href: "image.png"}}
			e, err := c.content(tc.source, "section", 0)
			if err != nil {
				t.Fatal(err)
			}
			body, err := renderBody(t.Context(), e)
			if err != nil || body != tc.want {
				t.Fatalf("body=%s error=%v; want %s", body, err, tc.want)
			}
		})
	}
}

func TestKFXUnknownColorKeepsText(t *testing.T) {
	c := contentTestReader(t)
	var warnings []string
	c.warn = func(message string) { warnings = append(warnings, message) }
	addSourceFragment(c, 157, "base", sourceStruct(19, int(0xff112233), 13, sourceSymbol("$361")))
	e, err := c.content(sourceStruct(159, sourceSymbol("$269"), 157, "base", 19, sourceStruct(70, false), 145, "Visible"), "div", 0)
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderBody(t.Context(), e)
	if err != nil || body != `<p style="font-weight:bold;">Visible</p>` || len(warnings) != 1 {
		t.Fatalf("body=%s warnings=%v error=%v", body, warnings, err)
	}
}

func TestKFXUnicodePositions(t *testing.T) {
	// KFX offsets count Unicode code points, not UTF-8 bytes, UTF-16 code
	// units, or grapheme clusters. Check both astral and combining characters.
	for _, tc := range []struct {
		text   string
		offset int
		before string
		after  string
	}{
		{"😀Bold", 1, "😀", "Bold"},
		{"e\u0301Bold", 2, "e\u0301", "Bold"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			c := contentTestReader(t)
			e, err := c.content(sourceStruct(155, 1, 159, sourceSymbol("$269"), 145, tc.text,
				142, []value{sourceStruct(143, tc.offset, 144, 4, 13, sourceSymbol("$361"))}), "section", 0)
			if err != nil {
				t.Fatal(err)
			}
			id := c.anchor(position{"1", tc.offset})
			if err := c.placeAnchors(e); err != nil {
				t.Fatal(err)
			}
			body, err := renderBody(t.Context(), e)
			if err != nil || !strings.Contains(body, `style="font-weight:bold;">`+tc.after+`</span>`) ||
				!strings.Contains(body, tc.before+`<span id="`+id+`"></span>`) {
				t.Fatalf("body=%s error=%v", body, err)
			}
		})
	}
}

func TestKFXInlineObjectPositions(t *testing.T) {
	c := contentTestReader(t)
	c.externalLinks["note"] = "https://example.com/note"
	e, err := c.content(sourceStruct(155, 1, 159, sourceSymbol("$269"), 146, []value{
		sourceValue("Before "),
		sourceStruct(155, 2, 159, sourceSymbol("$269"), 601, sourceSymbol("$283"), 145, "inline", 179, "note"),
		sourceValue(" after."),
	}, 142, []value{sourceStruct(143, 8, 144, 6, 13, sourceSymbol("$361"))}), "section", 0)
	if err != nil {
		t.Fatal(err)
	}
	after := c.anchor(position{"1", 8})
	inside := c.anchor(position{"2", 2})
	if err := c.placeAnchors(e); err != nil {
		t.Fatal(err)
	}
	body, err := renderBody(t.Context(), e)
	if err != nil || !strings.Contains(body, `style="font-weight:bold;"> after</span>`) ||
		!strings.Contains(body, `id="`+after+`"></span>`) ||
		!strings.Contains(body, `in<span id="`+inside+`"></span>line`) {
		t.Fatalf("body=%s error=%v", body, err)
	}
	if strings.Index(body, `id="`+after+`"`) < strings.Index(body, "line</") {
		t.Fatal("parent offset moved inside the inline object's own text")
	}
}

func TestKFXRulePositions(t *testing.T) {
	c := contentTestReader(t)
	e, err := c.content(sourceStruct(155, 1, 159, sourceSymbol("$269"), 146, []value{
		sourceValue("Before "), sourceStruct(155, 2, 159, sourceSymbol("$596")), sourceValue("After"),
	}, 142, []value{sourceStruct(143, 8, 144, 5, 13, sourceSymbol("$361"))}), "section", 0)
	if err != nil {
		t.Fatal(err)
	}
	after := c.anchor(position{"2", 1})
	if err := c.placeAnchors(e); err != nil {
		t.Fatal(err)
	}
	body, err := renderBody(t.Context(), e)
	if err != nil || !strings.Contains(body, `<hr/><span id="`+after+`"></span>`) ||
		!strings.Contains(body, `style="font-weight:bold;">After</span>`) || strings.Contains(body, "inline-block") {
		t.Fatalf("body=%s error=%v", body, err)
	}
}

func TestKFXOverlappingStylesStayBounded(t *testing.T) {
	c := contentTestReader(t)
	addSourceFragment(c, 157, "long", sourceStruct(11, strings.Repeat("F", 1024)))
	var events []value
	for range 1024 {
		events = append(events, sourceStruct(143, 0, 144, 1, 157, "long"))
	}
	paragraph := sourceStruct(159, sourceSymbol("$269"), 145, "X", 142, events)
	root := &element{tag: "div"}
	for range 80 {
		e, err := c.content(paragraph, "div", 0)
		if err != nil {
			t.Fatal(err)
		}
		root.children = append(root.children, e)
	}
	body, err := renderBody(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 100<<10 || strings.Count(body, "font-family:") != 80 {
		t.Fatal("overlapping styles multiplied the output")
	}

	// Reject aggregate expansion while constructing content, before allocating
	// or serializing the complete result. Individual source values fit the cap.
	c = contentTestReader(t)
	c.generatedLeft = 1024
	paragraph = sourceStruct(159, sourceSymbol("$269"), 145, strings.Repeat("x", 600))
	if _, err := c.content(paragraph, "div", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.content(paragraph, "div", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("expansion error=%v", err)
	}
}

func TestKFXRepeatedNavigationStaysBounded(t *testing.T) {
	c := contentTestReader(t)
	c.generatedLeft = 1024
	addSourceFragment(c, 393, "entry", sourceStruct(241, sourceStruct(244, strings.Repeat("&", 100))))
	ref := sourceValue(sourceSymbol("entry"))
	if _, err := c.navItems([]value{ref, ref}, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("navigation expansion error=%v", err)
	}
}

func TestKFXStoryLocationsAndWhitespace(t *testing.T) {
	c := contentTestReader(t)
	addSourceFragment(c, 259, "story", sourceStruct(155, 501, 146, []value{
		sourceStruct(155, 101, 159, sourceSymbol("$269"), 145, "😀 A\nB  C", 142, []value{sourceStruct(143, 4, 144, 1, 13, sourceSymbol("$361"))}),
		sourceStruct(155, 102, 159, sourceSymbol("$278"), 146, []value{
			sourceStruct(159, sourceSymbol("$279"), 146, []value{sourceStruct(159, sourceSymbol("$270"), 148, 2, 149, 3, 145, "Cell")}),
		}),
	}))
	root, err := c.content(sourceStruct(155, 1, 159, sourceSymbol("$269"), 176, "story"), "section", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []position{{"1", 0}, {"501", 0}, {"101", 4}, {"102", 0}} {
		c.doc.Navigation = append(c.doc.Navigation, NavItem{Label: p.id, Href: "#" + c.anchor(p)})
	}
	if err := c.placeAnchors(root); err != nil {
		t.Fatal(err)
	}
	body, err := renderBody(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if c.doc.Navigation[0].Href != c.doc.Navigation[1].Href {
		t.Fatal("story and container lost their shared destination")
	}
	marker := c.anchors[position{"101", 4}]
	for _, want := range []string{"<br/>", `id="` + marker + `"></span>`, "B", "\u00a0\u00a0C", `<table id="`, `<td colspan="2" rowspan="3">Cell</td>`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	if strings.Index(body, "<br/>") > strings.Index(body, `id="`+marker+`"`) {
		t.Fatal("anchor moved ahead of its source line break")
	}
}

func TestKFXAuxiliaryRecovery(t *testing.T) {
	c := contentTestReader(t)
	var warnings []string
	c.warn = func(message string) { warnings = append(warnings, message) }
	addSourceFragment(c, 266, "unused", sourceStruct(183, sourceStruct(143, 0)))
	addSourceFragment(c, 262, "font", sourceStruct(11, "Missing Font", 165, "font-data"))
	if err := c.navigation(); err != nil {
		t.Fatal(err)
	}
	if err := c.fonts(); err != nil {
		t.Fatal(err)
	}
	root, err := c.content(sourceStruct(159, sourceSymbol("$269"), 145, "Readable", 179, "broken"), "div", 0)
	if err != nil {
		t.Fatal(err)
	}
	c.doc.Pages = []NavItem{{Label: "missing", Href: "#" + c.anchor(position{"absent", 0})}}
	if err := c.placeAnchors(root); err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || len(c.doc.Pages) != 0 {
		t.Fatalf("warnings=%v pages=%v", warnings, c.doc.Pages)
	}
	body, err := renderBody(t.Context(), root)
	if err != nil || !strings.Contains(body, "Readable") {
		t.Fatalf("body=%s error=%v", body, err)
	}

	want := errors.New("storage unavailable")
	c.book.fragments[fragmentKey{418, "font-data"}] = &fragment{source: brokenReader{want}, size: 10}
	if err := c.fonts(); !errors.Is(err, want) {
		t.Fatalf("IO error=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c.book.ctx = ctx
	if err := c.fonts(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestKFXResourceKinds(t *testing.T) {
	c := contentTestReader(t)
	c.book.decoder = ionDecoder{ctx: t.Context(), remaining: 100}
	// Only signatures matter for resource lookup; the conversion fixture has
	// complete payloads. The font and image pools may reuse the same name.
	font := []byte("\x00\x01\x00\x00")
	png := []byte("\x89PNG\r\n\x1a\n")
	addRaw := func(kind int, name string, data []byte) {
		raw := append([]byte("ENTY\x01\x00\x0b\x00\x00\x00\xd0"), data...)
		c.book.fragments[fragmentKey{kind, name}] = &fragment{source: bytes.NewReader(raw), size: int64(len(raw))}
	}
	addRaw(fragmentRawFont, "shared", font)
	addRaw(fragmentRawMedia, "shared", png)
	addRaw(fragmentRawFont, "wrong", png)
	addRaw(fragmentRawMedia, "wrong", font)
	addSourceFragment(c, fragmentFont, "face", sourceStruct(11, "Test", 165, "shared"))
	addSourceFragment(c, fragmentFont, "wrong", sourceStruct(11, "Wrong", 165, "wrong"))
	addSourceFragment(c, fragmentResource, "font:face", sourceStruct(165, "shared"))
	addSourceFragment(c, fragmentResource, "wrong", sourceStruct(165, "wrong"))
	if err := c.fonts(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.resource("font:face"); err != nil {
		t.Fatal(err)
	}
	if len(c.doc.Resources) != 3 || c.doc.Resources[0].MediaType != "font/ttf" ||
		c.doc.Resources[2].MediaType != "image/png" || len(c.warned) != 1 {
		t.Fatalf("resources=%+v warnings=%v", c.doc.Resources, c.warned)
	}
	if _, err := c.resource("wrong"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("accepted a font as an image: %v", err)
	}
}
