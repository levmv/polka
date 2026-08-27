package web

import (
	"strings"

	"golang.org/x/net/html"
)

var allowedTags = map[string]bool{
	"p": true, "br": true, "strong": true, "b": true, "em": true, "i": true,
	"u": true, "ul": true, "ol": true, "li": true, "a": true, "blockquote": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "span": true,
}

var blockedTags = map[string]bool{
	"script": true, "style": true,
}

// SanitizeHTML sanitizes the given HTML string to return a SAFE HTML fragment
// suitable for web display. It enforces the web-output policy by stripping
// disallowed tags, inline styles, and unsafe hrefs.
func SanitizeHTML(input string) string {
	if input == "" {
		return ""
	}

	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		// html.Parse only errors on IO failure from the reader, which a
		// strings.Reader cannot produce; drop the description rather than risk
		// emitting unparsed markup.
		return ""
	}

	var buf strings.Builder
	var walk func(*html.Node)

	// to track if we are inside a blocked tag to drop its content
	blockedDepth := 0

	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && blockedTags[n.Data] {
			blockedDepth++
		}

		if blockedDepth == 0 {
			if n.Type == html.TextNode {
				buf.WriteString(html.EscapeString(n.Data))
			} else if n.Type == html.ElementNode {
				if allowedTags[n.Data] {
					buf.WriteString("<" + n.Data)
					if n.Data == "a" {
						for _, a := range n.Attr {
							if a.Key == "href" {
								val := strings.TrimSpace(a.Val)
								if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") || strings.HasPrefix(val, "mailto:") {
									buf.WriteString(` href="` + html.EscapeString(val) + `" rel="noopener noreferrer"`)
								}
							}
						}
					}
					buf.WriteString(">")
				}
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}

		if blockedDepth == 0 {
			if n.Type == html.ElementNode && allowedTags[n.Data] {
				// Avoid closing tags for void elements if html.Parse makes them. br is void.
				if n.Data != "br" {
					buf.WriteString("</" + n.Data + ">")
				}
			}
		}

		if n.Type == html.ElementNode && blockedTags[n.Data] {
			blockedDepth--
		}
	}

	for c := doc.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.Data == "html" {
			for c2 := c.FirstChild; c2 != nil; c2 = c2.NextSibling {
				if c2.Type == html.ElementNode && c2.Data == "body" {
					for c3 := c2.FirstChild; c3 != nil; c3 = c3.NextSibling {
						walk(c3)
					}
				}
			}
		}
	}

	return buf.String()
}
