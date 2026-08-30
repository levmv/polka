package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/koreader"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if assetID == "" {
		http.Error(w, "Missing asset ID", http.StatusBadRequest)
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}

	// Downloads are addressed by asset_id. Always resolve storage_path from
	// SQLite for this request: relayout can move files after a page renders, so a
	// cached path would race and break otherwise valid links.
	asset, err := s.assetFile(assetID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Asset not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}

	fullPath, err := s.managedRoot().Resolve(asset.StoragePath)
	if err != nil {
		serverError(w, err)
		return
	}

	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, "File not found on disk", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", format.MediaTypeForExtension(asset.Extension))
	w.Header().Set("Content-Disposition", fileContentDisposition("attachment", asset.Filename))

	// Import leaves this lazy identity empty; compute it on the first download.
	// Supported byte replacements (metadata write-back and duplicate restore)
	// maintain or clear the persisted value themselves, so subsequent downloads
	// should not re-read samples and open a redundant SQLite write transaction.
	if asset.KOReaderHash == "" {
		if hash, err := koreader.PartialMD5File(fullPath); err == nil {
			_ = db.SetAssetKOReaderHash(s.db, assetID, hash)
		}
	}

	http.ServeFile(w, r, fullPath)
}

func (s *Server) handleDownloadAs(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if assetID == "" {
		http.Error(w, "Missing asset ID", http.StatusBadRequest)
		return
	}
	target := converter.NormalizeTarget(r.PathValue("target"))
	if target == "" {
		http.Error(w, "Missing target format", http.StatusBadRequest)
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}

	asset, err := s.assetFile(assetID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Asset not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}

	fullPath, err := s.managedRoot().Resolve(asset.StoragePath)
	if err != nil {
		serverError(w, err)
		return
	}

	f, err := os.Open(fullPath)
	if os.IsNotExist(err) {
		http.Error(w, "File not found on disk", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		serverError(w, err)
		return
	}
	if !converter.CanConvert(asset.Format, target) {
		http.Error(w, fmt.Sprintf("Unsupported conversion from %s to %s", format.FormatLabel(asset.Format), target), http.StatusUnprocessableEntity)
		return
	}

	targetExt := converter.TargetExtension(target)
	if targetExt == "" {
		http.Error(w, "Unsupported target format", http.StatusUnprocessableEntity)
		return
	}
	contentType := converter.TargetMediaType(target)
	if contentType == "" {
		contentType = mime.TypeByExtension(targetExt)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	convertOpts, err := s.assetConversionOptions(asset)
	if err != nil {
		serverError(w, err)
		return
	}

	ready, convertedSize, cleanup, err := s.stageConvertedDownload(r.Context(), targetExt, func(dst *os.File) error {
		return converter.ConvertContextWithOptions(r.Context(), dst, f, asset.Format, info.Size(), target, convertOpts)
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, converter.ErrInputTooLarge) {
			http.Error(w, "Conversion input is too large", http.StatusRequestEntityTooLarge)
			return
		}
		if errors.Is(err, converter.ErrResourceLimit) {
			http.Error(w, "Conversion exceeds resource limits", http.StatusRequestEntityTooLarge)
			return
		}
		if errors.Is(err, format.ErrAZW4PDFNotFound) {
			http.Error(w, "Asset cannot be converted to "+string(target), http.StatusUnprocessableEntity)
			return
		}
		serverError(w, err)
		return
	}
	defer cleanup()
	if err := r.Context().Err(); err != nil {
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fileContentDisposition("attachment", convertedDownloadFilename(asset.Filename, target)))
	w.Header().Set("Content-Length", strconv.FormatInt(convertedSize, 10))
	if conversionDependsOnlyOnSource(asset.Format, target) {
		setVersionedConversionCacheControl(w, r, asset.CurrentSHA256)
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, ready)
}

func conversionDependsOnlyOnSource(from format.Format, target converter.Target) bool {
	return from == format.FormatEPUB && target == converter.TargetKEPUB ||
		(from == format.FormatCBR || from == format.FormatCB7) && target == converter.TargetCBZ
}

type assetFileRow struct {
	StoragePath   string
	WorkID        string
	Filename      string
	Extension     string
	Format        format.Format
	CanRead       bool
	CurrentSHA256 string
	KOReaderHash  string
	Title         string
	SortTitle     string
	Language      string
	Description   string
	Publisher     string
	Date          string
	Identifier    string
	Series        string
	SeriesIndex   float64
	Tags          string
}

func (s *Server) assetFile(assetID string) (assetFileRow, error) {
	var a assetFileRow
	var formatKey string
	var canRead int
	err := s.db.QueryRow(`
		SELECT a.storage_path, a.work_id, a.filename, a.extension, a.format, a.can_read,
		       COALESCE(a.current_sha256, ''),
		       COALESCE(a.koreader_hash, ''),
		       w.title, w.sort_title, COALESCE(w.language, ''),
		       COALESCE(w.description, ''), COALESCE(w.publisher, ''),
		       COALESCE(w.published_date, ''), COALESCE(w.identifiers, ''),
		       COALESCE(w.series, ''), COALESCE(w.series_index, 0),
		       COALESCE(w.tags, '')
		FROM assets a
		JOIN works w ON w.id = a.work_id
		WHERE a.id = ?
	`, assetID).Scan(
		&a.StoragePath, &a.WorkID, &a.Filename, &a.Extension, &formatKey, &canRead, &a.CurrentSHA256, &a.KOReaderHash,
		&a.Title, &a.SortTitle, &a.Language, &a.Description, &a.Publisher,
		&a.Date, &a.Identifier, &a.Series, &a.SeriesIndex, &a.Tags,
	)
	a.Format = format.FormatFromKey(formatKey)
	a.CanRead = canRead == 1
	return a, err
}

func (s *Server) assetConversionOptions(asset assetFileRow) (converter.ConversionOptions, error) {
	meta := asset.conversionMetadata()
	if asset.WorkID != "" {
		authorsByWork, err := db.AuthorsByWorkIDs(s.db, []string{asset.WorkID})
		if err != nil {
			return converter.ConversionOptions{}, err
		}
		for _, author := range authorsByWork[asset.WorkID] {
			name := strings.TrimSpace(author.Name)
			if name == "" {
				continue
			}
			meta.Authors = append(meta.Authors, bookmeta.AuthorMeta{
				Name:     name,
				SortName: strings.TrimSpace(author.SortName),
				Role:     strings.TrimSpace(author.Role),
			})
		}
	}
	return converter.ConversionOptions{Metadata: meta, SourceName: asset.Filename}, nil
}

func (a assetFileRow) conversionMetadata() *bookmeta.Metadata {
	return &bookmeta.Metadata{
		Title:       strings.TrimSpace(a.Title),
		SortTitle:   strings.TrimSpace(a.SortTitle),
		Language:    bookmeta.NormalizeLanguage(a.Language),
		Description: strings.TrimSpace(a.Description),
		Publisher:   strings.TrimSpace(a.Publisher),
		Date:        strings.TrimSpace(a.Date),
		Identifier:  strings.TrimSpace(a.Identifier),
		Series:      strings.TrimSpace(a.Series),
		SeriesIndex: a.SeriesIndex,
		Tags:        conversionTags(a.Tags),
	}
}

func conversionTags(raw string) []string {
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		key := strings.ToLower(tag)
		if seen[key] {
			continue
		}
		seen[key] = true
		tags = append(tags, tag)
	}
	return tags
}

func convertedDownloadFilename(filename string, target converter.Target) string {
	base := filename
	sourceExt := format.BookExtension(filename)
	if sourceExt != "" {
		base = filename[:len(filename)-len(sourceExt)]
	}
	if base == "" {
		base = "download"
	}
	if target == converter.TargetEPUB && strings.EqualFold(sourceExt, ".epub") {
		return base + ".repaired.epub"
	}
	if ext := converter.TargetExtension(target); ext != "" {
		return base + ext
	}
	return filename
}

func fileContentDisposition(disposition, filename string) string {
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disposition, asciiFilenameFallback(filename), encodeRFC5987(filename))
}

func asciiFilenameFallback(filename string) string {
	var sb strings.Builder
	for i := range len(filename) {
		b := filename[i]
		if b > 127 || b == '"' || b == '\\' || b < 32 || b == 127 {
			sb.WriteByte('_')
		} else {
			sb.WriteByte(b)
		}
	}
	return sb.String()
}

func encodeRFC5987(s string) string {
	var sb strings.Builder
	for i := range len(s) {
		b := s[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
			b == '!' || b == '#' || b == '$' || b == '&' || b == '+' || b == '-' ||
			b == '.' || b == '^' || b == '_' || b == '`' || b == '|' || b == '~' {
			sb.WriteByte(b)
		} else {
			sb.WriteString(fmt.Sprintf("%%%02X", b))
		}
	}
	return sb.String()
}
