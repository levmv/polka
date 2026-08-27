package web

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/pdfcover"
	"github.com/levmv/polka/internal/storage"
)

const maxFolderImportErrors = 8

type folderImportRequest struct {
	Path string `json:"path"`
}

type FolderImportPreviewDTO struct {
	Path         string   `json:"path"`
	Files        int      `json:"files"`
	CalibreBooks int      `json:"calibre_books"`
	WouldImport  int      `json:"would_import"`
	Duplicates   int      `json:"duplicates"`
	Trashed      int      `json:"trashed"` // Subset of Duplicates.
	Skipped      int      `json:"skipped"`
	Failed       int      `json:"failed"`
	Errors       []string `json:"errors,omitempty"`
}

type FolderImportResultDTO struct {
	Path         string          `json:"path"`
	Files        int             `json:"files"`
	CalibreBooks int             `json:"calibre_books"`
	Imported     int             `json:"imported"`
	Duplicates   int             `json:"duplicates"`
	Trashed      int             `json:"trashed"` // Subset of Duplicates.
	Restored     int             `json:"restored"`
	Skipped      int             `json:"skipped"`
	Failed       int             `json:"failed"`
	Warnings     int             `json:"warnings"`
	Errors       []string        `json:"errors,omitempty"`
	Storage      AdminStorageDTO `json:"storage"`
}

func (s *Server) handleAPIAdminStorageImportPreview(w http.ResponseWriter, r *http.Request) {
	var req folderImportRequest
	if !readJSON(w, r, &req) {
		return
	}
	path, err := s.validateFolderImportPath(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	preview, err := s.previewFolderImport(r.Context(), path)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleAPIAdminStorageImportRun(w http.ResponseWriter, r *http.Request) {
	var req folderImportRequest
	if !readJSON(w, r, &req) {
		return
	}
	path, err := s.validateFolderImportPath(req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

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

	renderer := pdfcover.NewRenderer()
	defer renderer.Close()
	result, err := s.importFolder(r.Context(), path, root, renderer, importer.Options{
		PathTemplate: template,
		CoverRoot:    s.dataRoot(),
	})
	if err != nil {
		serverError(w, err)
		return
	}
	status, err := s.adminStorageStatus()
	if err != nil {
		serverError(w, err)
		return
	}
	result.Storage = status
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) validateFolderImportPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("Folder path is required")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("Folder path must be absolute")
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve folder path: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat folder: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("Folder path must be a directory")
	}

	sourcePath, err := realPath(abs)
	if err != nil {
		return "", fmt.Errorf("resolve folder symlinks: %w", err)
	}
	for _, reserved := range []struct {
		name string
		path string
	}{
		{name: "data dir", path: s.dataDir},
		{name: "books folder", path: s.managedRoot().Path},
	} {
		reservedPath, err := realPathIfPossible(reserved.path)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", reserved.name, err)
		}
		if pathsOverlap(sourcePath, reservedPath) {
			return "", fmt.Errorf("Folder must be outside the %s", reserved.name)
		}
	}
	return abs, nil
}

