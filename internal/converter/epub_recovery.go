package converter

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"path"
	"slices"
	"strings"
	"uuid"

	"golang.org/x/net/html"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/xmlutil"
)

const (
	idpfFontObfuscation  = "http://www.idpf.org/2008/embedding"
	adobeFontObfuscation = "http://ns.adobe.com/pdf/enc#RC"
)

type epubRecovery struct {
	options ConversionOptions
	omitted map[string]bool
	patches map[*zip.File][]byte
	fonts   map[*zip.File][]byte
}

func prepareEPUBConversion(ctx context.Context, zr *zip.Reader, opfPath string, raw []byte, preloadFonts bool, opts ConversionOptions) ([]byte, *epubRecovery, error) {
	r := &epubRecovery{
		options: opts,
		omitted: make(map[string]bool),
		patches: make(map[*zip.File][]byte),
		fonts:   make(map[*zip.File][]byte),
	}
	for _, file := range zr.File {
		if strings.EqualFold(path.Clean(file.Name), "META-INF/signatures.xml") {
			r.omit(file.Name, "Removed the source's digital signature from the converted copy")
		} else if err := validateRebuildEntryName(file.Name); err != nil {
			r.omit(file.Name, "%v", err)
		} else if file.Flags&1 != 0 {
			r.omit(file.Name, "EPUB entry %s is encrypted; omitted it", file.Name)
		}
	}
	next := opts.rewriteOPF(raw)
	var packageDoc rebuildOPFDoc
	if err := bookmeta.DecodeOPFXML(raw, &packageDoc); err != nil {
		return nil, nil, err
	}
	fonts := make(map[*zip.File]bool)
	for _, item := range packageDoc.Manifest.Items {
		if !isEPUBFontResource(item) {
			continue
		}
		name := cleanEPUBHref(opfPath, item.Href)
		file, err := epubZipFile(zr, name)
		if err != nil {
			return nil, nil, err
		}
		if file == nil {
			r.omit(name, "EPUB font %s is missing; omitted its references", name)
			continue
		}
		fonts[file] = true
		if !preloadFonts {
			continue
		}
		data, err := r.read(ctx, file, maxConverterResourceBytes)
		if err != nil {
			return nil, nil, err
		}
		if r.omitted[file.Name] {
			r.omitted[name] = true
		} else {
			r.fonts[file] = data
		}
	}
	file, err := epubZipFile(zr, "META-INF/encryption.xml")
	if err != nil {
		return nil, nil, err
	}
	if file != nil {
		oldKeys := epubFontKeys(raw)
		newKeys := oldKeys
		if !bytes.Equal(raw, next) {
			newKeys = epubFontKeys(next)
		}
		data, err := kepubReadZipFile(ctx, file, maxConverterMetadataBytes)
		if fatalConversionError(err) {
			return nil, nil, err
		}
		var entries []rebuildXMLNode
		parsed := err == nil && walkRebuildXML(data, func(node rebuildXMLNode) {
			if node.Name.Local == "EncryptedData" && node.Parent.Local == "encryption" {
				entries = append(entries, node)
			}
		})
		if !parsed {
			r.omit(file.Name, "Could not read EPUB encryption metadata; omitted embedded fonts and kept available content")
			for file := range fonts {
				r.omitted[file.Name] = true
			}
		} else {
			var edits []rebuildXMLEdit
			for _, node := range entries {
				var entry struct {
					Methods []struct {
						Algorithm string `xml:"Algorithm,attr"`
					} `xml:"EncryptionMethod"`
					Cipher struct {
						References []struct {
							URI string `xml:"URI,attr"`
						} `xml:"CipherReference"`
					} `xml:"CipherData"`
				}
				if err := xml.Unmarshal(data[node.Start:node.End], &entry); err != nil {
					return nil, nil, err
				}
				algorithm := ""
				if len(entry.Methods) == 1 {
					algorithm = entry.Methods[0].Algorithm
				}
				keep := len(entry.Cipher.References) == 1 && (algorithm == idpfFontObfuscation || algorithm == adobeFontObfuscation)
				for _, ref := range entry.Cipher.References {
					name := cleanEPUBHref("", ref.URI)
					resource, err := epubZipFile(zr, name)
					if err != nil {
						return nil, nil, err
					}
					if !fonts[resource] || r.omitted[name] || r.omitted[resource.Name] {
						keep = false
					}
				}
				oldKey, keyType := oldKeys[algorithm].Value, oldKeys[algorithm].Type
				newKey := newKeys[algorithm].Value
				if keep && oldKey != "" && oldKey != newKey && opts.Metadata != nil {
					meta := *opts.Metadata
					ids := bookmeta.ParseIdentifiers(meta.Identifier)
					ids = slices.DeleteFunc(ids, func(id bookmeta.Identifier) bool { return strings.EqualFold(id.Type, keyType) })
					meta.Identifier = bookmeta.FormatIdentifiers(ids)
					opts.Metadata = &meta
					next = opts.rewriteOPF(raw)
					newKeys = epubFontKeys(next)
					newKey = newKeys[algorithm].Value
					if oldKey == newKey {
						opts.warn("Kept source %s identifier to preserve embedded fonts", keyType)
					}
				}
				keep = keep && oldKey != "" && oldKey == newKey
				for _, ref := range entry.Cipher.References {
					name := cleanEPUBHref("", ref.URI)
					if !keep && name != "" {
						r.omit(name, "Could not preserve encrypted EPUB resource %s; omitted it", name)
					}
				}
				if !keep {
					edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.End})
					if len(entry.Cipher.References) == 0 {
						opts.warn("Ignored EPUB encryption entry without a resource reference")
					}
				}
			}
			if len(edits) == len(entries) {
				r.omitted[file.Name] = true
			} else if len(edits) != 0 {
				r.patches[file], _ = applyRebuildXMLEdits(data, edits)
			}
		}
	}
	for name := range r.omitted {
		if resource, _ := epubZipFile(zr, name); resource != nil {
			r.omitted[resource.Name] = true
		}
	}
	return next, r, nil
}

