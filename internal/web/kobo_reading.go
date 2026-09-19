package web

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/polka/internal/db"
	kobowire "github.com/levmv/polka/internal/kobo"
)

func (s *Server) handleKoboReadingState(w http.ResponseWriter, r *http.Request) {
	asset, ok := s.requireKoboAsset(w, r)
	if !ok {
		return
	}
	state, err := s.koboReadingState(r.Context(), UserID(r.Context()), asset.AssetID, asset.AddedAt)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, []*kobowire.ReadingState{state})
}

func (s *Server) handleKoboReadingStateSave(w http.ResponseWriter, r *http.Request) {
	asset, ok := s.requireKoboAsset(w, r)
	if !ok {
		return
	}
	var request kobowire.ReadingStateUpdate
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	// Firmware can send statistics and other fields that Polka does not own.
	if err := json.UnmarshalRead(r.Body, &request); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	id := strconv.FormatInt(asset.AssetID, 10)
	if len(request.ReadingStates) != 1 || request.ReadingStates[0].EntitlementID != id {
		http.Error(w, "Expected one reading state for this book", http.StatusBadRequest)
		return
	}
	state := request.ReadingStates[0]
	input := db.KoboReadingUpdate{
		DeviceID:   strings.TrimSpace(r.Header.Get("X-Kobo-DeviceId")),
		DeviceName: strings.TrimSpace(r.Header.Get("X-Kobo-DeviceModel")),
	}
	if input.DeviceName == "" {
		input.DeviceName = "Kobo"
	}
	if bookmark := state.CurrentBookmark; bookmark != nil && (bookmark.Location != nil || bookmark.ProgressPercent != nil) {
		input.Position = &db.KoboPosition{}
		input.PositionUpdatedAt = bookmark.LastModified.Unix()
		if bookmark.LastModified.IsZero() {
			input.PositionUpdatedAt = state.LastModified.Unix()
		}
		if bookmark.ProgressPercent != nil {
			progress := *bookmark.ProgressPercent / 100
			input.Progress = &progress
		}
		if bookmark.ContentSourceProgressPercent != nil {
			input.Position.ChapterProgressPercent = bookmark.ContentSourceProgressPercent
		}
		if location := bookmark.Location; location != nil {
			input.Position.Source = location.Source
			input.Position.Fragment = location.Value
			input.Position.Type = location.Type
		}
	}
	if status := state.StatusInfo; status != nil {
		switch status.Status {
		case "ReadyToRead":
			input.Status = db.ReadingStatusUnread
		case "Reading":
			input.Status = db.ReadingStatusReading
		case "Finished":
			input.Status = db.ReadingStatusFinished
		default:
			http.Error(w, "Invalid reading status", http.StatusBadRequest)
			return
		}
		input.StatusUpdatedAt = status.LastModified.Unix()
		if status.LastModified.IsZero() {
			input.StatusUpdatedAt = state.LastModified.Unix()
		}
	}
	result, err := s.db.SaveKoboReading(r.Context(), UserID(r.Context()), asset.AssetID, input)
	if errors.Is(err, db.ErrInvalidReaderInput) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	bookmarkResult, statusResult := "Ignored", "Ignored"
	if result.PositionChanged {
		bookmarkResult = "Success"
	}
	if result.StatusChanged {
		statusResult = "Success"
	}
	writeJSON(w, http.StatusOK, kobowire.ReadingUpdateResponse{
		RequestResult: "Success",
		UpdateResults: []kobowire.ReadingUpdateResult{{EntitlementID: id,
			CurrentBookmarkResult: kobowire.Result{Result: bookmarkResult},
			StatusInfoResult:      kobowire.Result{Result: statusResult},
			StatisticsResult:      kobowire.Result{Result: "Ignored"},
		}},
	})
}

func (s *Server) koboReadingState(ctx context.Context, userID, assetID, createdAt int64) (*kobowire.ReadingState, error) {
	position, err := s.readingPosition(ctx, userID, assetID, coordinateKobo)
	if err != nil {
		return nil, err
	}
	status, err := db.GetReadingStatus(s.db.Read(ctx), userID, position.BookID)
	if err != nil {
		return nil, err
	}
	statusName := "Reading"
	switch status.Status {
	case db.ReadingStatusUnread:
		statusName = "ReadyToRead"
	case db.ReadingStatusFinished:
		statusName = "Finished"
	}
	positionTime := time.Unix(position.UpdatedAt, 0).UTC()
	if position.UpdatedAt == 0 {
		positionTime = time.Unix(createdAt, 0).UTC()
	}
	statusTime := time.Unix(status.UpdatedAt, 0).UTC()
	if status.UpdatedAt == 0 {
		statusTime = time.Unix(createdAt, 0).UTC()
	}
	modified := positionTime
	if statusTime.After(modified) {
		modified = statusTime
	}
	// Six decimal places retain fractional percentages without scaling noise
	// that Kobo can mistake for a different position.
	progress := math.Round(position.Progress*1e8) / 1e6
	bookmark := &kobowire.Bookmark{LastModified: positionTime, ProgressPercent: &progress}
	pos := position.KoboPosition
	bookmark.ContentSourceProgressPercent = pos.ChapterProgressPercent
	if pos.Source != "" || pos.Type != "" || pos.Fragment != "" {
		bookmark.Location = &kobowire.Location{Source: pos.Source, Type: pos.Type, Value: pos.Fragment}
	}
	return &kobowire.ReadingState{
		EntitlementID: strconv.FormatInt(assetID, 10), Created: time.Unix(createdAt, 0).UTC(),
		LastModified: modified, PriorityTimestamp: modified, CurrentBookmark: bookmark,
		StatusInfo: &kobowire.StatusInfo{LastModified: statusTime, Status: statusName},
	}, nil
}
