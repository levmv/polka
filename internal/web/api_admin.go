package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/fsprofile"
	"github.com/levmv/polka/internal/ingest"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/writeback"
)

type AdminStorageDTO struct {
	Books     BooksStorageDTO    `json:"books"`
	Ingest    IngestStatusDTO    `json:"ingest"`
	Layout    FileLayoutDTO      `json:"layout"`
	Writeback WritebackStatusDTO `json:"writeback"`
}

type FileLayoutDTO struct {
	Template string `json:"template"`
}

// WritebackStatusDTO is the library-wide metadata write-back policy plus the
// backlog it governs: Pending is writable assets whose file is behind the
// catalog, Failed the subset whose last write attempt errored.
type WritebackStatusDTO struct {
	Mode    string `json:"mode"`
	Pending int    `json:"pending"`
	Failed  int    `json:"failed"`
}

// BooksStorageDTO is the health of the books folder: where the books live,
// whether the folder is currently reachable (the key signal for a NAS-backed
// folder), its filesystem, and how much it holds vs. how much room is left.
// FreeBytes is -1 when the filesystem can't report free space.
type BooksStorageDTO struct {
	Path      string `json:"path"`
	Reachable bool   `json:"reachable"`
	FSType    string `json:"fs_type"`
	Network   bool   `json:"network"`
	BookCount int    `json:"book_count"`
	SizeBytes int64  `json:"size_bytes"`
	FreeBytes int64  `json:"free_bytes"`
}

// StorageScanResultDTO reports the outcome of a manual "Scan now" pass together
// with the refreshed storage status, so the UI updates its counts in one round
// trip.
type StorageScanResultDTO struct {
	Imported   int             `json:"imported"`
	Duplicates int             `json:"duplicates"`
	Trashed    int             `json:"trashed"`
	Restored   int             `json:"restored"`
	Failed     int             `json:"failed"`
	Storage    AdminStorageDTO `json:"storage"`
}

type IngestStatusDTO struct {
	Enabled       bool   `json:"enabled"`
	DeleteSources bool   `json:"delete_sources"`
	Path          string `json:"path"`
	Reachable     bool   `json:"reachable"`
	Running       bool   `json:"running"`
	Pending       int    `json:"pending"`
	LastScanAt    int64  `json:"last_scan_at,omitzero"`
	LastImportAt  int64  `json:"last_import_at,omitzero"`
	LastError     string `json:"last_error,omitempty"`
}

type adminStorageUpdateRequest struct {
	Ingest    *ingestUpdateRequest    `json:"ingest"`
	Writeback *writebackUpdateRequest `json:"writeback"`
}

type ingestUpdateRequest struct {
	Enabled       *bool   `json:"enabled"`
	DeleteSources *bool   `json:"delete_sources"`
	Path          *string `json:"path"`
}

type writebackUpdateRequest struct {
	Mode *string `json:"mode"`
}

