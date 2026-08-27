package converter

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// epubLinkProblem describes one internal reference in a generated EPUB that does
// not resolve to a packaged item or, where the reference carries a fragment, to
// an actual id in the target document.
type epubLinkProblem struct {
	// Location is the EPUB-root-relative path of the file the reference appears
	// in (for example "OEBPS/text.xhtml"), or "" for package-level references.
	Location string
	// Ref is the raw reference as emitted.
	Ref string
	// Reason is a short human-readable explanation.
	Reason string
}

func (p epubLinkProblem) String() string {
	switch {
	case p.Location == "" && p.Ref == "":
		return p.Reason
	case p.Location == "":
		return fmt.Sprintf("%s: %s", p.Ref, p.Reason)
	case p.Ref == "":
		return fmt.Sprintf("%s: %s", p.Location, p.Reason)
	default:
		return fmt.Sprintf("%s: %s: %s", p.Location, p.Ref, p.Reason)
	}
}

// epubRefAttrs maps element names to the attributes that carry an internal
// reference we can resolve against the package. Only elements our EPUB writers
// emit need to be covered, but the table is easy to extend.
var epubRefAttrs = map[string][]string{
	"a":      {"href"},
	"link":   {"href"},
	"img":    {"src"},
	"image":  {"href", "xlink:href"},
	"use":    {"href", "xlink:href"},
	"source": {"src"},
	"audio":  {"src"},
	"video":  {"src"},
	"iframe": {"src"},
	"object": {"data"},
	"embed":  {"src"},
}

var epubCSSURLRefRE = regexp.MustCompile(`(?i)url\(\s*(?:'([^']*)'|"([^"]*)"|([^)'"\s]*))\s*\)`)

// checkEPUBInternalLinks parses a generated EPUB and returns every internal
// reference that does not resolve. It resolves references through the OPF
// manifest and the actual packaged entries rather than assuming fixed file
// names, so it keeps describing broken output even when the writer changes paths
// (for example when text is split into multiple content documents). External
// references (http, mailto, data, …) are ignored; a Kindle-only scheme such as
// kindle:embed:* that survives into output is reported as unresolved.
func checkEPUBInternalLinks(epub []byte) ([]epubLinkProblem, error) {
	zr, err := zip.NewReader(bytes.NewReader(epub), int64(len(epub)))
	if err != nil {
		return nil, fmt.Errorf("read EPUB zip: %w", err)
	}
	files := map[string]*zip.File{}
	exists := map[string]bool{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(f.Name)
		files[name] = f
		exists[name] = true
	}

	opfPath, err := epubOPFPath(files)
	if err != nil {
		return nil, err
	}
	opfData, err := readZipEntry(files, opfPath)
	if err != nil {
		return nil, err
	}
	manifest, err := parseEPUBManifest(opfData)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", opfPath, err)
	}
	opfDir := path.Dir(opfPath)

	var problems []epubLinkProblem
	add := func(loc, ref, reason string) {
		problems = append(problems, epubLinkProblem{Location: loc, Ref: ref, Reason: reason})
	}

	// Validate manifest items and build the lookup structures used to resolve
	// references from content documents and stylesheets.
	manifestIDs := map[string]bool{}
	docPaths := map[string]bool{}
	cssPaths := map[string]bool{}
	for _, item := range manifest.Items {
		if strings.TrimSpace(item.ID) != "" {
			manifestIDs[item.ID] = true
		}
		if strings.TrimSpace(item.Href) == "" {
			add(opfPath, item.ID, "manifest item has no href")
			continue
		}
		target, _, ok, bad, reason := epubRefLocation(opfDir, item.Href)
		if bad {
			add(opfPath, item.Href, reason)
			continue
		}
		if !ok || target == "" {
			continue
		}
		if !exists[target] {
			add(opfPath, item.Href, "manifest item target is not packaged")
			continue
		}
		if epubItemIsXHTML(item.MediaType, target) {
			docPaths[target] = true
		}
		if epubItemIsCSS(item.MediaType, target) {
			cssPaths[target] = true
		}
	}

	if manifest.CoverID != "" && !manifestIDs[manifest.CoverID] {
		add(opfPath, manifest.CoverID, "cover metadata references an unknown manifest id")
	}
	for _, idref := range manifest.SpineIDs {
		if !manifestIDs[idref] {
			add(opfPath, idref, "spine references an unknown manifest id")
		}
	}

	// First pass: collect ids and references per content document so cross-document
	// fragments can be checked once every document's ids are known.
	docIDs := map[string]map[string]bool{}
	type docRef struct{ doc, ref string }
	var refs []docRef
	for docPath := range docPaths {
		data, err := readZipEntry(files, docPath)
		if err != nil {
			return nil, err
		}
		ids, docRefs := scanXHTMLIDsAndRefs(data)
		docIDs[docPath] = ids
		for _, ref := range docRefs {
			refs = append(refs, docRef{doc: docPath, ref: ref})
		}
	}

	// Second pass: resolve each content-document reference.
	for _, r := range refs {
		target, fragment, ok, bad, reason := epubRefLocation(path.Dir(r.doc), r.ref)
		if bad {
			add(r.doc, r.ref, reason)
			continue
		}
		if !ok {
			continue
		}
		if target == "" {
			// Same-document fragment.
			if fragment != "" && !docIDs[r.doc][fragment] {
				add(r.doc, r.ref, fmt.Sprintf("fragment #%s has no matching id", fragment))
			}
			continue
		}
		if !exists[target] {
			add(r.doc, r.ref, "target is not packaged")
			continue
		}
		if fragment != "" {
			if ids, isDoc := docIDs[target]; isDoc && !ids[fragment] {
				add(r.doc, r.ref, fmt.Sprintf("fragment #%s has no matching id in %s", fragment, target))
			}
		}
	}

	// Stylesheet url(...) references.
	for cssPath := range cssPaths {
		data, err := readZipEntry(files, cssPath)
		if err != nil {
			return nil, err
		}
		for _, ref := range extractCSSURLRefs(data) {
			target, _, ok, bad, reason := epubRefLocation(path.Dir(cssPath), ref)
			if bad {
				add(cssPath, ref, reason)
				continue
			}
			if !ok || target == "" {
				continue
			}
			if !exists[target] {
				add(cssPath, ref, "url() target is not packaged")
			}
		}
	}

	sort.Slice(problems, func(i, j int) bool { return problems[i].String() < problems[j].String() })
	return problems, nil
}

