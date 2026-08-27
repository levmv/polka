package ingest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/pdfcover"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/workslot"
)

const (
	pollInterval       = 10 * time.Second
	DefaultStableScans = 2
)

type Options struct {
	StableScans   int
	DeleteSources bool
	ImportQueue   *workslot.Queue
	Logf          func(format string, args ...any)

	// CoverRoot is where imported cover files are placed (the app data dir,
	// separate from the books root). When unset, covers fall back to the books
	// root — used only by tests that do not split cover storage.
	CoverRoot storage.Root
}

type Service struct {
	db            *db.DB
	storageRoot   storage.Root
	coverRoot     storage.Root
	importQueue   *workslot.Queue
	scanQueue     *workslot.Queue
	path          string
	stableScans   int
	deleteSources bool
	logf          func(format string, args ...any)
	importFile    func(context.Context, *db.DB, storage.Root, string, *pdfcover.Renderer, importer.Options) (importer.Result, error)

	mu           sync.Mutex
	observed     map[string]observation
	processed    map[string]processedCandidate
	running      bool
	lastScanAt   int64
	lastImportAt int64
	lastError    string
}

type observation struct {
	fingerprint string
	stableCount int
}

type processedCandidate struct {
	fingerprint string
	failed      bool
}

type candidate struct {
	Path        string
	RelPath     string
	Fingerprint string
	Sources     []importer.Source
}

type importOutcome struct {
	status      importer.Status
	workTrashed bool
	restored    bool
}

type Summary struct {
	Imported   int
	Duplicates int
	// Trashed is the subset of duplicate candidates whose work remains in Trash.
	Trashed int
	// Restored counts imported groups returned to the live catalog.
	Restored int
	Failed   int
	Deferred int
}

type Status struct {
	Path         string
	Reachable    bool
	Running      bool
	Pending      int
	LastScanAt   int64
	LastImportAt int64
	LastError    string
}

func NewService(database *db.DB, root storage.Root, path string, opts Options) *Service {
	stableScans := opts.StableScans
	if stableScans <= 0 {
		stableScans = DefaultStableScans
	}
	importQueue := opts.ImportQueue
	if importQueue == nil {
		importQueue = workslot.New()
	}
	return &Service{
		db:            database,
		storageRoot:   root,
		coverRoot:     opts.CoverRoot,
		importQueue:   importQueue,
		scanQueue:     workslot.New(),
		path:          filepath.Clean(path),
		stableScans:   stableScans,
		deleteSources: opts.DeleteSources,
		logf:          opts.Logf,
		importFile:    importer.ImportFile,
		observed:      make(map[string]observation),
		processed:     make(map[string]processedCandidate),
	}
}

func NewServiceFromSettings(database *db.DB, dataDir string, root storage.Root, opts Options) (*Service, error) {
	cfg, err := OpenConfig(database, dataDir)
	if err != nil {
		return nil, err
	}
	opts.DeleteSources = cfg.DeleteSources
	if opts.CoverRoot.Path == "" {
		opts.CoverRoot = storage.NewRoot(dataDir)
	}
	return NewService(database, root, cfg.Path, opts), nil
}

func (s *Service) Start(ctx context.Context) {
	s.setRunning(true)
	defer s.setRunning(false)

	s.scanAndLog(ctx)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanAndLog(ctx)
		}
	}
}

