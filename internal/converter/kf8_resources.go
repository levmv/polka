package converter

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"path"
	"strings"
	"uuid"

	"github.com/levmv/polka/internal/css"
	"github.com/levmv/polka/internal/imagecodec"
)

func (w *kf8Writer) resource(base, href string) (string, error) {
	if name, fragment, ok := strings.Cut(href, "#"); ok && name != "" {
		ref, err := w.resource(base, name)
		if err == nil {
			ref += "#" + fragment
		}
		return ref, err
	}
	if err := checkContext(w.ctx); err != nil {
		return "", err
	}
	w.resourceCalls++
	if w.resourceCalls > 200000 {
		return "", fmt.Errorf("Kindle resource references exceed limit: %w", ErrResourceLimit)
	}
	if strings.HasPrefix(href, "data:") {
		if ref, ok := w.dataRefs[href]; ok {
			return ref, nil
		}
		if len(href) > int(maxConverterResourceBytes) {
			return "", ErrInputTooLarge
		}
		header, encoded, ok := strings.Cut(href, ",")
		if !ok {
			return "", fmt.Errorf("invalid data URI")
		}
		var data []byte
		var err error
		if strings.HasSuffix(header, ";base64") {
			data, err = base64.StdEncoding.DecodeString(encoded)
		} else {
			var value string
			value, err = url.PathUnescape(encoded)
			data = []byte(value)
		}
		if err != nil {
			return "", err
		}
		if err = claimConversionResources(w.ctx, 1, "Kindle data URI"); err != nil {
			return "", err
		}
		if err = claimConversionDecodedBytes(w.ctx, int64(len(data)), "Kindle data URI"); err != nil {
			return "", err
		}
		var ref string
		if svg, ok := imagecodec.PrepareSVG(data, "embedded.svg"); ok {
			index, reference := w.reserveFlow("image/svg+xml")
			w.dataRefs[href] = reference
			data, err = w.svg(base, svg)
			if err == nil {
				err = w.storeFlow(index, data)
			}
			if err != nil {
				delete(w.dataRefs, href)
				return "", err
			}
			ref = reference
		} else {
			ref, err = w.binaryResource(data)
		}
		if err == nil {
			w.dataRefs[href] = ref
		}
		return ref, err
	}
	file, _, external, err := w.reference(base, href)
	if err != nil {
		return "", err
	}
	if external || file == nil {
		return "", fmt.Errorf("resource is not packaged: %.200q: %w", href, ErrUnsupportedContent)
	}
	if ref, ok := w.resourceRefs[file]; ok {
		return ref, nil
	}
	data, err := w.recovery.read(w.ctx, file, maxConverterResourceBytes)
	if err != nil {
		return "", err
	}
	if font, ok := w.recovery.fontKeys[file]; ok {
		var key []byte
		count := 1040
		if font.algorithm == idpfFontObfuscation {
			sum := sha1.Sum([]byte(font.key))
			key = sum[:]
		} else {
			id, err := uuid.Parse(font.key)
			if err != nil {
				return "", err
			}
			key = id[:]
			count = 1024
		}
		data = bytes.Clone(data)
		for i := range min(len(data), count) {
			data[i] ^= key[i%len(key)]
		}
	}
	ext := strings.ToLower(path.Ext(file.Name))
	media := ""
	if w.mediaTypes[file] == "text/css" || w.mediaTypes[file] != "image/svg+xml" && ext == ".css" {
		media = "text/css"
	} else if svg, ok := w.prepareSVG(file, data); ok {
		media = "image/svg+xml"
		data = svg
	}
	if media != "" {
		index, ref := w.reserveFlow(media)
		w.resourceRefs[file] = ref
		if media == "text/css" {
			css, e := w.css(file.Name, data)
			data = []byte(css)
			err = e
		} else {
			data, err = w.svg(file.Name, data)
		}
		if err != nil {
			delete(w.resourceRefs, file)
			return "", err
		}
		if err := w.storeFlow(index, data); err != nil {
			return "", err
		}
		return ref, nil
	}
	if _, kind, err := imagecodec.DecodeConfig(bytes.NewReader(data)); err == nil && kind != "jpeg" && kind != "png" && kind != "gif" {
		id, err := w.images.image(base, href)
		if err != nil {
			return "", err
		}
		data = w.images.images[id]
	}
	ref, err := w.binaryResource(data)
	if err == nil {
		w.resourceRefs[file] = ref
	}
	return ref, err
}

func (w *kf8Writer) reserveFlow(media string) (int, string) {
	index := len(w.flows) + 1
	w.flows = append(w.flows, []byte("\n"))
	return index, "kindle:flow:" + kf8Base32(index, 4) + "?mime=" + media
}

func (w *kf8Writer) storeFlow(index int, data []byte) error {
	if len(data) == 0 {
		data = []byte("\n")
	}
	if err := claimConversionDecodedBytes(w.ctx, int64(len(data)), "Kindle resource flow"); err != nil {
		return err
	}
	w.flows[index-1] = data
	return nil
}

func (w *kf8Writer) binaryResource(data []byte) (string, error) {
	media := ""
	if bytes.HasPrefix(data, []byte{0, 1, 0, 0}) || bytes.HasPrefix(data, []byte("OTTO")) {
		var compressed bytes.Buffer
		z := zlib.NewWriter(&compressed)
		if _, err := z.Write(data); err != nil {
			return "", err
		}
		if err := z.Close(); err != nil {
			return "", err
		}
		font := make([]byte, 24)
		copy(font, "FONT")
		binary.BigEndian.PutUint32(font[4:], uint32(len(data)))
		binary.BigEndian.PutUint32(font[8:], 1)
		binary.BigEndian.PutUint32(font[12:], 24)
		data = append(font, compressed.Bytes()...)
	} else {
		_, kind, err := imagecodec.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("unsupported Kindle resource: %w", ErrUnsupportedContent)
		}
		media = "image/" + kind
		if kind != "jpeg" && kind != "png" && kind != "gif" {
			return "", ErrUnsupportedContent
		}
	}
	w.resources = append(w.resources, data)
	ref := "kindle:embed:" + kf8Base32(len(w.resources), 4)
	if media != "" {
		ref += "?mime=" + media
	}
	return ref, nil
}

func (w *kf8Writer) css(base string, data []byte) (string, error) {
	return css.Parse(string(data)).RewriteReferences(func(ref css.Reference) (string, error) {
		if ref.URL == "" || strings.HasPrefix(ref.URL, "#") {
			return ref.Raw(), nil
		}
		value, err := w.resource(base, ref.URL)
		if fatalConversionError(err) {
			return "", err
		}
		if err != nil {
			w.options.warn("Could not preserve CSS resource %.200q: %v", ref.URL, err)
		}
		if value == ref.URL {
			return ref.Raw(), nil
		}
		// Some Kindle readers require URL syntax for a string import.
		if ref.StringImport {
			return ref.AsURL(value), nil
		}
		return ref.WithURL(value), nil
	})
}
