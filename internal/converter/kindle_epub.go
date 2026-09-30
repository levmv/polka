package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
	"golang.org/x/net/html"
)

// kindleEPUB retains the publication before either Kindle writer projects it
// into its output format. KF8 keeps the source markup, CSS and font resources.
type kindleEPUB struct {
	options                       ConversionOptions
	ctx                           context.Context
	archive                       *zip.Reader
	mediaTypes                    map[*zip.File]string
	documents                     []kindleDocument
	nav                           []kindleNavItem
	toc                           *html.Node
	start, contents               string
	meta                          epubMetadata
	opfPath, coverHref, direction string
	recovery                      *epubRecovery
	tokens                        int
}

type kindleDocument struct {
	name string
	root *html.Node
}

type kindleNavItem struct {
	Label    string
	Href     string
	Children []kindleNavItem
}

func readKindleEPUB(ctx context.Context, src io.ReaderAt, size int64, opts ConversionOptions) (*kindleEPUB, error) {
	s := &kindleEPUB{ctx: ctx, options: opts}
	if err := s.load(src, size); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *kindleEPUB) load(src io.ReaderAt, size int64) error {
	ctx, opts := s.ctx, s.options
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return fmt.Errorf("open EPUB: %w", err)
	}
	if err := claimConversionResources(ctx, len(zr.File), "EPUB archive"); err != nil {
		return err
	}
	pkg, err := kepubReadPackage(ctx, zr)
	if err != nil {
		return err
	}
	sourceOptions := opts
	sourceOptions.Metadata = nil
	var recovery *epubRecovery
	pkg.opfBytes, recovery, err = prepareEPUBConversion(ctx, zr, pkg.opfPath, pkg.opfBytes, false, sourceOptions)
	if err != nil {
		return err
	}
	zr.File = slices.DeleteFunc(zr.File, func(file *zip.File) bool { return recovery.omitted[file.Name] })
	var opf rebuildOPFDoc
	if err := bookmeta.DecodeOPFXML(pkg.opfBytes, &opf); err != nil {
		return fmt.Errorf("parse EPUB package: %w", err)
	}
	meta, err := bookmeta.ParseOPF(bytes.NewReader(pkg.opfBytes))
	if err != nil {
		opts.warn("Could not read EPUB metadata: %v", err)
		meta = &format.Metadata{}
	}
	s.archive, s.opfPath, s.recovery = zr, pkg.opfPath, recovery
	s.meta = epubMetadataForOutput(toEPUBMetadata(meta), opts)
	s.direction = opf.Spine.Direction

	if s.meta.Title == "" {
		s.meta.Title = "Untitled"
	}
	if s.meta.Language == "" {
		s.meta.Language = "und"
	}
	items := map[string]epubManifestItem{}
	s.mediaTypes = map[*zip.File]string{}
	coverID := ""
	for _, m := range opf.Metadata.Meta {
		if m.Name == "cover" {
			coverID = m.Content
		}
	}
	for _, item := range opf.Manifest.Items {
		if _, found := items[item.ID]; found {
			return fmt.Errorf("duplicate EPUB manifest id %q", item.ID)
		}
		items[item.ID] = item
		if file, _, external, err := s.reference(pkg.opfPath, item.Href); err == nil && !external && file != nil {
			s.mediaTypes[file] = strings.ToLower(strings.TrimSpace(item.MediaType))
		}
		if containsToken(item.Properties, "cover-image") {
			coverID = item.ID
		}
	}
	if item, ok := items[coverID]; ok {
		s.coverHref = item.Href
	} else if coverID != "" {
		opts.warn("EPUB cover item %q is missing", coverID)
	}

	seen := map[*zip.File]bool{}
	readDocument := func(item epubManifestItem) error {
		if !isEPUBContentDocument(item) {
			opts.warn("Kindle output needs HTML content; skipped %.200q (%s)", item.Href, item.MediaType)
			return nil
		}
		file, _, external, err := s.reference(pkg.opfPath, item.Href)
		if err != nil {
			return err
		}
		if external || file == nil {
			return fmt.Errorf("EPUB content %q is not packaged", item.Href)
		}
		if seen[file] {
			return nil
		}
		seen[file] = true
		raw, err := recovery.read(ctx, file, maxConverterDecodedInputBytes)
		if err != nil {
			return err
		}
		if recovery.omitted[file.Name] {
			return nil
		}
		originalSize := len(raw)
		raw, err = format.DecodeHTMLToUTF8(raw)
		if err != nil {
			return fmt.Errorf("decode EPUB content %s: %w", file.Name, err)
		}
		if err := claimConversionDecodedBytes(ctx, int64(len(raw)-originalSize), "decoded EPUB content"); err != nil {
			return err
		}
		prepared, err := prepareEPUBXHTML(raw)
		if err != nil {
			prepareErr := err
			prepared, err = recoverEPUBContent(raw)
			if err != nil {
				return fmt.Errorf("read EPUB content %s: %w", file.Name, err)
			}
			opts.warn("Could not preserve all formatting in %s: %v", file.Name, prepareErr)
		}
		raw = prepared
		z := html.NewTokenizer(bytes.NewReader(raw))
		for z.Next() != html.ErrorToken {
			s.tokens++
			if s.tokens > 500000 {
				return fmt.Errorf("EPUB content complexity exceeds limit: %w", ErrResourceLimit)
			}
			if s.tokens%1024 == 0 {
				if err := checkContext(ctx); err != nil {
					return err
				}
			}
		}
		root, err := html.Parse(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		if err := declareHTMLNamespaces(root, ""); err != nil {
			return err
		}
		doc := kindleDocument{name: file.Name, root: root}
		s.documents = append(s.documents, doc)
		if containsToken(item.Properties, "nav") {
			if err := s.readNavigation(doc); err != nil {
				return err
			}
		}
		return nil
	}
	addDocument := func(item epubManifestItem) error {
		if err := readDocument(item); err != nil {
			if fatalConversionError(err) {
				return err
			}
			opts.warn("Could not include EPUB document %.200q: %v", item.Href, err)
		}
		return nil
	}
	for _, ref := range opf.Spine.Items {
		item, ok := items[ref.IDRef]
		if !ok {
			opts.warn("EPUB spine item %.200q is missing", ref.IDRef)
			continue
		}
		if err := addDocument(item); err != nil {
			return err
		}
	}
	if len(s.documents) == 0 {
		opts.warn("EPUB reading order was unavailable; trying documents in manifest order")
		for _, item := range opf.Manifest.Items {
			if isEPUBContentDocument(item) {
				if err := addDocument(item); err != nil {
					return err
				}
			}
		}
	}
	if len(s.documents) == 0 {
		return fmt.Errorf("EPUB has no readable content: %w", ErrUnsupportedContent)
	}
	s.start = kindleKey(s.documents[0].name, "")
	// Non-spine documents can contain footnotes or other linked content. Keep
	// them after the declared reading order, with one copy of each actual entry.
	for _, item := range opf.Manifest.Items {
		if isEPUBContentDocument(item) {
			if err := addDocument(item); err != nil {
				return err
			}
		}
	}
	for _, ref := range opf.Guide.References {
		if ref.Type == "text" || ref.Type == "start" || ref.Type == "toc" {
			key, err := s.link(pkg.opfPath, ref.Href)
			if err != nil {
				opts.warn("Could not preserve %s guide reference: %v", ref.Type, err)
				continue
			}
			if ref.Type == "toc" {
				s.contents = key
			} else {
				s.start = key
			}
		}
	}
	if len(s.nav) == 0 && opf.Spine.TOC != "" {
		item, ok := items[opf.Spine.TOC]
		if ok {
			if err := s.readNCX(pkg.opfPath, item.Href); err != nil {
				if fatalConversionError(err) {
					return err
				}
				opts.warn("Could not preserve navigation: %v", err)
			}
		}
	}
	return nil
}

