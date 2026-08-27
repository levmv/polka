package writeback

import (
	"context"
	"fmt"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
	"github.com/levmv/polka/internal/workslot"
)

const (
	pollInterval      = 15 * time.Second
	retryAfter        = 5 * time.Minute
	DefaultBatchLimit = 5
)

type ServiceOptions struct {
	BatchLimit int
	WorkQueue  *workslot.Queue
	CoverRoot  storage.Root
	Logf       func(format string, args ...any)
}

type Service struct {
	db         *db.DB
	root       storage.Root
	coverRoot  storage.Root
	workQueue  *workslot.Queue
	batchLimit int
	logf       func(format string, args ...any)
	now        func() time.Time
}

func NewService(database *db.DB, root storage.Root, opts ServiceOptions) *Service {
	batchLimit := opts.BatchLimit
	if batchLimit <= 0 {
		batchLimit = DefaultBatchLimit
	}
	return &Service{
		db:         database,
		root:       root,
		coverRoot:  opts.CoverRoot,
		workQueue:  opts.WorkQueue,
		batchLimit: batchLimit,
		logf:       opts.Logf,
		now:        time.Now,
	}
}

func (s *Service) Start(ctx context.Context) {
	s.runAndLog(ctx)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runAndLog(ctx)
		}
	}
}

func (s *Service) RunOnce(ctx context.Context) (Summary, error) {
	mode, err := OpenMode(s.db.DB)
	if err != nil {
		return Summary{}, err
	}
	if mode != ModeAuto {
		return Summary{}, nil
	}

	now := s.now()
	rows, err := db.ListAutomaticMetadataWritebackAssets(
		s.db,
		db.FullVisibilityScope(),
		now.Add(-retryAfter).Unix(),
		s.batchLimit,
	)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Planned: len(rows)}
	if len(rows) == 0 {
		return summary, nil
	}
	catalogHasBooks, err := db.HasAnyAsset(s.db.DB)
	if err != nil {
		return summary, err
	}
	if err := storage.RequireWritableRoot(s.root, catalogHasBooks); err != nil {
		return summary, err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		result, err := writeAssetQueued(ctx, s.db, s.root, row.AssetID, Options{
			CoverRoot: s.coverRoot,
			WorkQueue: s.workQueue,
		})
		if result.Status != "" {
			appendResult(&summary, result)
		}
		if err != nil {
			return summary, fmt.Errorf("write back asset %s: %w", row.AssetID, err)
		}
	}
	return summary, nil
}

func (s *Service) runAndLog(ctx context.Context) {
	summary, err := s.RunOnce(ctx)
	if err != nil {
		if s.logf != nil {
			s.logf("metadata write-back reconciler failed: %v", err)
		}
		return
	}
	if s.logf != nil && (summary.Written > 0 || summary.Unchanged > 0 || summary.Failed > 0) {
		s.logf("metadata write-back reconciler: written=%d unchanged=%d failed=%d", summary.Written, summary.Unchanged, summary.Failed)
	}
}
