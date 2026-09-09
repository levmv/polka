package web

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/pdfcover"
	"github.com/levmv/polka/internal/storage"
)

const (
	maxImportUploadBytes = 512 << 20 // 512 MiB
	// Keep ParseMultipartForm's default part limit when streaming uploads.
	maxImportUploadParts = 1000
)

type ImportUploadDTO struct {
	Status   string        `json:"status"`
	Book     BookDetailDTO `json:"book"`
	AssetID  int64         `json:"asset_id,omitzero"`
	Warnings []string      `json:"warnings,omitempty"`
}

func (s *Server) handleAPIImport(w http.ResponseWriter, r *http.Request) {
	source, ok := s.readImportUpload(w, r)
	if !ok {
		return
	}
	defer os.Remove(source.Path)

	renderer := pdfcover.NewRenderer()
	defer renderer.Close()

	root := s.managedRoot()
	catalogHasBooks, err := db.HasAnyAsset(s.db.Read(r.Context()))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := storage.RequireWritableRoot(root, catalogHasBooks); err != nil {
		serverError(w, r, err)
		return
	}
	template, err := storage.OpenBookPathTemplate(s.db.Read(r.Context()))
	if err != nil {
		serverError(w, r, err)
		return
	}
	releaseImport, err := s.acquireStorageWorkSlot(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer releaseImport()

	res, err := importer.Import(r.Context(), s.db, root, source, renderer, importer.Options{PathTemplate: template, CoverRoot: s.dataRoot()})
	if err != nil {
		http.Error(w, fmt.Sprintf("Import failed: %v", err), http.StatusBadRequest)
		return
	}

	status := string(res.Status)
	if res.Status == importer.StatusDuplicate && res.BookTrashed {
		// Upload is an explicit action on one book, unlike a recurring folder
		// sweep, so accepting the bytes while leaving the book hidden would read
		// as a failed upload.
		if restoreErr := db.RestoreBook(s.db.Write(r.Context()), res.BookID); restoreErr != nil && !errors.Is(restoreErr, sql.ErrNoRows) {
			serverError(w, r, restoreErr)
			return
		}
		status = "restored"
	}
	viewerIsAdmin := s.viewerIsAdmin(r)
	book, err := s.bookDetailDTO(r.Context(), db.FullVisibilityScope(), UserID(r.Context()), res.BookID, viewerIsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		serverError(w, r, errors.New("imported book not found"))
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}

	warnings := make([]string, 0, len(res.Warnings))
	for _, warning := range res.Warnings {
		warnings = append(warnings, warning.Error())
	}

	statusCode := http.StatusCreated
	if res.Status == importer.StatusDuplicate {
		statusCode = http.StatusOK
	}

	writeJSON(w, statusCode, ImportUploadDTO{
		Status:   status,
		Book:     book,
		AssetID:  res.AssetID,
		Warnings: warnings,
	})
}

// readImportUpload writes the HTTP error and removes the temporary file on
// failure. On success, the caller owns the file and must remove it after import.
func (s *Server) readImportUpload(w http.ResponseWriter, r *http.Request) (source importer.Source, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes)
	form, err := r.MultipartReader()
	if err != nil {
		writeMultipartError(w, err)
		return importer.Source{}, false
	}

	var tmpPath, originalName string
	// Read the entire form before importing: later fields can still exceed the
	// request limit, and a truncated upload must never reach managed storage.
	for parts := 0; ; parts++ {
		part, err := form.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeMultipartError(w, err)
			return importer.Source{}, false
		}
		if parts >= maxImportUploadParts {
			http.Error(w, "Too many multipart fields", http.StatusBadRequest)
			return importer.Source{}, false
		}
		if tmpPath != "" || part.FormName() != "book" || part.FileName() == "" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				writeMultipartError(w, err)
				return importer.Source{}, false
			}
			continue
		}

		originalName = cleanUploadFilename(part.FileName())
		if !importer.IsSupportedBook(originalName) {
			http.Error(w, "Unsupported book format", http.StatusBadRequest)
			return importer.Source{}, false
		}
		tmpDir := filepath.Join(s.dataDir, "tmp", "uploads")
		if err := os.MkdirAll(tmpDir, 0o755); err != nil {
			serverError(w, r, err)
			return importer.Source{}, false
		}
		tmp, err := os.CreateTemp(tmpDir, "book-*")
		if err != nil {
			serverError(w, r, err)
			return importer.Source{}, false
		}
		tmpPath = tmp.Name()
		defer func() {
			if !ok {
				os.Remove(tmpPath)
			}
		}()

		_, copyErr := io.Copy(tmp, part)
		closeErr := tmp.Close()
		if copyErr != nil {
			if _, ok := errors.AsType[*os.PathError](copyErr); ok {
				serverError(w, r, copyErr)
			} else {
				writeMultipartError(w, copyErr)
			}
			return importer.Source{}, false
		}
		if closeErr != nil {
			serverError(w, r, closeErr)
			return importer.Source{}, false
		}
	}
	if tmpPath == "" {
		http.Error(w, "Missing book field", http.StatusBadRequest)
		return importer.Source{}, false
	}
	return importer.Source{Path: tmpPath, OriginalName: originalName}, true
}

func cleanUploadFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, `/`)
	return filepath.Base(name)
}