func (s *Service) ScanOnce(ctx context.Context, force bool) (Summary, error) {
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	releaseScan, err := s.scanQueue.Acquire(ctx)
	if err != nil {
		return Summary{}, err
	}
	defer releaseScan()

	if err := EnsureLayout(s.path); err != nil {
		s.setLastError(err)
		return Summary{}, err
	}

	candidates, scanErrs, err := findCandidates(s.path)
	if err != nil {
		s.setLastError(err)
		return Summary{}, err
	}
	s.noteScan()
	s.pruneObserved(candidates)

	summary := Summary{Failed: len(scanErrs)}
	if len(scanErrs) > 0 {
		s.setLastError(summarizeScanErrors(scanErrs))
	}
	if len(candidates) == 0 {
		if summary.Failed == 0 {
			s.clearLastError()
		}
		return summary, nil
	}

	catalogHasBooks, err := db.HasAnyAsset(s.db)
	if err != nil {
		s.setLastError(err)
		return summary, err
	}
	if err := storage.RequireWritableRoot(s.storageRoot, catalogHasBooks); err != nil {
		s.setLastError(err)
		return summary, err
	}

	renderer := pdfcover.NewRenderer()
	defer renderer.Close()
	importOptions := importer.Options{CoverRoot: s.coverRoot}
	template, err := storage.OpenBookPathTemplate(s.db)
	if err != nil {
		s.setLastError(err)
		return summary, err
	}
	importOptions.PathTemplate = template

	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			s.setLastError(err)
			return summary, err
		}
		if !force && s.isProcessed(c) {
			continue
		}
		if !force && !s.isStable(c) {
			summary.Deferred++
			continue
		}

		if len(c.Sources) == 0 && !importer.IsSupportedBook(c.Path) {
			summary.Failed++
			s.markProcessed(c, true)
			s.setLastError(fmt.Errorf("%s: unsupported book format", c.RelPath))
			continue
		}

		outcome, err := s.importCandidate(ctx, s.storageRoot, c, renderer, importOptions)
		if err != nil {
			s.setLastError(err)
			return summary, err
		}
		switch outcome.status {
		case importer.StatusImported:
			summary.Imported++
			if outcome.restored {
				summary.Restored++
			}
			s.noteImport()
			if err := s.finishSuccessfulCandidate(c); err != nil {
				s.setLastError(err)
				return summary, err
			}
		case importer.StatusDuplicate:
			summary.Duplicates++
			if outcome.workTrashed {
				summary.Trashed++
			}
			s.noteImport()
			if err := s.finishSuccessfulCandidate(c); err != nil {
				s.setLastError(err)
				return summary, err
			}
		default:
			summary.Failed++
			s.markProcessed(c, true)
		}
	}
	if summary.Failed == 0 && !s.hasProcessedFailure(candidates) {
		s.clearLastError()
	}
	return summary, nil
}

func (s *Service) Status() (Status, error) {
	status, candidates, err := statusForPath(s.path)
	if err != nil {
		return status, err
	}

	s.mu.Lock()
	status.Running = s.running
	status.LastScanAt = s.lastScanAt
	status.LastImportAt = s.lastImportAt
	status.LastError = s.lastError
	s.mu.Unlock()
	status.Pending = s.pendingCount(candidates)
	return status, nil
}

// StatusForPath reports the incoming folder's state without ever creating it.
// A status read must not MkdirAll: on a dropped NAS mount that would populate a
// shadow directory in the empty mountpoint (the exact hazard storage guards
// against). A missing/unreachable folder reports Reachable=false, nothing
// pending, and no tree walk. The folder is created only by library bootstrap,
// config save, or the running service — never by polling status.
func StatusForPath(path string) (Status, error) {
	status, _, err := statusForPath(path)
	return status, err
}

func statusForPath(path string) (Status, []candidate, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Status{Path: path}, nil, nil
		}
		return Status{}, nil, fmt.Errorf("stat ingest directory %s: %w", path, err)
	}
	if !info.IsDir() {
		return Status{Path: path}, nil, nil
	}
	candidates, _, err := findCandidates(path)
	if err != nil {
		return Status{}, nil, err
	}
	return Status{
		Path:      path,
		Reachable: true,
		Pending:   len(candidates),
	}, candidates, nil
}

func EnsureLayout(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create ingest directory %s: %w", path, err)
	}
	return nil
}

func (s *Service) scanAndLog(ctx context.Context) {
	summary, err := s.ScanOnce(ctx, false)
	if err != nil {
		if s.logf != nil {
			s.logf("ingest scan failed: %v", err)
		}
		return
	}
	if s.logf != nil && (summary.Imported > 0 || summary.Duplicates > 0 || summary.Failed > 0) {
		s.logf("ingest scan: imported=%d duplicates=%d trashed=%d restored=%d failed=%d deferred=%d", summary.Imported, summary.Duplicates, summary.Trashed, summary.Restored, summary.Failed, summary.Deferred)
	}
}

func (s *Service) importCandidate(ctx context.Context, root storage.Root, c candidate, renderer *pdfcover.Renderer, opts importer.Options) (outcome importOutcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			outcome = importOutcome{status: s.failCandidate(c, fmt.Sprintf("panic while importing: %v", r))}
			err = nil
		}
	}()

	release, err := s.importQueue.Acquire(ctx)
	if err != nil {
		return importOutcome{}, err
	}
	defer release()

	if len(c.Sources) > 0 {
		group, groupErr := importer.ImportGroup(ctx, s.db, root, c.Sources, renderer, opts)
		if groupErr != nil {
			err = groupErr
		} else {
			outcome = importOutcome{
				status:      groupStatus(group),
				workTrashed: groupWorkTrashed(group),
				restored:    group.Restored,
			}
		}
	} else {
		var res importer.Result
		res, err = s.importFile(ctx, s.db, root, c.Path, renderer, opts)
		outcome = importOutcome{status: res.Status, workTrashed: res.WorkTrashed}
	}
	if err != nil {
		return importOutcome{status: s.failCandidate(c, err.Error())}, nil
	}
	return outcome, nil
}

