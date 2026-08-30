package web

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/version"
)

type readerPageData struct {
	layoutPageData
	WorkID          string
	AssetID         string
	Title           string
	Extension       string
	TransportFormat string
	ReadURL         string
	FallbackURL     string
	IsPDF           bool
	// UsesFoliate is true for formats rendered in-browser by foliate-js (EPUB,
	// KEPUB, FB2, MOBI/KF8, CBZ, and CBR/CB7 normalized to CBZ). They share the
	// same reader stage, flow toggle, and state plumbing; only the format name
	// differs. PDF uses a separate fixed-layout surface and browser bundle so
	// other readers never load PDF.js.
	UsesFoliate bool
}

type readerPageAsset struct {
	WorkID        string
	AssetID       string
	Title         string
	Extension     string
	Format        format.Format
	CurrentSHA256 string
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	asset, err := db.PrimaryAssetForWork(s.db, scope, workID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Book or primary asset not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}

	reader := format.ReaderForFormat(asset.Format)
	if !asset.CanRead || reader == format.ReaderNone {
		http.Error(w, "Primary asset is not readable", http.StatusUnprocessableEntity)
		return
	}
	renderReaderPage(w, readerPageAsset{
		WorkID:        asset.WorkID,
		AssetID:       asset.ID,
		Title:         asset.Title,
		Extension:     asset.Extension,
		Format:        asset.Format,
		CurrentSHA256: asset.CurrentSHA256,
	})
}

func (s *Server) handleReadAssetPage(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
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
	if !asset.CanRead || format.ReaderForFormat(asset.Format) == format.ReaderNone {
		http.Error(w, "Asset is not readable", http.StatusUnprocessableEntity)
		return
	}
	renderReaderPage(w, readerPageAsset{
		WorkID:        asset.WorkID,
		AssetID:       assetID,
		Title:         asset.Title,
		Extension:     asset.Extension,
		Format:        asset.Format,
		CurrentSHA256: asset.CurrentSHA256,
	})
}

func renderReaderPage(w http.ResponseWriter, asset readerPageAsset) {
	reader := format.ReaderForFormat(asset.Format)
	ext := strings.ToLower(asset.Extension)
	data := readerPageData{
		layoutPageData:  newLayoutPageData(),
		WorkID:          asset.WorkID,
		AssetID:         asset.AssetID,
		Title:           asset.Title,
		Extension:       strings.TrimPrefix(strings.ToUpper(ext), "."),
		TransportFormat: readerTransportFormat(asset.Format),
		ReadURL:         readerAssetURL(asset.AssetID, asset.Format, asset.CurrentSHA256),
		FallbackURL:     readerFallbackURL(asset.AssetID, asset.Format, asset.CurrentSHA256),
		IsPDF:           reader == format.ReaderPDF,
		UsesFoliate:     reader == format.ReaderFoliate,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", readerContentSecurityPolicy)
	// The shell is cheap and carries the current content-versioned asset URLs.
	// Revalidate it so a cached page cannot keep pointing at an older book body.
	w.Header().Set("Cache-Control", "private, no-cache")
	renderPage(w, readerTmpl, "reader.html", data)
}

func readerFallbackURL(assetID string, kind format.Format, currentSHA256 string) string {
	if kind != format.FormatEPUB || !converter.CanConvert(kind, converter.TargetKEPUB) {
		return ""
	}
	return versionedURL("/download/"+assetID+"/as/kepub", conversionCacheVersion(currentSHA256))
}

func readerAssetURL(assetID string, kind format.Format, currentSHA256 string) string {
	version := assetCacheVersion(currentSHA256)
	if kind == format.FormatCBR || kind == format.FormatCB7 {
		version = conversionCacheVersion(currentSHA256)
	}
	return versionedURL("/read/assets/"+assetID, version)
}

const assetCacheVersionHexLength = 16

func assetCacheVersion(currentSHA256 string) string {
	if len(currentSHA256) < assetCacheVersionHexLength {
		return ""
	}
	return currentSHA256[:assetCacheVersionHexLength]
}

func versionedAssetURL(baseURL, currentSHA256 string) string {
	return versionedURL(baseURL, assetCacheVersion(currentSHA256))
}

func versionedURL(baseURL, version string) string {
	if version == "" {
		return baseURL
	}
	return baseURL + "?v=" + version
}

func conversionCacheVersion(currentSHA256 string) string {
	if len(currentSHA256) < assetCacheVersionHexLength || version.Version == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(currentSHA256 + "\x00" + version.Version))
	return hex.EncodeToString(sum[:assetCacheVersionHexLength/2])
}