func epubFontKeys(raw []byte) map[string]bookmeta.Identifier {
	keys := make(map[string]bookmeta.Identifier)
	var doc struct {
		UniqueID string `xml:"unique-identifier,attr"`
		Metadata struct {
			IDs []struct {
				ID     string `xml:"id,attr"`
				Scheme string `xml:"scheme,attr"`
				Value  string `xml:",chardata"`
			} `xml:"identifier"`
		} `xml:"metadata"`
	}
	if bookmeta.DecodeOPFXML(raw, &doc) != nil {
		return keys
	}
	for _, id := range doc.Metadata.IDs {
		_, primaryFound := keys[idpfFontObfuscation]
		if !primaryFound && doc.UniqueID != "" && id.ID == doc.UniqueID {
			key := strings.Map(func(r rune) rune {
				if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
					return -1
				}
				return r
			}, id.Value)
			keys[idpfFontObfuscation] = bookmeta.Identifier{Type: bookmeta.IdentifierFromOPF(id.Scheme, id.Value).Type, Value: key}
		}
		if keys[adobeFontObfuscation].Value == "" {
			if id, err := uuid.Parse(strings.TrimSpace(id.Value)); err == nil {
				keys[adobeFontObfuscation] = bookmeta.Identifier{Type: "uuid", Value: id.String()}
			}
		}
	}
	return keys
}

func isEPUBFontResource(item epubManifestItem) bool {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	return strings.Contains(mediaType, "font") || mediaType == "application/vnd.ms-opentype" ||
		slices.Contains([]string{".ttf", ".otf", ".woff", ".woff2"}, strings.ToLower(path.Ext(item.Href)))
}

func (r *epubRecovery) omit(name, warning string, args ...any) {
	if !r.omitted[name] {
		r.omitted[name] = true
		r.options.warn(warning, args...)
	}
}

func (r *epubRecovery) read(ctx context.Context, file *zip.File, limit int64) ([]byte, error) {
	if r.omitted[file.Name] {
		return nil, nil
	}
	if data, ok := r.patches[file]; ok {
		return data, nil
	}
	if data, ok := r.fonts[file]; ok {
		return data, nil
	}
	data, err := kepubReadZipFile(ctx, file, limit)
	if fatalConversionError(err) {
		return nil, err
	}
	if err != nil {
		if len(data) != 0 && isEPUBContentDocument(epubManifestItem{Href: file.Name}) {
			if recovered, recoveryErr := recoverEPUBContent(data); recoveryErr == nil {
				r.options.warn("Recovered readable content from damaged EPUB document %s: %v", file.Name, err)
				r.patches[file] = recovered
				return recovered, nil
			}
		}
		r.omit(file.Name, "Could not read EPUB resource %s; omitted it: %v", file.Name, err)
		return nil, nil
	}
	return data, nil
}

