package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/imagecodec"
	"github.com/levmv/polka/internal/xmlutil"
)

type mobi6Source struct {
	options   ConversionOptions
	ctx       context.Context
	archive   *zip.Reader
	documents []mobi6Document
	images    [][]byte
	imageIDs  map[*zip.File]int
	styles    map[*zip.File][]mobi6CSSRule
	nav       []mobi6NavItem
	toc       *html.Node
	start     string
	meta      epubMetadata
	cover     int
	tokens    int
}

type mobi6Document struct {
	name string
	root *html.Node
	css  []mobi6CSSRule
}

type mobi6NavItem struct {
	Label    string
	Href     string
	Children []mobi6NavItem
}

func convertEPUBToMOBI6(ctx context.Context, w io.Writer, src io.ReaderAt, size int64, opts ConversionOptions) error {
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
	s := &mobi6Source{ctx: ctx, options: opts, archive: zr, imageIDs: map[*zip.File]int{}, styles: map[*zip.File][]mobi6CSSRule{}, cover: -1, meta: epubMetadataForOutput(toEPUBMetadata(meta), opts)}
	if s.meta.Title == "" {
		s.meta.Title = "Untitled"
	}
	if s.meta.Language == "" {
		s.meta.Language = "und"
	}
	items := map[string]epubManifestItem{}
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
		if containsToken(item.Properties, "cover-image") {
			coverID = item.ID
		}
	}
	if coverID != "" {
		item, ok := items[coverID]
		if !ok {
			opts.warn("EPUB cover item %q is missing", coverID)
		} else {
			s.cover, err = s.image(pkg.opfPath, item.Href)
			if fatalConversionError(err) {
				return fmt.Errorf("convert cover: %w", err)
			}
			if err != nil {
				s.cover = -1
				opts.warn("Could not include the cover: %v", err)
			}
		}
	}
	seen := map[*zip.File]bool{}
	readDocument := func(item epubManifestItem) error {
		if !isEPUBContentDocument(item) {
			opts.warn("MOBI6 needs HTML content; skipped %.200q (%s)", item.Href, item.MediaType)
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
		prepared, err := xmlutil.PrepareXHTMLForHTML(raw)
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
				return fmt.Errorf("MOBI6 content complexity exceeds limit: %w", ErrResourceLimit)
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
		doc := mobi6Document{name: file.Name, root: root}
		if err := s.readDocumentStyles(&doc); err != nil {
			return err
		}
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
	s.start = mobi6Key(s.documents[0].name, "")
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
		if ref.Type == "text" || ref.Type == "start" {
			key, err := s.link(pkg.opfPath, ref.Href)
			if err != nil {
				opts.warn("Could not preserve reading start: %v", err)
				continue
			}
			s.start = key
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
	book, err := s.render()
	if err != nil {
		return err
	}
	return writeMOBI6(ctx, w, book)
}

func mobi6Key(name, fragment string) string { return name + "\x00" + fragment }

func (s *mobi6Source) reference(base, href string) (*zip.File, string, bool, error) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid EPUB reference %q: %w", href, err)
	}
	if u.Scheme != "" || u.Host != "" {
		return nil, "", true, nil
	}
	name := base
	if u.Path != "" {
		name = path.Join(path.Dir(base), u.Path)
		if strings.HasPrefix(u.Path, "/") {
			name = strings.TrimPrefix(path.Clean(u.Path), "/")
		}
	}
	if strings.HasPrefix(name, "../") || name == ".." {
		return nil, "", false, fmt.Errorf("EPUB reference leaves archive: %q", href)
	}
	file, err := epubZipFile(s.archive, name)
	return file, u.Fragment, false, err
}

func (s *mobi6Source) link(base, href string) (string, error) {
	file, fragment, external, err := s.reference(base, href)
	if err != nil {
		return "", err
	}
	if external {
		return "", fmt.Errorf("MOBI6 navigation target is external: %q: %w", href, ErrUnsupportedContent)
	}
	if file == nil {
		return "", fmt.Errorf("EPUB link target is missing: %s → %s: %w", base, href, ErrUnsupportedContent)
	}
	return mobi6Key(file.Name, fragment), nil
}

