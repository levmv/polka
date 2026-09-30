package css

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResourceReferences(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		urls         []string
	}{
		{"contexts", `@namespace svg url("http://www.w3.org/2000/svg");
@import /* shared */ "base.css" screen;
@supports (background:url(condition.png)) {
  .x { --art: image-set("one.png" 1x, url(two.png) 2x type("image/png"));
       fill:var(--paint, url(#paint)); content:"url(words.png)"; }
} /* url(comment.png) */`, []string{"base.css", "one.png", "two.png", "#paint"}},
		{"escapes", `@\69mport 'b\61se.css'; .x { a:u\72l(art\20 work.svg#paint); b:URL("a\\b.png"); }`, []string{"base.css", "art work.svg#paint", `a\b.png`}},
		{"token boundaries", `p {a:1url(fake); b:#url(fake); c:url(bad url(fake)); d:url(real.png); e:url("unfinished)}`, []string{"real.png", "unfinished)}"}},
		{"unknown values", `@unknown screen { a {new-property:unknown(url(a%2520b.svg#x)); --words:"unchanged"} }`, []string{"a%2520b.svg#x"}},
		{"statement boundaries", `<!-- @import "base.css"; --> --widget,p {fill:url(shape.svg)} @supports (fill:url(condition.svg)) {p {fill:url(next.svg)}}`, []string{"base.css", "shape.svg", "next.svg"}},
		{"custom values", `p {
  --label: @import "words";
  --art: @brand url(art.svg);
  \2d -template: {src:url(template.svg); @import "inside"} @import "after";
  background:future(@brand url(next.svg));
}
@supports (background:url(condition.svg)) {p {fill:url(paint.svg)}}`, []string{"art.svg", "template.svg", "next.svg", "paint.svg"}},
		{"implicit URL closure", `p {background:url(art.png`, []string{"art.png"}},
		{"implicit function closure", `p {background:url("art.png"`, []string{"art.png"}},
		{"implicit import closure", `@import "base.css`, []string{"base.css"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var urls []string
			got, err := Parse(tc.source).RewriteReferences(func(ref Reference) (string, error) {
				urls = append(urls, ref.URL)
				return ref.WithURL(ref.URL), nil
			})
			if err != nil || got != tc.source || !reflect.DeepEqual(urls, tc.urls) {
				t.Fatalf("URLs=%q, unchanged=%v, error=%v", urls, got == tc.source, err)
			}
		})
	}
	const source = `a{fill:u\72l(art.svg#p); mask: url('other.svg')} /* keep */`
	got, err := Parse(source).RewriteURLs(func(string) (string, error) { return "new art.svg#paint", nil })
	const want = `a{fill:u\72l("new art.svg#paint"); mask: url('new art.svg#paint')} /* keep */`
	if err != nil || got != want {
		t.Fatalf("rewrite = %q, %v", got, err)
	}
	for _, source := range []string{`url(a.png`, `url("a.png"`, `@import "a.css`} {
		got, err := Parse(source).RewriteURLs(func(string) (string, error) { return "new\x01 art.svg#paint", nil })
		var urls []string
		if err == nil {
			err = Parse(got).References(func(r Reference) error { urls = append(urls, r.URL); return nil })
		}
		if err != nil || !reflect.DeepEqual(urls, []string{"new\x01 art.svg#paint"}) {
			t.Errorf("rewrite %q: %q, %v", source, got, err)
		}
	}
}

func TestRulesDeclarationsAndEdits(t *testing.T) {
	const source = `/* header */ @f\6fnt-face { f\6fnt-family: Book; src:url(a.woff),local("A, B") }
p { color:red; --Case:{future:[a;b]}; content:"!important"; COLOR: blue !/**/\69mportant; color:green; future:{a:b} !important }`
	rules := Parse(source).Rules()
	if !rules.Next() || rules.Name != "font-face" || !rules.HasBlock {
		t.Fatal("font-face rule not recognized")
	}
	declarations := rules.Body.Declarations()
	if !declarations.Next() || declarations.Name != "font-family" || !declarations.Value.Keyword("book") {
		t.Fatal("escaped declaration not decoded")
	}
	if !rules.Next() || strings.TrimSpace(rules.Prelude.Raw()) != "p" {
		t.Fatal("style rule missing")
	}
	type declaration struct {
		name, value string
		important   bool
	}
	var got []declaration
	var edits []Edit
	declarations = rules.Body.Declarations()
	for declarations.Next() {
		d := declarations.Declaration
		got = append(got, declaration{d.Name, strings.TrimSpace(d.Value.Raw()), d.Important})
		if d.Name == "color" && d.Important {
			edits = append(edits, Edit{Span: d.Value.Span, Text: "purple "})
		}
	}
	want := []declaration{{"color", "red", false}, {"--Case", "{future:[a;b]}", false}, {"content", `"!important"`, false}, {"color", "blue", true}, {"color", "green", false}, {"future", "{a:b}", true}}
	if declarations.Err() != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("declarations = %#v, %v", got, declarations.Err())
	}
	if rules.Next() || rules.Err() != nil {
		t.Fatalf("unexpected rule: %v", rules.Err())
	}
	rewritten, err := Parse(source).Apply(edits)
	if err != nil || rewritten != strings.Replace(source, "blue ", "purple ", 1) {
		t.Fatalf("source bytes lost in edit: %q, %v", rewritten, err)
	}
}

