package web

import (
	"context"
	"os"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/position"
)

func (s *Server) readerPosition(ctx context.Context, userID, assetID int64) (*db.ReaderState, error) {
	return s.readingPosition(ctx, userID, assetID, coordinateWeb)
}

type readerCoordinate uint8

const (
	coordinateWeb readerCoordinate = iota
	coordinateKOReader
	coordinateKobo
)

func (s *Server) readingPosition(ctx context.Context, userID, assetID int64, target readerCoordinate) (*db.ReaderState, error) {
	for range 2 {
		state, err := db.GetReaderState(s.db.Read(ctx), userID, assetID)
		if err != nil {
			return nil, err
		}
		available := target == coordinateWeb && !state.Locator.IsZero() ||
			target == coordinateKOReader && state.KOReaderPosition != "" ||
			target == coordinateKobo && !state.KoboPosition.IsZero()
		if available || state.Locator.CFI == "" && state.KOReaderPosition == "" && !strings.EqualFold(state.KoboPosition.Type, "KoboSpan") {
			return state, nil
		}
		state, err = s.convertReaderPosition(ctx, state, target)
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
	state, err := s.readingPosition(ctx, userID, assetID, coordinateKOReader)
	if err != nil {
		return nil, err
	}
	if state.Revision == 0 {
		// Until the first upload adopts it, a newly matched book can still
		// have its only saved position in the external KOSync fallback.
		return db.GetExternalKOReaderState(s.db.Read(ctx), userID, document)
	}
	return state.KOReaderState(), nil
}

// Fill the missing coordinate against the asset's current EPUB or KEPUB,
// including older downloads. Leave unresolved positions unchanged.
// Callers have already checked access to the asset.
// A nil result means the reading revision changed; callers may retry.
func (s *Server) convertReaderPosition(ctx context.Context, state *db.ReaderState, target readerCoordinate) (*db.ReaderState, error) {
	file, size, err := s.openPositionSource(ctx, state.AssetID)
	if err != nil || file == nil {
		return state, err
	}
	defer file.Close()
	converted := *state
	if converted.Locator.CFI == "" {
		var cfi string
		switch {
		case state.KOReaderPosition != "":
			cfi, err = position.KOReaderToCFI(ctx, file, size, state.KOReaderPosition)
		case strings.EqualFold(state.KoboPosition.Type, "KoboSpan"):
			cfi, err = position.KEPUBToCFI(ctx, file, size, position.KEPUB{
				Path: state.KoboPosition.Source, Fragment: state.KoboPosition.Fragment,
			})
		}
		if err == nil && cfi != "" {
			converted.Locator = db.Locator{CFI: cfi}
		}
	}
	if converted.Locator.CFI != "" {
		switch target {
		case coordinateKOReader:
			converted.KOReaderPosition, _ = position.CFIToKOReader(ctx, file, size, converted.Locator.CFI)
		case coordinateKobo:
			if pos, err := position.CFIToKEPUB(ctx, file, size, converted.Locator.CFI); err == nil {
				converted.KoboPosition = db.KoboPosition{Source: pos.Path, Fragment: pos.Fragment,
					Type: "KoboSpan", ChapterProgressPercent: new(pos.ChapterProgress * 100)}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if converted.Locator.Equal(state.Locator) && converted.KOReaderPosition == state.KOReaderPosition && converted.KoboPosition.Equal(state.KoboPosition) {
		return state, nil
	}
	// Retain successful conversions; failures must remain retryable.
	saved, err := s.db.CachePositionConversion(ctx, &converted)
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
	if f := format.FormatFromKey(formatKey); f != format.FormatEPUB && f != format.FormatKEPUB {
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