func (s *Server) handleAPIAdminStorage(w http.ResponseWriter, r *http.Request) {
	status, err := s.adminStorageStatus()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleAPIAdminStorageSave(w http.ResponseWriter, r *http.Request) {
	var req adminStorageUpdateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Ingest == nil && req.Writeback == nil {
		http.Error(w, "No storage settings to update", http.StatusBadRequest)
		return
	}

	// The write-back mode is an independent single-row setting with no filesystem
	// side effects, so it saves on its own rather than sharing the ingest tx.
	if req.Writeback != nil && req.Writeback.Mode != nil {
		mode := writeback.Mode(strings.TrimSpace(*req.Writeback.Mode))
		if err := writeback.SaveMode(s.db.DB, mode); err != nil {
			if errors.Is(err, writeback.ErrInvalidMode) {
				http.Error(w, "Write-back mode must be off, manual, or auto", http.StatusBadRequest)
			} else {
				serverError(w, err)
			}
			return
		}
	}

	if req.Ingest == nil {
		status, err := s.adminStorageStatus()
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}

	cfg, err := ingest.OpenConfig(s.db.DB, s.dataDir)
	if err != nil {
		serverError(w, err)
		return
	}
	pathChanged := false
	if req.Ingest.Path != nil {
		path := strings.TrimSpace(*req.Ingest.Path)
		if path == "" {
			http.Error(w, "Incoming folder path is required", http.StatusBadRequest)
			return
		}
		cfg.Path = path
		pathChanged = true
	}
	if req.Ingest.Enabled != nil {
		cfg.Enabled = *req.Ingest.Enabled
	}
	if req.Ingest.DeleteSources != nil {
		cfg.DeleteSources = *req.Ingest.DeleteSources
	}

	if cfg.Enabled || pathChanged {
		path, err := ingest.ResolvePath(s.dataDir, cfg.Path)
		if err != nil {
			serverError(w, err)
			return
		}
		if err := ingest.EnsureLayout(path); err != nil {
			serverError(w, err)
			return
		}
	}
	if err := s.db.Transact(r.Context(), func(tx *sql.Tx) error {
		var err error
		cfg, err = ingest.SaveConfig(tx, s.dataDir, cfg)
		return err
	}); err != nil {
		serverError(w, err)
		return
	}
	if err := s.configureIngest(cfg); err != nil {
		serverError(w, err)
		return
	}

	status, err := s.adminStorageStatus()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) adminStorageStatus() (AdminStorageDTO, error) {
	ingestConfig, err := ingest.OpenConfig(s.db.DB, s.dataDir)
	if err != nil {
		return AdminStorageDTO{}, err
	}
	ingestStatus, err := s.currentIngestStatus(ingestConfig)
	if err != nil {
		return AdminStorageDTO{}, err
	}
	books, err := s.booksStorageStatus()
	if err != nil {
		return AdminStorageDTO{}, err
	}
	wb, err := s.writebackStatus()
	if err != nil {
		return AdminStorageDTO{}, err
	}
	layout, err := s.fileLayoutStatus()
	if err != nil {
		return AdminStorageDTO{}, err
	}
	return AdminStorageDTO{
		Books:     books,
		Ingest:    ingestStatusDTO(ingestConfig, ingestStatus),
		Layout:    layout,
		Writeback: wb,
	}, nil
}

func (s *Server) fileLayoutStatus() (FileLayoutDTO, error) {
	template, err := storage.OpenBookPathTemplate(s.db.DB)
	if err != nil {
		return FileLayoutDTO{}, err
	}
	return FileLayoutDTO{Template: template}, nil
}

func (s *Server) writebackStatus() (WritebackStatusDTO, error) {
	mode, err := writeback.OpenMode(s.db.DB)
	if err != nil {
		return WritebackStatusDTO{}, err
	}
	counts, err := db.CountDirtyMetadataWritebackAssets(s.db.DB, db.FullVisibilityScope())
	if err != nil {
		return WritebackStatusDTO{}, err
	}
	return WritebackStatusDTO{
		Mode:    string(mode),
		Pending: counts.Dirty,
		Failed:  counts.Failed,
	}, nil
}

func (s *Server) booksStorageStatus() (BooksStorageDTO, error) {
	bookCount, sizeBytes, err := db.LibraryStorageStats(s.db.DB)
	if err != nil {
		return BooksStorageDTO{}, err
	}
	catalogHasBooks, err := db.HasAnyAsset(s.db.DB)
	if err != nil {
		return BooksStorageDTO{}, err
	}
	root := s.managedRoot()
	info := fsprofile.Detect(root.Path)
	// Reachable stays honest for the dropped-NAS case: the mountpoint can exist as
	// an empty directory while the catalog still has books. Reuse the write
	// guard's predicate so the health line and writes agree — including an
	// all-trashed catalog (HasAnyAsset, not the live book count) over an empty
	// mount, which must not read "reachable" while writes 503.
	reachable := storage.RequireWritableRoot(root, catalogHasBooks) == nil
	dto := BooksStorageDTO{
		Path:      root.Path,
		Reachable: reachable,
		FSType:    info.TypeOrUnknown(),
		Network:   info.IsNetwork(),
		BookCount: bookCount,
		SizeBytes: sizeBytes,
		FreeBytes: -1,
	}
	if usage, ok := fsprofile.DiskUsage(root.Path); ok {
		dto.FreeBytes = int64(usage.FreeBytes)
	}
	return dto, nil
}

// handleAPIAdminStorageScan runs one immediate ingest pass — the UI twin of
// `polka ingest`. It is an explicit admin action, so it works even when the
// incoming folder's automatic watching is turned off.
func (s *Server) handleAPIAdminStorageScan(w http.ResponseWriter, r *http.Request) {
	summary, err := s.scanIncomingNow(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	status, err := s.adminStorageStatus()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, StorageScanResultDTO{
		Imported:   summary.Imported,
		Duplicates: summary.Duplicates,
		Trashed:    summary.Trashed,
		Restored:   summary.Restored,
		Failed:     summary.Failed,
		Storage:    status,
	})
}

func (s *Server) scanIncomingNow(ctx context.Context) (ingest.Summary, error) {
	// A running watcher already owns the folder's dedup state; force a scan
	// through it. When watching is off there is no service, so build a transient
	// one-shot that shares the storage queue (StableScans: 1 imports immediately
	// rather than waiting for the file to settle across polls).
	if ingester := s.currentIngester(); ingester != nil {
		return ingester.ScanOnce(ctx, true)
	}
	svc, err := ingest.NewServiceFromSettings(s.db, s.dataDir, s.managedRoot(), ingest.Options{
		StableScans: 1,
		ImportQueue: s.storageQueue,
	})
	if err != nil {
		return ingest.Summary{}, err
	}
	return svc.ScanOnce(ctx, true)
}

func (s *Server) currentIngestStatus(cfg ingest.Config) (ingest.Status, error) {
	if cfg.Enabled {
		if ingester := s.currentIngester(); ingester != nil {
			return ingester.Status()
		}
	}
	return ingest.StatusForPath(cfg.Path)
}

func ingestStatusDTO(cfg ingest.Config, status ingest.Status) IngestStatusDTO {
	return IngestStatusDTO{
		Enabled:       cfg.Enabled,
		DeleteSources: cfg.DeleteSources,
		Path:          status.Path,
		Reachable:     status.Reachable,
		Running:       status.Running,
		Pending:       status.Pending,
		LastScanAt:    status.LastScanAt,
		LastImportAt:  status.LastImportAt,
		LastError:     status.LastError,
	}
}