func TestReferencesInDeclarationValues(t *testing.T) {
	p := Parse(`--art: @import "words" url(real.svg), { @import "inside"; fill:url(nested.svg) };`).Declarations()
	if !p.Next() {
		t.Fatal("missing declaration")
	}
	var urls []string
	items := p.Value.Split(',')
	for items.Next() {
		if err := items.Values.References(func(ref Reference) error { urls = append(urls, ref.URL); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if items.Err() != nil || !reflect.DeepEqual(urls, []string{"real.svg", "nested.svg"}) {
		t.Fatalf("value references = %q, %v", urls, items.Err())
	}
}

func TestRecoveryAndLimits(t *testing.T) {
	for _, source := range []string{
		`broken; color:red; no colon; display:block`,
		"content:\"broken\n; color:red; display:block",
		`background:url(bad url(fake)); color:red; display:block`,
		`@unknown {a:b} color:red; display:block`,
		`a:hover {font-weight:bold} color:red; display:block`,
		`a:{} color:red; display:block`,
	} {
		p := Parse(source).Declarations()
		var names []string
		for p.Next() {
			names = append(names, p.Name)
		}
		if p.Err() != nil || !reflect.DeepEqual(names, []string{"color", "display"}) {
			t.Errorf("recovery of %q: %v, %v", source, names, p.Err())
		}
	}
	p := Parse(`p {color:red`).Rules()
	if !p.Next() || !p.HasBlock || p.Body.Raw() != "color:red" {
		t.Fatal("lost rule with implicit EOF closure")
	}
	tooDeep := strings.Repeat("f(", maxDepth+1) + "url(a)" + strings.Repeat(")", maxDepth+1)
	if err := Parse(tooDeep).References(func(Reference) error { return nil }); !errors.Is(err, ErrLimit) {
		t.Fatalf("nesting limit = %v", err)
	}
}

func TestDecodedStrings(t *testing.T) {
	for _, tc := range []struct {
		source, text string
		closed       bool
	}{
		{`"a\\b"`, `a\b`, true},
		{`"a\20 b"`, "a b", true},
		{`"tail\`, "tail", false},
	} {
		c, ok := Parse(tc.source).Single()
		if !ok || c.Kind != String || c.Text() != tc.text || c.Closed != tc.closed {
			t.Errorf("decode %q: value=%q, closed=%v", tc.source, c.Text(), c.Closed)
		}
	}
}

func FuzzSourcePreservation(f *testing.F) {
	for _, source := range []string{`@import "x"; p{content:";}";a:url(x)}`, `--x:{a:[b];@import "data"} url(x);`, `url(bad url(x));`, "\\0 ", "\x00\xff", `p {a:hover{} color:red !important}`} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 128<<10 {
			t.Skip()
		}
		v := Parse(source)
		got, err := v.RewriteReferences(func(ref Reference) (string, error) { return ref.WithURL(ref.URL), nil })
		if err != nil && !errors.Is(err, ErrLimit) || err == nil && got != source {
			t.Fatalf("preservation failed: %q, %v", got, err)
		}
		count := 0
		const target = "rewritten art.svg#paint"
		got, err = v.RewriteReferences(func(ref Reference) (string, error) {
			count++
			return ref.WithURL(target), nil
		})
		if err == nil {
			remaining := count
			err = Parse(got).References(func(ref Reference) error {
				remaining--
				if ref.URL != target {
					t.Fatalf("rewritten reference = %q in %q", ref.URL, got)
				}
				return nil
			})
			if err != nil || remaining != 0 {
				t.Fatalf("rewritten references could not be read back: %q -> %q, %v", source, got, err)
			}
		}
		check := func(span Span) {
			if span.Start < 0 || span.End < span.Start || span.End > len(source) {
				t.Fatalf("invalid span: %v", span)
			}
		}
		rules := v.Rules()
		for rules.Next() {
			check(rules.Span)
			check(rules.Prelude.Span)
		}
		decls := v.Declarations()
		for decls.Next() {
			check(decls.Span)
			check(decls.Value.Span)
		}
	})
}
