package kfx

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/imagecodec"
)

func ExtractMetadata(r io.ReaderAt, size int64) (*bookmeta.Metadata, error) {
	b, err := openBook(context.Background(), r, size)
	if err != nil {
		return nil, err
	}
	meta, _, err := b.metadata()
	return meta, err
}

func (b *book) metadata() (*bookmeta.Metadata, string, error) {
	modern, err := b.singleton(fragmentMetadata)
	if err != nil {
		return nil, "", err
	}
	legacy, err := b.singleton(fragmentLegacyMetadata)
	if err != nil {
		return nil, "", err
	}
	values := make(map[string][]string)
	for _, category := range modern.get(fieldCategories).list {
		if category.get(fieldCategory).text != "kindle_title_metadata" {
			continue
		}
		for _, item := range category.get(fieldMetadataEntries).list {
			key, text := item.get(fieldMetadataKey).text, strings.TrimSpace(item.get(fieldValue).text)
			if text != "" {
				values[key] = append(values[key], text)
			}
		}
	}
	get := func(key string, id int) string {
		if list := values[key]; len(list) > 0 {
			return list[0]
		}
		return strings.TrimSpace(legacy.get(id).text)
	}
	meta := &bookmeta.Metadata{
		Title: get("title", 153), Language: bookmeta.NormalizeLanguage(get("language", 10)),
		Description: get("description", 154), Publisher: get("publisher", 232),
		Date: bookmeta.NormalizeMetadataDate(get("issue_date", 219)),
	}
	if asin := get("ASIN", 224); asin != "" {
		meta.Identifier = "asin:" + asin
	}
	authors := values["author"]
	if len(authors) == 0 {
		authors = bookmeta.ParseAuthorList(legacy.get(222).text)
	}
	seen := map[string]bool{}
	for _, author := range authors {
		if author == "" || seen[author] {
			continue
		}
		seen[author] = true
		name := author
		if last, first, ok := strings.Cut(author, ","); ok && strings.TrimSpace(first) != "" {
			name = strings.TrimSpace(first) + " " + strings.TrimSpace(last)
		}
		meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: name, SortName: bookmeta.AuthorSort(author)})
	}
	return meta, get("cover_image", 424), nil
}

func ExtractCover(r io.ReaderAt, size int64) ([]byte, string, error) {
	b, err := openBook(context.Background(), r, size)
	if err != nil {
		return nil, "", err
	}
	_, cover, err := b.metadata()
	if err != nil || cover == "" {
		return nil, "", err
	}
	resource, err := b.find(fragmentResource, cover)
	if err != nil {
		return nil, "", err
	}
	data, err := b.resourceBytes(resource, fragmentRawMedia)
	if err != nil {
		return nil, "", err
	}
	_, name, err := imagecodec.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", err
	}
	ext, ok := imagecodec.CoverExtension(name)
	if !ok {
		return nil, "", invalid("unsupported cover image format %s", name)
	}
	return data, ext, nil
}

func (b *book) resourceBytes(resource value, kind int) ([]byte, error) {
	if resource.get(fieldImageTiles).kind != ionPadding {
		return nil, invalid("tiled image resources are not supported")
	}
	location := resource.get(fieldResourceLocation).text
	f := b.fragments[fragmentKey{kind, location}]
	// Some producers place fonts among ordinary media. Prefer the font pool
	// when both kinds use the same location name.
	if f == nil && kind == fragmentRawFont {
		f = b.fragments[fragmentKey{fragmentRawMedia, location}]
	}
	if f != nil {
		return b.payload(f)
	}
	return nil, invalid("missing resource %s; supply the complete book bundle", location)
}