func setVersionedAssetCacheControl(w http.ResponseWriter, r *http.Request, currentSHA256 string) {
	setCacheControlForVersion(w, r, assetCacheVersion(currentSHA256))
}

func setVersionedConversionCacheControl(w http.ResponseWriter, r *http.Request, currentSHA256 string) {
	setCacheControlForVersion(w, r, conversionCacheVersion(currentSHA256))
}

func setCacheControlForVersion(w http.ResponseWriter, r *http.Request, version string) {
	if version != "" && r.URL.Query().Get("v") == version {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
}

func readerTransportFormat(kind format.Format) string {
	if (kind == format.FormatCBR || kind == format.FormatCB7) && converter.CanConvert(kind, converter.TargetCBZ) {
		return format.FormatKey(format.FormatCBZ)
	}
	return format.FormatKey(kind)
}

const readerContentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' blob:; img-src 'self' data: blob:; " +
	"font-src 'self' data: blob:; connect-src 'self' blob:; frame-src 'self' blob:; " +
	"media-src 'self' blob:; worker-src 'self' blob:; object-src 'none'; " +
	"base-uri 'self'; form-action 'self'"

func (s *Server) handleReadAsset(w http.ResponseWriter, r *http.Request) {
	assetID := r.PathValue("id")
	if assetID == "" {
		http.Error(w, "Missing asset ID", http.StatusBadRequest)
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}

	// Reader files are still addressed by asset_id and resolved from SQLite for
	// this request. The reader page only carries the id, never a cached path.
	asset, err := s.assetFile(assetID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Asset not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	if !asset.CanRead || format.ReaderForFormat(asset.Format) == format.ReaderNone {
		http.Error(w, "Asset is not readable", http.StatusUnprocessableEntity)
		return
	}
	if asset.Format == format.FormatCBR || asset.Format == format.FormatCB7 {
		// Foliate reads ZIP comic archives. Keep the original archive asset as the
		// source of truth and normalize a bounded temporary CBZ for this read.
		setVersionedConversionCacheControl(w, r, asset.CurrentSHA256)
		http.Redirect(w, r, versionedURL("/download/"+assetID+"/as/cbz", conversionCacheVersion(asset.CurrentSHA256)), http.StatusTemporaryRedirect)
		return
	}

	fullPath, err := s.managedRoot().Resolve(asset.StoragePath)
	if err != nil {
		serverError(w, err)
		return
	}
	if asset.Format == format.FormatFB2 {
		s.serveFB2ReadAsset(w, r, asset, fullPath)
		return
	}
	if _, err := os.Stat(fullPath); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "File not found on disk", http.StatusNotFound)
			return
		}
		serverError(w, err)
		return
	}

	setVersionedAssetCacheControl(w, r, asset.CurrentSHA256)
	w.Header().Set("Content-Type", format.MediaTypeForExtension(asset.Extension))
	w.Header().Set("Content-Disposition", fileContentDisposition("inline", asset.Filename))
	http.ServeFile(w, r, fullPath)
}

func (s *Server) serveFB2ReadAsset(w http.ResponseWriter, r *http.Request, asset assetFileRow, fullPath string) {
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

	// Import detects FB2 containers from content, including archives stored with
	// a plain .fb2 suffix. Repeat that decision at request time so Foliate always
	// receives XML rather than ZIP or gzip bytes.
	source, err := format.OpenFB2Source(f, info.Size(), "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	defer source.Reader.Close()

	setVersionedAssetCacheControl(w, r, asset.CurrentSHA256)
	w.Header().Set("Content-Type", "application/x-fictionbook+xml")
	w.Header().Set("Content-Disposition", fileContentDisposition("inline", format.FB2PlainFilename(asset.Filename)))
	if source.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(source.ContentLength, 10))
	}
	_, _ = io.Copy(w, source.Reader)
}