// epubRefLocation resolves a raw reference relative to baseDir within the EPUB.
// ok is false for empty or external references. bad is true when the reference
// is malformed or uses a scheme/host that cannot resolve to a packaged item,
// with reason set. A returned empty target with ok true means a same-document
// fragment (fragment is set).
func epubRefLocation(baseDir, raw string) (target, fragment string, ok, bad bool, reason string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false, false, ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false, true, "reference is not a valid URL"
	}
	if epubExternalScheme(u.Scheme) {
		return "", "", false, false, ""
	}
	if u.Scheme != "" || u.Opaque != "" || u.Host != "" {
		return "", "", false, true, "reference does not resolve to a packaged item"
	}
	if u.Path == "" {
		return "", u.Fragment, true, false, ""
	}
	return epubResolvePath(baseDir, u.Path), u.Fragment, true, false, ""
}

func epubResolvePath(baseDir, ref string) string {
	if after, ok := strings.CutPrefix(ref, "/"); ok {
		return path.Clean(after)
	}
	if baseDir == "" || baseDir == "." {
		return path.Clean(ref)
	}
	return path.Clean(path.Join(baseDir, ref))
}

func epubExternalScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "ftp", "ftps", "mailto", "tel", "data", "urn":
		return true
	}
	return false
}

func scanXHTMLIDsAndRefs(data []byte) (ids map[string]bool, refs []string) {
	ids = map[string]bool{}
	z := html.NewTokenizer(bytes.NewReader(data))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return ids, refs
		case html.StartTagToken, html.SelfClosingTagToken:
			token := z.Token()
			refAttrs := epubRefAttrs[strings.ToLower(token.Data)]
			for _, attr := range token.Attr {
				key := strings.ToLower(attr.Key)
				if key == "id" {
					if v := strings.TrimSpace(attr.Val); v != "" {
						ids[v] = true
					}
					continue
				}
				for _, want := range refAttrs {
					if key == want {
						refs = append(refs, attr.Val)
					}
				}
			}
		}
	}
}

func extractCSSURLRefs(css []byte) []string {
	var refs []string
	for _, match := range epubCSSURLRefRE.FindAllSubmatch(css, -1) {
		val := match[1]
		if len(val) == 0 {
			val = match[2]
		}
		if len(val) == 0 {
			val = match[3]
		}
		if len(val) > 0 {
			refs = append(refs, string(val))
		}
	}
	return refs
}

type epubManifestItem struct {
	ID        string
	Href      string
	MediaType string
}

type epubManifestInfo struct {
	Items    []epubManifestItem
	SpineIDs []string
	CoverID  string
}

func parseEPUBManifest(opf []byte) (epubManifestInfo, error) {
	var info epubManifestInfo
	dec := xml.NewDecoder(bytes.NewReader(opf))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "item":
			item := epubManifestItem{}
			for _, attr := range start.Attr {
				switch attr.Name.Local {
				case "id":
					item.ID = attr.Value
				case "href":
					item.Href = attr.Value
				case "media-type":
					item.MediaType = attr.Value
				}
			}
			info.Items = append(info.Items, item)
		case "itemref":
			for _, attr := range start.Attr {
				if attr.Name.Local == "idref" && strings.TrimSpace(attr.Value) != "" {
					info.SpineIDs = append(info.SpineIDs, attr.Value)
				}
			}
		case "meta":
			var name, content string
			for _, attr := range start.Attr {
				switch attr.Name.Local {
				case "name":
					name = attr.Value
				case "content":
					content = attr.Value
				}
			}
			if strings.EqualFold(name, "cover") && strings.TrimSpace(content) != "" {
				info.CoverID = content
			}
		}
	}
	return info, nil
}

func epubOPFPath(files map[string]*zip.File) (string, error) {
	data, err := readZipEntry(files, "META-INF/container.xml")
	if err != nil {
		return "", err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parse META-INF/container.xml: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "rootfile" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local == "full-path" && strings.TrimSpace(attr.Value) != "" {
				return path.Clean(attr.Value), nil
			}
		}
	}
	return "", fmt.Errorf("EPUB container.xml has no rootfile full-path")
}

func epubItemIsXHTML(mediaType, name string) bool {
	if strings.EqualFold(strings.TrimSpace(mediaType), "application/xhtml+xml") {
		return true
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".xhtml", ".html", ".htm":
		return true
	}
	return false
}

func epubItemIsCSS(mediaType, name string) bool {
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/css") || strings.EqualFold(path.Ext(name), ".css")
}

func readZipEntry(files map[string]*zip.File, name string) ([]byte, error) {
	f, ok := files[path.Clean(name)]
	if !ok {
		return nil, fmt.Errorf("EPUB missing %s", name)
	}
	return readZipFile(f)
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", f.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", f.Name, err)
	}
	return data, nil
}
