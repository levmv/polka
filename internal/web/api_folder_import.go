package web

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/importer"
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
		serverError(w, r, err)
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

	root := s.storageRoot
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
	releaseImport, err := s.storageQueue.Acquire(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer releaseImport()

	extractor := format.NewExtractor()
	defer extractor.Close()
	result, err := s.importFolder(r.Context(), path, root, extractor, importer.Options{
		PathTemplate: template,
		CoverRoot:    s.dataRoot(),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	status, err := s.adminStorageStatus(r.Context())
	if err != nil {
		serverError(w, r, err)
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
		{name: "books folder", path: s.storageRoot.Path},
	} {
		reservedPath, err := realPathIfPossible(reserved.path)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", reserved.name, err)
		}
		if pathsOverlap(sourcePath, reservedPath) {
			return "", fmt.Errorf("Folder must be outside the %s", reserved.name)
		}
	}
	return sourcePath, nil
}

func (s *Server) previewFolderImport(ctx context.Context, rootPath string) (FolderImportPreviewDTO, error) {
	out := FolderImportPreviewDTO{Path: rootPath}
	err := importer.WalkFolder(ctx, rootPath, func(found importer.FolderItem) error {
		if found.Err != nil {
			out.addError(found.Path, rootPath, found.Err, 1)
			return nil
		}
		if len(found.Sources) == 0 {
			out.Skipped++
			return nil
		}
		if found.Group {
			out.CalibreBooks++
			out.Files += len(found.Sources)
			probe, err := importer.ProbeGroup(ctx, s.db.Read(ctx), found.Sources)
			if err != nil {
				if cause := context.Cause(ctx); cause != nil {
					return cause
				}
				out.addError(found.Path, rootPath, err, len(found.Sources))
				return nil
			}
			for _, source := range probe {
				out.addProbeResult(source)
			}
			return nil
		}
		out.Files++
		if err := out.addProbe(ctx, found.Sources[0].Path, rootPath, s.db.Read(ctx)); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return FolderImportPreviewDTO{}, err
	}
	return out, nil
}

func (p *FolderImportPreviewDTO) addProbe(ctx context.Context, path, rootPath string, database db.Queryer) error {
	probe, err := importer.ProbeSource(ctx, database, importer.Source{Path: path})
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
		p.addError(path, rootPath, err, 1)
		return nil
	}
	p.addProbeResult(probe)
	return nil
}

func (p *FolderImportPreviewDTO) addProbeResult(probe importer.SourceProbe) {
	if probe.Duplicate {
		p.Duplicates++
		if probe.Existing.BookTrashed {
			p.Trashed++
		}
	} else {
		p.WouldImport++
	}
}

func (p *FolderImportPreviewDTO) addError(path, rootPath string, err error, files int) {
	p.Failed += files
	p.Errors = appendBoundedError(p.Errors, rootPath, path, err)
}

func (s *Server) importFolder(ctx context.Context, rootPath string, root storage.Root, extractor *format.Extractor, opts importer.Options) (FolderImportResultDTO, error) {
	out := FolderImportResultDTO{Path: rootPath}
	knownSizes, err := db.AssetContentSizes(s.db.Read(ctx))
	if err != nil {
		return out, err
	}
	opts.KnownAssetSizes = knownSizes
	err = importer.WalkFolder(ctx, rootPath, func(found importer.FolderItem) error {
		if found.Err != nil {
			out.addError(found.Path, rootPath, found.Err, 1)
			return nil
		}
		if len(found.Sources) == 0 {
			out.Skipped++
			return nil
		}

		out.Files += len(found.Sources)
		if found.Group {
			out.CalibreBooks++
			group, err := importer.ImportGroup(ctx, s.db, root, found.Sources, extractor, opts)
			if err != nil {
				if cause := context.Cause(ctx); cause != nil {
					return cause
				}
				out.addError(found.Path, rootPath, err, len(found.Sources))
				return nil
			}
			out.Warnings += len(group.Warnings)
			if group.Restored {
				out.Restored++
			}
			for _, res := range group.Results {
				out.addImportResult(res)
			}
			return nil
		}

		res, err := importer.Import(ctx, s.db, root, found.Sources[0], extractor, opts)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			out.addError(found.Path, rootPath, err, 1)
			return nil
		}
		out.addImportResult(res)
		return nil
	})
	if err != nil {
		return FolderImportResultDTO{}, err
	}
	return out, nil
}

func (r *FolderImportResultDTO) addImportResult(res importer.Result) {
	if res.Status == importer.StatusDuplicate {
		r.Duplicates++
		if res.BookTrashed {
			r.Trashed++
		}
	} else {
		r.Imported++
	}
	r.Warnings += len(res.Warnings)
}

func (r *FolderImportResultDTO) addError(path, rootPath string, err error, files int) {
	r.Failed += files
	r.Errors = appendBoundedError(r.Errors, rootPath, path, err)
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