func kindleKey(name, fragment string) string { return name + "\x00" + fragment }

func (s *kindleEPUB) reference(base, href string) (*zip.File, string, bool, error) {
	ref, err := resolvePackageReference(base, href)
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid EPUB reference %q: %w", href, err)
	}
	if ref.external {
		return nil, "", true, nil
	}
	file, err := epubZipFile(s.archive, ref.path)
	return file, ref.fragment, false, err
}

func (s *kindleEPUB) link(base, href string) (string, error) {
	file, fragment, external, err := s.reference(base, href)
	if err != nil {
		return "", err
	}
	if external {
		return "", fmt.Errorf("Kindle navigation target is external: %q: %w", href, ErrUnsupportedContent)
	}
	if file == nil {
		return "", fmt.Errorf("EPUB link target is missing: %s → %s: %w", base, href, ErrUnsupportedContent)
	}
	return kindleKey(file.Name, fragment), nil
}

func walkKindleHTML(root *html.Node, visit func(*html.Node) error) error {
	var walk func(*html.Node, int) error
	walk = func(n *html.Node, depth int) error {
		if depth > 256 {
			return fmt.Errorf("EPUB content nesting exceeds limit: %w", ErrResourceLimit)
		}
		if err := visit(n); err != nil {
			return err
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root, 0)
}

func kindleNodeText(n *html.Node) string {
	var out strings.Builder
	_ = walkKindleHTML(n, func(n *html.Node) error {
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		return nil
	})
	return strings.Join(strings.Fields(out.String()), " ")
}

func (s *kindleEPUB) readNavigation(doc kindleDocument) error {
	return walkKindleHTML(doc.root, func(n *html.Node) error {
		if n.Type != html.ElementNode || n.Data != "nav" {
			return nil
		}
		if containsToken(attrValue(n, "epub:type"), "landmarks") {
			return walkKindleHTML(n, func(link *html.Node) error {
				if s.contents != "" || link.Type != html.ElementNode || link.Data != "a" || !containsToken(attrValue(link, "epub:type"), "toc") {
					return nil
				}
				key, err := s.link(doc.name, attrValue(link, "href"))
				if err != nil {
					s.options.warn("Could not preserve table of contents landmark: %v", err)
				} else {
					s.contents = key
				}
				return nil
			})
		}
		if len(s.nav) > 0 || !containsToken(attrValue(n, "epub:type"), "toc") {
			return nil
		}
		var readList func(*html.Node) ([]kindleNavItem, error)
		readList = func(root *html.Node) ([]kindleNavItem, error) {
			var items []kindleNavItem
			for child := root.FirstChild; child != nil; child = child.NextSibling {
				if child.Type != html.ElementNode {
					continue
				}
				if child.Data != "li" {
					if child.Data == "ol" || child.Data == "ul" {
						found, err := readList(child)
						if err != nil {
							return nil, err
						}
						items = append(items, found...)
					}
					continue
				}
				item := kindleNavItem{}
				for c := child.FirstChild; c != nil; c = c.NextSibling {
					if c.Type != html.ElementNode {
						continue
					}
					switch c.Data {
					case "a", "span":
						item.Label = kindleNodeText(c)
						if href := attrValue(c, "href"); href != "" {
							var err error
							item.Href, err = s.link(doc.name, href)
							if err != nil {
								s.options.warn("Could not preserve navigation link: %v", err)
							}
						}
					case "ol", "ul":
						var err error
						item.Children, err = readList(c)
						if err != nil {
							return nil, err
						}
					}
				}
				items = append(items, item)
			}
			return items, nil
		}
		var err error
		s.nav, err = readList(n)
		if len(s.nav) > 0 {
			s.toc = n
		}
		return err
	})
}

func (s *kindleEPUB) readNCX(base, href string) error {
	file, _, external, err := s.reference(base, href)
	if err != nil {
		return err
	}
	if external || file == nil {
		return fmt.Errorf("EPUB NCX is missing: %s", href)
	}
	raw, err := kepubReadZipFile(s.ctx, file, maxConverterPackageBytes)
	if err != nil {
		return err
	}
	type point struct {
		Label   string `xml:"navLabel>text"`
		Content struct {
			Src string `xml:"src,attr"`
		} `xml:"content"`
		Children []point `xml:"navPoint"`
	}
	var ncx struct {
		Points []point `xml:"navMap>navPoint"`
	}
	if err := xml.Unmarshal(raw, &ncx); err != nil {
		return fmt.Errorf("parse EPUB NCX: %w", err)
	}
	var convert func([]point, int) ([]kindleNavItem, error)
	convert = func(points []point, depth int) ([]kindleNavItem, error) {
		if depth > 64 {
			return nil, fmt.Errorf("NCX nesting exceeds limit: %w", ErrResourceLimit)
		}
		var out []kindleNavItem
		for _, p := range points {
			key, err := s.link(file.Name, p.Content.Src)
			if err != nil {
				s.options.warn("Could not preserve navigation link: %v", err)
			}
			children, err := convert(p.Children, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, kindleNavItem{Label: p.Label, Href: key, Children: children})
		}
		return out, nil
	}
	s.nav, err = convert(ncx.Points, 0)
	return err
}