func (s *Service) failCandidate(c candidate, reason string) importer.Status {
	s.setLastError(fmt.Errorf("%s: %s", c.RelPath, reason))
	return ""
}

func (s *Service) finishSuccessfulCandidate(c candidate) error {
	if s.deleteSources {
		if err := removeCandidateSource(c); err != nil {
			return fmt.Errorf("delete imported source %s: %w", c.RelPath, err)
		}
	}
	s.markProcessed(c, false)
	return nil
}

func removeCandidateSource(c candidate) error {
	if len(c.Sources) > 0 {
		return os.RemoveAll(c.Path)
	}
	if err := os.Remove(c.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func findCandidates(root string) ([]candidate, []error, error) {
	var candidates []candidate
	var scanErrs []error
	addScanError := func(path string, err error) {
		rel := path
		if r, relErr := filepath.Rel(root, path); relErr == nil {
			rel = r
		}
		scanErrs = append(scanErrs, fmt.Errorf("%s: %w", rel, err))
	}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == root {
				return err
			}
			addScanError(path, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			sources, ok, detectErr := importer.CalibreBookSources(path)
			if detectErr != nil {
				addScanError(path, detectErr)
				return filepath.SkipDir
			}
			if ok {
				fingerprint, err := directoryFingerprint(path)
				if err != nil {
					addScanError(path, err)
					return filepath.SkipDir
				}
				candidates = append(candidates, candidate{
					Path:        path,
					RelPath:     rel,
					Fingerprint: fingerprint,
					Sources:     sources,
				})
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if isIgnoredFile(d.Name()) || isSidecarFile(d.Name()) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			addScanError(path, err)
			return nil
		}
		candidates = append(candidates, candidate{
			Path:        path,
			RelPath:     rel,
			Fingerprint: fmt.Sprintf("%d/%d", info.Size(), info.ModTime().UnixNano()),
		})
		return nil
	})
	return candidates, scanErrs, err
}

func summarizeScanErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return fmt.Errorf("%v (and %d more scan errors)", errs[0], len(errs)-1)
}

func directoryFingerprint(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() || isIgnoredFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s:%d/%d", entry.Name(), info.Size(), info.ModTime().UnixNano()))
	}
	return strings.Join(parts, "|"), nil
}

func groupStatus(group importer.GroupResult) importer.Status {
	for _, result := range group.Results {
		if result.Status == importer.StatusImported {
			return importer.StatusImported
		}
	}
	return importer.StatusDuplicate
}

func groupWorkTrashed(group importer.GroupResult) bool {
	for _, result := range group.Results {
		if result.WorkTrashed {
			return true
		}
	}
	return false
}

func (s *Service) isStable(c candidate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	obs := s.observed[c.RelPath]
	if obs.fingerprint == c.Fingerprint {
		obs.stableCount++
	} else {
		obs = observation{fingerprint: c.Fingerprint, stableCount: 1}
	}
	s.observed[c.RelPath] = obs
	return obs.stableCount >= s.stableScans
}

func (s *Service) pruneObserved(candidates []candidate) {
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		seen[c.RelPath] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.observed {
		if !seen[key] {
			delete(s.observed, key)
		}
	}
	for key := range s.processed {
		if !seen[key] {
			delete(s.processed, key)
		}
	}
}

func (s *Service) isProcessed(c candidate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.processed[c.RelPath].fingerprint == c.Fingerprint
}

func (s *Service) markProcessed(c candidate, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processed[c.RelPath] = processedCandidate{
		fingerprint: c.Fingerprint,
		failed:      failed,
	}
}

func (s *Service) hasProcessedFailure(candidates []candidate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range candidates {
		processed := s.processed[c.RelPath]
		if processed.fingerprint == c.Fingerprint && processed.failed {
			return true
		}
	}
	return false
}

func (s *Service) pendingCount(candidates []candidate) int {
	count := 0
	for _, c := range candidates {
		if !s.isProcessed(c) {
			count++
		}
	}
	return count
}

func isIgnoredFile(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") {
		return true
	}
	for _, suffix := range []string{".tmp", ".part", ".crdownload"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func isSidecarFile(name string) bool {
	switch strings.ToLower(name) {
	case "metadata.opf", "cover.jpg", "cover.jpeg", "cover.png":
		return true
	}
	return false
}

func (s *Service) setRunning(running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = running
}

func (s *Service) noteScan() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastScanAt = time.Now().Unix()
}

func (s *Service) noteImport() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastImportAt = time.Now().Unix()
}

func (s *Service) setLastError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastError = err.Error()
	}
}

func (s *Service) clearLastError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = ""
}
