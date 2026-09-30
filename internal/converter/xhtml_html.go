package converter

import (
	"fmt"
	"slices"

	"github.com/levmv/polka/internal/xmlutil"
	"golang.org/x/net/html"
)

func prepareEPUBXHTML(raw []byte) ([]byte, error) {
	return xmlutil.PrepareXHTMLForHTML(normalizeEPUBForeignPrefixes(raw, nil))
}

// The HTML tree's foreign-content namespaces are implicit. XML serialization
// needs declarations at namespace transitions, including HTML inside SVG.
func declareHTMLNamespaces(n *html.Node, inherited string) error {
	var walk func(*html.Node, string, int) error
	walk = func(n *html.Node, inherited string, depth int) error {
		if depth > 256 {
			return fmt.Errorf("content nesting exceeds limit: %w", ErrResourceLimit)
		}
		if n.Type == html.ElementNode {
			namespace := "http://www.w3.org/1999/xhtml"
			switch n.Namespace {
			case "svg":
				namespace = "http://www.w3.org/2000/svg"
			case "math":
				namespace = "http://www.w3.org/1998/Math/MathML"
			}
			declared := false
			for i := range n.Attr {
				if n.Attr[i].Namespace == "" && n.Attr[i].Key == "xmlns" {
					n.Attr[i].Val = namespace
					declared = true
				}
			}
			if !declared && namespace != inherited {
				n.Attr = append(n.Attr, html.Attribute{Key: "xmlns", Val: namespace})
			}
			for _, attr := range n.Attr {
				if attr.Namespace == "xlink" {
					n.Attr = slices.DeleteFunc(n.Attr, func(a html.Attribute) bool {
						return a.Key == "xmlns:xlink" || a.Namespace == "xmlns" && a.Key == "xlink"
					})
					n.Attr = append(n.Attr, html.Attribute{Key: "xmlns:xlink", Val: "http://www.w3.org/1999/xlink"})
					break
				}
			}
			inherited = namespace
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child, inherited, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(n, inherited, 0)
}