func (s *Server) previewFolderImport(ctx context.Context, rootPath string) (FolderImportPreviewDTO, error) {
	out := FolderImportPreviewDTO{Path: rootPath}
	err := walkFolderImport(rootPath, func(path string, d fs.DirEntry) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if d.IsDir() {
			sources, ok, detectErr := importer.CalibreBookSources(path)
			if detectErr != nil {
				out.addError(path, rootPath, detectErr)
				return filepath.SkipDir
			}
			if ok {
				out.CalibreBooks++
				for _, source := range sources {
					out.Files++
					if err := out.addProbe(ctx, source.Path, rootPath, s.db); err != nil {
						return err
					}
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !importer.IsSupportedBook(d.Name()) {
			out.Skipped++
			return nil
		}
		out.Files++
		return out.addProbe(ctx, path, rootPath, s.db)
	}, func(path string, err error) {
		out.addError(path, rootPath, err)
	})
	if err != nil {
		return FolderImportPreviewDTO{}, fmt.Errorf("walk folder import: %w", err)
	}
	return out, nil
}

func (p *FolderImportPreviewDTO) addProbe(ctx context.Context, path, rootPath string, database db.Queryer) error {
	probe, err := importer.ProbeSource(ctx, database, importer.Source{Path: path})
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		p.addError(path, rootPath, err)
		return nil
	}
	if probe.Duplicate {
		p.Duplicates++
		if probe.Existing.WorkTrashed {
			p.Trashed++
		}
	} else {
		p.WouldImport++
	}
	return nil
}

func (p *FolderImportPreviewDTO) addError(path, rootPath string, err error) {
	p.Failed++
	p.Errors = appendBoundedError(p.Errors, rootPath, path, err)
}

func (s *Server) importFolder(ctx context.Context, rootPath string, root storage.Root, renderer *pdfcover.Renderer, opts importer.Options) (FolderImportResultDTO, error) {
	out := FolderImportResultDTO{Path: rootPath}
	err := walkFolderImport(rootPath, func(path string, d fs.DirEntry) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if d.IsDir() {
			sources, ok, detectErr := importer.CalibreBookSources(path)
			if detectErr != nil {
				out.addError(path, rootPath, detectErr, 1)
				return filepath.SkipDir
			}
			if ok {
				out.CalibreBooks++
				out.Files += len(sources)
				group, err := importer.ImportGroup(ctx, s.db, root, sources, renderer, opts)
				if err != nil {
					if cause := context.Cause(ctx); cause != nil {
						return cause
					}
					out.addError(path, rootPath, err, len(sources))
					return filepath.SkipDir
				}
				out.Warnings += len(group.Warnings)
				if group.Restored {
					out.Restored++
				}
				for _, res := range group.Results {
					out.addImportResult(res)
				}
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !importer.IsSupportedBook(d.Name()) {
			out.Skipped++
			return nil
		}
		out.Files++
		res, err := importer.ImportFile(ctx, s.db, root, path, renderer, opts)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			out.addError(path, rootPath, err, 1)
			return nil
		}
		out.addImportResult(res)
		return nil
	}, func(path string, err error) {
		out.addError(path, rootPath, err, 1)
	})
	if err != nil {
		return FolderImportResultDTO{}, fmt.Errorf("walk folder import: %w", err)
	}
	return out, nil
}

func (r *FolderImportResultDTO) addImportResult(res importer.Result) {
	if res.Status == importer.StatusDuplicate {
		r.Duplicates++
		if res.WorkTrashed {
			r.Trashed++
		}
	} else {
		r.Imported++
	}
	r.Warnings += len(res.Warnings)
}

func (r *FolderImportResultDTO) addError(path, rootPath string, err error, files int) {
	if files <= 0 {
		files = 1
	}
	r.Failed += files
	r.Errors = appendBoundedError(r.Errors, rootPath, path, err)
}

func walkFolderImport(rootPath string, visit func(path string, d fs.DirEntry) error, onError func(path string, err error)) error {
	return filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == rootPath {
				return err
			}
			onError(path, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == rootPath {
			return nil
		}
		return visit(path, d)
	})
}

func appendBoundedError(errors []string, rootPath, path string, err error) []string {
	if len(errors) >= maxFolderImportErrors {
		return errors
	}
	rel := path
	if r, relErr := filepath.Rel(rootPath, path); relErr == nil {
		rel = r
	}
	return append(errors, fmt.Sprintf("%s: %v", rel, err))
}

func realPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func realPathIfPossible(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	if os.IsNotExist(err) {
		return filepath.Clean(abs), nil
	}
	return "", err
}

func pathsOverlap(a, b string) bool {
	return pathInside(a, b) || pathInside(b, a)
}

func pathInside(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