func (r *epubRecovery) finishOPF(zr *zip.Reader, opfPath string, raw []byte) ([]byte, error) {
	if r.omitted[opfPath] {
		return nil, fmt.Errorf("EPUB package %q cannot be safely copied: %w", opfPath, ErrUnsupportedContent)
	}
	var doc rebuildOPFDoc
	if err := bookmeta.DecodeOPFXML(raw, &doc); err != nil {
		return nil, err
	}
	removedIDs := make(map[string]bool)
	available := make(map[string]bool)
	for _, item := range doc.Manifest.Items {
		name := cleanEPUBHref(opfPath, item.Href)
		file, err := epubZipFile(zr, name)
		if err != nil {
			return nil, err
		}
		if file == nil && isEPUBContentDocument(item) {
			r.omit(name, "EPUB document %s is missing; omitted it", name)
		}
		if r.omitted[name] || file != nil && r.omitted[file.Name] {
			removedIDs[item.ID] = true
		} else if file != nil {
			available[item.ID] = true
		}
	}
	readable := false
	for _, ref := range doc.Spine.Items {
		if available[ref.IDRef] {
			readable = true
		} else {
			removedIDs[ref.IDRef] = true
		}
	}
	var restoredSpine []byte
	if !readable {
		type itemRef struct {
			IDRef string `xml:"idref,attr"`
		}
		spine := struct {
			XMLName   xml.Name  `xml:"spine"`
			Namespace string    `xml:"xmlns,attr,omitempty"`
			TOC       string    `xml:"toc,attr,omitempty"`
			Direction string    `xml:"page-progression-direction,attr,omitempty"`
			Items     []itemRef `xml:"itemref"`
		}{Namespace: doc.XMLName.Space, Direction: doc.Spine.Direction}
		if available[doc.Spine.TOC] {
			spine.TOC = doc.Spine.TOC
		}
		for _, item := range doc.Manifest.Items {
			if available[item.ID] && isEPUBContentDocument(item) {
				spine.Items = append(spine.Items, itemRef{IDRef: item.ID})
			}
		}
		if len(spine.Items) == 0 {
			return nil, fmt.Errorf("EPUB has no readable content: %w", ErrUnsupportedContent)
		}
		var err error
		restoredSpine, err = xml.Marshal(spine)
		if err != nil {
			return nil, err
		}
		r.options.warn("EPUB reading order was unavailable; used remaining documents in manifest order")
	}
	if len(removedIDs) == 0 && restoredSpine == nil {
		return raw, nil
	}
	var edits []rebuildXMLEdit
	spineWritten := false
	walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Space != doc.XMLName.Space {
			return
		}
		if restoredSpine != nil {
			if node.Name.Local == "spine" && node.Parent.Local == "package" {
				value := restoredSpine
				if spineWritten {
					value = nil
				}
				edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.End, value: value})
				spineWritten = true
				return
			}
			if node.Name.Local == "package" && !spineWritten {
				edits = append(edits, rebuildXMLEdit{start: node.EndTagStart, end: node.EndTagStart, value: restoredSpine})
			}
		}
		remove := node.Name.Local == "item" && node.Parent.Local == "manifest" && removedIDs[rebuildXMLAttr(node.Attrs, "id")] ||
			restoredSpine == nil && node.Name.Local == "itemref" && node.Parent.Local == "spine" && removedIDs[rebuildXMLAttr(node.Attrs, "idref")] ||
			node.Name.Local == "meta" && node.Parent.Local == "metadata" && removedIDs[strings.TrimPrefix(rebuildXMLAttr(node.Attrs, "refines"), "#")]
		if remove {
			edits = append(edits, rebuildXMLEdit{start: node.Start, end: node.End})
		}
	})
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out, nil
}

