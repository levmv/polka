package converter

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/imagecodec"
)

// KF8 readers reconstruct CSS url() references inside SVG flows, but leave
// xml-stylesheet processing instructions unresolved. Keep their cascade order
// by putting the imports before the SVG's own styles and artwork.
func inlineSVGStylesheets(raw []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	var out, styles bytes.Buffer
	var copied int64
	for {
		start := decoder.InputOffset()
		token, err := decoder.RawToken()
		if err == io.EOF {
			return raw, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.ProcInst:
			if t.Target != "xml-stylesheet" {
				continue
			}
			var sheet struct {
				Href      string `xml:"href,attr"`
				Type      string `xml:"type,attr"`
				Media     string `xml:"media,attr"`
				Alternate string `xml:"alternate,attr"`
			}
			if err := xml.Unmarshal([]byte("<style "+string(t.Inst)+"/>"), &sheet); err != nil {
				return nil, err
			}
			if sheet.Href == "" || sheet.Type != "text/css" || sheet.Alternate == "yes" {
				continue
			}
			out.Write(raw[copied:start])
			copied = decoder.InputOffset()
			styles.WriteString(`<style xmlns="http://www.w3.org/2000/svg" type="text/css"`)
			if sheet.Media != "" {
				fmt.Fprintf(&styles, ` media="%s"`, html.EscapeString(sheet.Media))
			}
			fmt.Fprintf(&styles, `>@import url(%s);</style>`, html.EscapeString(strconv.Quote(sheet.Href)))
		case xml.StartElement:
			if styles.Len() == 0 {
				return raw, nil
			}
			out.Write(raw[copied:decoder.InputOffset()])
			out.Write(styles.Bytes())
			out.Write(raw[decoder.InputOffset():])
			return out.Bytes(), nil
		}
	}
}

// Embed referenced package resources to keep SVG filter images self-contained.
func (w *kf8Writer) embedSVGResources(base string, raw []byte, level int) ([]byte, error) {
	if level > 8 {
		return nil, fmt.Errorf("SVG resource nesting exceeds limit: %w", ErrResourceLimit)
	}
	return rewriteSVGResources(w.ctx, raw, func(element, href string) (string, error) {
		return w.svgDataURL(base, href, level)
	})
}

func (w *kf8Writer) svgDataURL(base, href string, level int) (string, error) {
	data, media, name, err := w.readSVGResource(base, href)
	if err != nil {
		return "", err
	}
	svgName := name
	if media == "image/svg+xml" {
		svgName = "image.svg"
	}
	if svg, ok := imagecodec.PrepareSVG(data, svgName); ok {
		data, err = w.embedSVGResources(name, svg, level+1)
		if err != nil {
			return "", err
		}
		media = "image/svg+xml"
	}
	prefix := "data:" + media + ";base64,"
	size := int64(len(prefix) + base64.StdEncoding.EncodedLen(len(data)))
	if size > maxConverterResourceBytes {
		return "", ErrInputTooLarge
	}
	if err := claimConversionDecodedBytes(w.ctx, size, "SVG embedded reference"); err != nil {
		return "", err
	}
	return prefix + base64.StdEncoding.EncodeToString(data), nil
}

func (w *kf8Writer) readSVGResource(base, href string) (data []byte, media, name string, err error) {
	if strings.HasPrefix(href, "data:") {
		header, encoded, ok := strings.Cut(href[5:], ",")
		if !ok {
			return nil, "", "", fmt.Errorf("invalid SVG data URI")
		}
		media, _, _ = strings.Cut(header, ";")
		if strings.HasSuffix(header, ";base64") {
			data, err = base64.StdEncoding.DecodeString(encoded)
		} else {
			var decoded string
			decoded, err = url.PathUnescape(encoded)
			data = []byte(decoded)
		}
		name = base
		if media == "image/svg+xml" {
			name += ".svg"
		}
		if err == nil {
			err = claimConversionDecodedBytes(w.ctx, int64(len(data)), "SVG embedded image")
		}
		return
	}
	file, _, external, err := w.reference(base, href)
	if err != nil {
		return nil, "", "", err
	}
	if external || file == nil {
		return nil, "", "", fmt.Errorf("SVG image is not packaged: %.200q: %w", href, ErrUnsupportedContent)
	}
	data, err = kepubReadZipFile(w.ctx, file, maxConverterResourceBytes)
	if err != nil {
		return nil, "", "", err
	}
	name = file.Name
	if svg, ok := w.prepareSVG(file, data); ok {
		return svg, "image/svg+xml", name, nil
	}
	_, kind, err := imagecodec.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", "", fmt.Errorf("decode SVG image: %w", err)
	}
	return data, "image/" + kind, name, nil
}

func (w *kf8Writer) svg(base string, data []byte) ([]byte, error) {
	var err error
	data, err = inlineSVGStylesheets(data)
	if err != nil {
		return nil, err
	}
	return rewriteSVGResources(w.ctx, data, func(element, href string) (string, error) {
		return w.svgReference(base, element, href)
	})
}

func (w *kf8Writer) svgReference(base, element, href string) (string, error) {
	var ref string
	var err error
	if element == "feImage" {
		ref, err = w.svgDataURL(base, href, 0)
	} else {
		ref, err = w.resource(base, href)
	}
	if fatalConversionError(err) {
		return "", err
	}
	if err != nil {
		w.options.warn("Could not preserve SVG resource %.200q in %.200s: %v", href, base, err)
	}
	return ref, nil
}
