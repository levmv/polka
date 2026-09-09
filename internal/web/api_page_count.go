package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/pdfcover"
)

type pageCountResultDTO struct {
	AssetID   int64 `json:"asset_id"`
	PageCount int   `json:"page_count,omitzero"`
}

type pageCountRetry struct {
	hash       string
	retryAfter time.Time
}

// Counting uses an explicit POST so detail GETs and prefetches remain cheap.
// The storage slot excludes file replacement; reading the asset after acquiring
// it also lets concurrent requests reuse a count that has just been stored.
func (s *Server) handleAPIBookPageCount(w http.ResponseWriter, r *http.Request) {
	bookID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	scope, ok := s.requireBookAccess(w, r, bookID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	release, err := s.acquireStorageWorkSlot(ctx)
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer release()
	if ctx.Err() != nil {
		return
	}
	// Once admitted, finish and persist even if the client leaves. The handler
	// remains tracked for shutdown and keeps its original time budget.
	deadline, _ := ctx.Deadline()
	parent := s.requestBaseContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, stopWork := context.WithDeadline(parent, deadline)
	defer stopWork()
	asset, err := db.PrimaryPageCountAsset(s.db.Read(ctx), scope, bookID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	result := pageCountResultDTO{AssetID: asset.AssetID, PageCount: asset.PageCount}
	if asset.PageCount == 0 && !s.pageCountCoolingDown(asset) {
		pages, err := s.measurePageCount(ctx, asset)
		if err == nil && pages > 0 {
			stored, err := db.StorePageCount(s.db.Write(ctx), asset.AssetID, asset.SHA256, pages)
			if err != nil {
				serverError(w, r, err)
				return
			}
			if stored {
				result.PageCount = pages
			}
		} else if !errors.Is(err, context.Canceled) {
			// Avoid reopening a failed file on every visit. Changed bytes bypass
			// the cooldown; a restart forgets failures and allows another attempt.
			s.deferPageCountRetry(asset)
			if err != nil {
				log.Printf("page count for asset %d: %v", asset.AssetID, err)
			}
		}
	}
	if r.Context().Err() != nil {
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) measurePageCount(ctx context.Context, asset db.PageCountAsset) (int, error) {
	name, err := s.managedRoot().Resolve(asset.StoragePath)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(name)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() != asset.Size {
		return 0, fmt.Errorf("asset size differs from the catalog")
	}
	source := &pageCountSource{File: f, ctx: ctx}
	source.remainingBytes.Store(1 << 30)
	if asset.Format == format.FormatPDF {
		if pages, _ := format.ReadyPageCount(source, info.Size(), asset.Format); pages > 0 {
			return pages, nil
		}
		renderer := s.pageCountRenderer
		if renderer == nil {
			renderer = pdfcover.NewRenderer()
			defer renderer.Close()
		}
		return renderer.CountPages(ctx, source, info.Size())
	}
	return format.CountPages(ctx, source, info.Size(), asset.Format)
}

func (s *Server) pageCountCoolingDown(asset db.PageCountAsset) bool {
	retry := s.pageCountCooldown[asset.AssetID]
	return retry.hash == string(asset.SHA256) && time.Now().Before(retry.retryAfter)
}

func (s *Server) deferPageCountRetry(asset db.PageCountAsset) {
	if s.pageCountCooldown == nil {
		s.pageCountCooldown = make(map[int64]pageCountRetry)
	}
	if len(s.pageCountCooldown) >= 256 {
		var oldestID int64
		var oldest time.Time
		for id, retry := range s.pageCountCooldown {
			if oldest.IsZero() || retry.retryAfter.Before(oldest) {
				oldestID, oldest = id, retry.retryAfter
			}
		}
		delete(s.pageCountCooldown, oldestID)
	}
	s.pageCountCooldown[asset.AssetID] = pageCountRetry{hash: string(asset.SHA256), retryAfter: time.Now().Add(24 * time.Hour)}
}

type pageCountSource struct {
	*os.File
	ctx            context.Context
	remainingBytes atomic.Int64
}

func (s *pageCountSource) check(n int) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.remainingBytes.Add(-int64(n)) < 0 {
		return fmt.Errorf("page count exceeds read budget")
	}
	return nil
}

func (s *pageCountSource) ReadAt(p []byte, off int64) (int, error) {
	if err := s.check(len(p)); err != nil {
		return 0, err
	}
	return s.File.ReadAt(p, off)
}

func (s *pageCountSource) Read(p []byte) (int, error) {
	if err := s.check(len(p)); err != nil {
		return 0, err
	}
	return s.File.Read(p)
}
