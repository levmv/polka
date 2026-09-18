package web

import (
	"context"
	"os"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/position"
)

func (s *Server) readerPosition(ctx context.Context, userID, assetID int64) (*db.ReaderState, error) {
	for range 2 {
		state, err := db.GetReaderState(s.db.Read(ctx), userID, assetID)
		if err != nil {
			return nil, err
		}
		if !state.Locator.IsZero() || state.KOReaderPosition == "" {
			return state, nil
		}
		state, err = s.convertReaderPosition(ctx, state)
		if err != nil || state != nil {
			return state, err
		}
	}
	// Reading changed while converting. A fresh percentage is a safe fallback;
	// never return coordinates computed for a different observation.
	return db.GetReaderState(s.db.Read(ctx), userID, assetID)
}

// Known download hashes share the asset's native coordinate.
func (s *Server) koReaderState(ctx context.Context, userID, assetID int64, document string) (*db.KOReaderState, error) {
	if assetID == 0 {
		return db.GetExternalKOReaderState(s.db.Read(ctx), userID, document)
	}
	for range 2 {
		state, err := db.GetReaderState(s.db.Read(ctx), userID, assetID)
		if err != nil {
			return nil, err
		}
		if state.Revision == 0 {
			// Until the first upload adopts it, a newly matched book can still
			// have its only saved position in the external KOSync fallback.
			return db.GetExternalKOReaderState(s.db.Read(ctx), userID, document)
		}
		if state.KOReaderPosition != "" || state.Locator.CFI == "" {
			return state.KOReaderState(), nil
		}
		state, err = s.convertReaderPosition(ctx, state)
		if err != nil {
			return nil, err
		}
		if state != nil {
			return state.KOReaderState(), nil
		}
	}
	return nil, nil
}

// Fill the missing coordinate against the asset's current EPUB, including for
// older downloads. Leave positions the converter cannot resolve unchanged.
// Callers have already checked access to the asset.
// A nil result means the reading revision changed; callers may retry.
func (s *Server) convertReaderPosition(ctx context.Context, state *db.ReaderState) (*db.ReaderState, error) {
	file, size, err := s.openPositionSource(ctx, state.AssetID)
	if err != nil || file == nil {
		return state, err
	}
	defer file.Close()
	converted := *state
	if state.Locator.IsZero() {
		cfi, err := position.KOReaderToCFI(ctx, file, size, state.KOReaderPosition)
		if err != nil {
			return state, ctx.Err()
		}
		converted.Locator = db.Locator{CFI: cfi}
	} else {
		address, err := position.CFIToKOReader(ctx, file, size, state.Locator.CFI)
		if err != nil {
			return state, ctx.Err()
		}
		converted.KOReaderPosition = address
	}
	// Retain successful conversions; failures must remain retryable.
	saved, err := s.db.CachePositionConversion(ctx, state.UserID, state.AssetID, state.Revision, converted.Locator, converted.KOReaderPosition)
	if err != nil || !saved {
		return nil, err
	}
	return &converted, nil
}

func (s *Server) openPositionSource(ctx context.Context, assetID int64) (*os.File, int64, error) {
	var storagePath, formatKey string
	err := s.db.Read(ctx).QueryRow("SELECT storage_path, format FROM assets WHERE id = ?", assetID).
		Scan(&storagePath, &formatKey)
	if err != nil {
		return nil, 0, err
	}
	if format.FormatFromKey(formatKey) != format.FormatEPUB {
		return nil, 0, nil
	}
	path, err := s.managedRoot().Resolve(storagePath)
	if err != nil {
		return nil, 0, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, nil
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, nil
	}
	return file, info.Size(), nil
}
