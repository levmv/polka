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

const maxImportUploadBytes = 512 << 20 // 512 MiB

type ImportUploadDTO struct {
	Status   string        `json:"status"`
	Book     BookDetailDTO `json:"book"`
	AssetID  string        `json:"asset_id,omitempty"`
	Warnings []string      `json:"warnings,omitempty"`
}

func (s *Server) handleAPIImport(w http.ResponseWriter, r *http.Request) {
	if !parseLimitedMultipartForm(w, r, maxImportUploadBytes, 32<<20) {
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	file, header, err := r.FormFile("book")
	if err != nil {
		http.Error(w, "Missing book field", http.StatusBadRequest)
		return
	}
	defer file.Close()

	originalName := cleanUploadFilename(header.Filename)
	if originalName == "" {
		http.Error(w, "Missing filename", http.StatusBadRequest)
		return
	}
	if !importer.IsSupportedBook(originalName) {
		http.Error(w, "Unsupported book format", http.StatusBadRequest)
		return
	}

	tmpDir := filepath.Join(s.dataDir, "tmp", "uploads")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		serverError(w, err)
		return
	}
	tmp, err := os.CreateTemp(tmpDir, "book-*")
	if err != nil {
		serverError(w, err)
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		http.Error(w, "Failed to save upload", http.StatusBadRequest)
		return
	}
	if err := tmp.Close(); err != nil {
		serverError(w, err)
		return
	}

	renderer := pdfcover.NewRenderer()
	defer renderer.Close()

	root := s.managedRoot()
	catalogHasBooks, err := db.HasAnyAsset(s.db.DB)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := storage.RequireWritableRoot(root, catalogHasBooks); err != nil {
		serverError(w, err)
		return
	}
	template, err := storage.OpenBookPathTemplate(s.db.DB)
	if err != nil {
		serverError(w, err)
		return
	}
	releaseImport, err := s.acquireStorageWorkSlot(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	defer releaseImport()

	res, err := importer.Import(r.Context(), s.db, root, importer.Source{
		Path:         tmpPath,
		OriginalName: originalName,
	}, renderer, importer.Options{PathTemplate: template, CoverRoot: s.dataRoot()})
	if err != nil {
		http.Error(w, fmt.Sprintf("Import failed: %v", err), http.StatusBadRequest)
		return
	}

	status := string(res.Status)
	if res.Status == importer.StatusDuplicate && res.WorkTrashed {
		// Upload is an explicit action on one book, unlike a recurring folder
		// sweep, so accepting the bytes while leaving the book hidden would read
		// as a failed upload.
		if restoreErr := db.RestoreWork(s.db.DB, res.WorkID); restoreErr != nil && !errors.Is(restoreErr, sql.ErrNoRows) {
			serverError(w, restoreErr)
			return
		}
		status = "restored"
	}
	viewerIsAdmin := s.viewerIsAdmin(r)
	book, err := s.bookDetailDTO(db.FullVisibilityScope(), UserID(r.Context()), res.WorkID, viewerIsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		serverError(w, errors.New("imported book not found"))
		return
	} else if err != nil {
		serverError(w, err)
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

func cleanUploadFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, `/`)
	return filepath.Base(name)
}
