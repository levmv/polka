package djvu

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
)

type annotations struct {
	Fields []struct{ Key, Value string } `json:"metadata"`
	XMP    []struct{ Value string }      `json:"xmp"`
}

func (g guest) metadata(source io.ReaderAt, size int64) (result bookmeta.Metadata, err error) {
	// Release the scan even after an input/decoder error so rendering can proceed
	// on the same document. The cleanup guest does not inherit the operation error.
	cleanup := g
	defer cleanup.call("metadata_release")
	g.check(g.call("metadata_start"))
	for g.err == nil {
		status := g.call("metadata_step", 128)
		if status != 1 {
			g.check(status)
			break
		}
		ptr := g.call("metadata_range")
		g.check(g.call("last_status"))
		if ptr == 0 || g.err != nil {
			continue
		}
		if g.word(ptr) != math.MaxUint32 {
			return result, errors.New("DjVu metadata needs an external component")
		}
		offset, length := g.word(ptr+4), g.word(ptr+8)
		buffer := g.call("metadata_alloc")
		g.check(g.call("last_status"))
		g.read(source, offset, length, buffer)
		g.check(g.call("metadata_commit", uint64(size)))
	}
	ptr, length := g.call("metadata_ptr"), g.call("metadata_len")
	if err := g.error(); err != nil {
		return result, err
	}
	raw, ok := g.module.Memory().Read(ptr, length)
	if !ok {
		return result, errors.New("invalid DjVu metadata buffer")
	}
	var parsed annotations
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return result, err
	}
	return metadataFromAnnotations(parsed), nil
}

// ExtractMetadata releases its decoder after one file. Reuse a Decoder for batches.
func ExtractMetadata(ctx context.Context, source io.ReaderAt, size int64) (*bookmeta.Metadata, error) {
	decoder := NewDecoder()
	defer decoder.Close()
	result, err := decoder.Extract(ctx, source, size, Options{Metadata: true})
	return &result.Metadata, err
}

func metadataFromAnnotations(raw annotations) bookmeta.Metadata {
	meta := bookmeta.Metadata{}
	// Keep the first nonempty annotation value; use XMP for missing title/author.
	// DjVu's Creator commonly names scanner software, not a book author.
	for _, field := range raw.Fields {
		key := strings.ToLower(strings.TrimSpace(field.Key))
		value := cleanDJVUText(field.Value)
		if value == "" {
			continue
		}
		switch key {
		case "title", "booktitle":
			if meta.Title == "" {
				meta.Title = value
			}
		case "author":
			if len(meta.Authors) == 0 {
				for _, author := range bookmeta.ParseAuthorList(value) {
					meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{Name: author, SortName: bookmeta.AuthorSort(author)})
				}
			}
		case "publisher":
			if meta.Publisher == "" {
				meta.Publisher = value
			}
		case "description", "abstract", "summary":
			if meta.Description == "" {
				meta.Description = value
			}
		case "language", "lang":
			if meta.Language == "" {
				meta.Language = bookmeta.NormalizeLanguage(value)
			}
		case "date", "year":
			if meta.Date == "" {
				meta.Date = bookmeta.NormalizeMetadataDate(value)
			}
		case "subject", "keywords":
			meta.Tags = bookmeta.ApplyTagMode(meta.Tags, bookmeta.TagAdd, []string{strings.ReplaceAll(value, ";", ",")})
		case "isbn":
			if meta.Identifier == "" {
				if id := bookmeta.IdentifierFromOPF("isbn", value); id.Value != "" {
					meta.Identifier = bookmeta.FormatIdentifiers([]bookmeta.Identifier{id})
				}
			}
		}
	}
	for _, packet := range raw.XMP {
		fallback := bookmeta.ParseXMP([]byte(packet.Value))
		if fallback == nil {
			continue
		}
		if meta.Title == "" {
			meta.Title = cleanDJVUText(fallback.Title)
		}
		if len(meta.Authors) == 0 {
			meta.Authors = fallback.Authors
			for i := range meta.Authors {
				meta.Authors[i].Name = cleanDJVUText(meta.Authors[i].Name)
				meta.Authors[i].SortName = bookmeta.AuthorSort(meta.Authors[i].Name)
			}
		}
	}
	return meta
}

func cleanDJVUText(s string) string {
	return strings.Join(strings.Fields(strings.ToValidUTF8(strings.TrimSpace(s), "\uFFFD")), " ")
}