func (r *epubRecovery) cleanFontCSS(base string, raw []byte) []byte {
	if len(r.omitted) == 0 || !bytes.Contains(bytes.ToLower(raw), []byte("@font-face")) {
		return raw
	}
	// Token boundaries keep comments, strings and unrelated CSS intact.
	css := string(raw)
	var edits []rebuildXMLEdit
	for pos := 0; pos < len(raw); {
		pos = mobi6CSSDelimiter(css, pos, "@")
		if pos == len(raw) {
			break
		}
		if len(raw)-pos < 10 || !strings.EqualFold(string(raw[pos:pos+10]), "@font-face") {
			pos++
			continue
		}
		if pos+10 < len(raw) && !strings.ContainsRune(" \t\r\n\f{/", rune(raw[pos+10])) {
			pos += 10
			continue
		}
		start := pos
		open := mobi6CSSDelimiter(css, pos+10, "{;")
		if open == len(raw) || raw[open] != '{' {
			pos += 10
			continue
		}
		end := mobi6CSSDelimiter(css, open+1, "}")
		if end == len(raw) {
			break
		}
		declarations := mobi6StripCSSComments(string(raw[open+1 : end]))
		body := ""
		for pos := 0; pos < len(declarations); {
			end := mobi6CSSDelimiter(declarations, pos, ";")
			key, value, found := strings.Cut(declarations[pos:end], ":")
			if found && strings.EqualFold(strings.TrimSpace(key), "src") {
				body += value + ";"
			}
			pos = end + 1
		}
		lowerBody := strings.ToLower(body)
		remove := false
		for offset := 0; offset < len(body); {
			i := strings.Index(lowerBody[offset:], "url(")
			if i < 0 {
				break
			}
			i += offset + 4
			close := mobi6CSSDelimiter(body, i, ")")
			if close == len(body) {
				break
			}
			href := strings.Trim(strings.TrimSpace(body[i:close]), "\"'")
			if r.omitted[cleanEPUBHref(base, href)] {
				remove = true
			}
			offset = close + 1
		}
		if remove {
			edits = append(edits, rebuildXMLEdit{start: start, end: end + 1})
		}
		pos = end + 1
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out
}

func (r *epubRecovery) cleanFontStyles(base string, raw []byte) []byte {
	if len(r.omitted) == 0 {
		return raw
	}
	var edits []rebuildXMLEdit
	if !walkRebuildXML(raw, func(node rebuildXMLNode) {
		if node.Name.Local != "style" || node.StartTagEnd >= node.EndTagStart {
			return
		}
		css := raw[node.StartTagEnd:node.EndTagStart]
		if cleaned := r.cleanFontCSS(base, css); !bytes.Equal(cleaned, css) {
			edits = append(edits, rebuildXMLEdit{start: node.StartTagEnd, end: node.EndTagStart, value: cleaned})
		}
	}) {
		return raw
	}
	out, _ := applyRebuildXMLEdits(raw, edits)
	return out
}

func recoverKEPUBContent(raw []byte) ([]byte, error) {
	data, err := recoverEPUBContent(raw)
	if err != nil || bytes.Equal(data, raw) {
		return data, err
	}
	return transformKEPUBContent(data)
}

func recoverEPUBContent(raw []byte) ([]byte, error) {
	if xmlutil.WalkXHTML(raw, nil) == nil {
		// Keep existing Kobo anchors, including partially marked documents.
		return raw, nil
	}
	raw, err := format.DecodeHTMLToUTF8(raw)
	if err != nil {
		return nil, err
	}
	// An unfinished CDATA section must not hide the remaining readable text.
	raw = bytes.ReplaceAll(raw, []byte("<![CDATA["), nil)
	raw = bytes.ReplaceAll(raw, []byte("]]>"), nil)
	doc, err := html.Parse(bytes.NewReader(xmlutil.StripXMLDeclaration(raw)))
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "head") {
			return
		}
		if n.Type == html.TextNode {
			text.WriteString(sanitizeKEPUBXMLString(n.Data))
		}
		if n.Type == html.ElementNode && n.Data == "img" {
			text.WriteString(htmlAttr(n, "alt"))
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
		if n.Type == html.ElementNode && slices.Contains([]string{"p", "div", "br", "li", "h1", "h2", "h3"}, n.Data) {
			text.WriteByte('\n')
		}
	}
	visit(doc)
	if strings.TrimSpace(text.String()) == "" {
		return nil, fmt.Errorf("document has no recoverable text")
	}
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(text.String())); err != nil {
		return nil, err
	}
	return []byte(`<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Recovered chapter</title></head><body><pre>` + escaped.String() + `</pre></body></html>`), nil
}