func (s *mobi6Source) image(base, href string) (int, error) {
	file, _, external, err := s.reference(base, href)
	if err != nil {
		return 0, err
	}
	if external || file == nil {
		return 0, fmt.Errorf("MOBI6 image is not packaged: %q: %w", href, ErrUnsupportedContent)
	}
	if id, ok := s.imageIDs[file]; ok {
		return id, nil
	}
	data, err := kepubReadZipFile(s.ctx, file, maxConverterResourceBytes)
	if err != nil {
		return 0, err
	}
	config, kind, err := imagecodec.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if strings.EqualFold(path.Ext(file.Name), ".svg") {
			return 0, fmt.Errorf("MOBI6 requires rasterization of SVG image %s: %w", file.Name, ErrUnsupportedContent)
		}
		return 0, fmt.Errorf("decode image %s: %w", file.Name, err)
	}
	const maxPixels = 16 << 20
	if config.Width < 1 || config.Height < 1 || config.Width > maxPixels/config.Height {
		return 0, fmt.Errorf("MOBI6 image dimensions exceed limit: %w", ErrInputTooLarge)
	}
	if err := claimConversionDecodedBytes(s.ctx, int64(config.Width)*int64(config.Height)*4, "MOBI6 decoded image"); err != nil {
		return 0, err
	}
	img, _, err := imagecodec.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("decode image %s: %w", file.Name, err)
	}
	if kind != "jpeg" {
		// Classic readers have the broadest support for baseline JPEG. Preserve
		// resolution and flatten transparency onto the normal white page.
		rgb := image.NewRGBA(image.Rect(0, 0, config.Width, config.Height))
		draw.Draw(rgb, rgb.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(rgb, rgb.Bounds(), img, img.Bounds().Min, draw.Over)
		var out bytes.Buffer
		if err := jpeg.Encode(&out, rgb, &jpeg.Options{Quality: 90}); err != nil {
			return 0, err
		}
		data = out.Bytes()
	}
	// Go's JPEG encoder omits APP0. Some Kindle renderers require JFIF even
	// for an otherwise valid baseline JPEG; adding it leaves image data intact.
	if !bytes.Contains(data[:min(len(data), 64)], []byte("JFIF\x00")) {
		jfif := []byte{0xff, 0xe0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
		withJFIF := append([]byte{0xff, 0xd8}, jfif...)
		data = append(withJFIF, data[2:]...)
	}
	id := len(s.images)
	s.images = append(s.images, data)
	s.imageIDs[file] = id
	return id, checkContext(s.ctx)
}

func mobi6Walk(root *html.Node, visit func(*html.Node) error) error {
	var walk func(*html.Node, int) error
	walk = func(n *html.Node, depth int) error {
		if depth > 256 {
			return fmt.Errorf("MOBI6 content nesting exceeds limit: %w", ErrResourceLimit)
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

func mobi6NodeText(n *html.Node) string {
	var out strings.Builder
	_ = mobi6Walk(n, func(n *html.Node) error {
		if n.Type == html.TextNode {
			out.WriteString(n.Data)
		}
		return nil
	})
	return strings.Join(strings.Fields(out.String()), " ")
}

func (s *mobi6Source) readNavigation(doc mobi6Document) error {
	return mobi6Walk(doc.root, func(n *html.Node) error {
		if len(s.nav) > 0 || n.Type != html.ElementNode || n.Data != "nav" || !containsToken(attrValue(n, "epub:type"), "toc") {
			return nil
		}
		var readList func(*html.Node) ([]mobi6NavItem, error)
		readList = func(root *html.Node) ([]mobi6NavItem, error) {
			var items []mobi6NavItem
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
				item := mobi6NavItem{}
				for c := child.FirstChild; c != nil; c = c.NextSibling {
					if c.Type != html.ElementNode {
						continue
					}
					switch c.Data {
					case "a", "span":
						item.Label = mobi6NodeText(c)
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

func (s *mobi6Source) readNCX(base, href string) error {
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
	var convert func([]point, int) ([]mobi6NavItem, error)
	convert = func(points []point, depth int) ([]mobi6NavItem, error) {
		if depth > 64 {
			return nil, fmt.Errorf("NCX nesting exceeds limit: %w", ErrResourceLimit)
		}
		var out []mobi6NavItem
		for _, p := range points {
			key, err := s.link(file.Name, p.Content.Src)
			if err != nil {
				s.options.warn("Could not preserve navigation link: %v", err)
			}
			children, err := convert(p.Children, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, mobi6NavItem{Label: p.Label, Href: key, Children: children})
		}
		return out, nil
	}
	s.nav, err = convert(ncx.Points, 0)
	return err
}
