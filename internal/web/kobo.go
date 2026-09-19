package web

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	kobowire "github.com/levmv/polka/internal/kobo"
)

const maxKoboSyncResponseBytes = 1 << 20

func (s *Server) handleKoboRoot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleKoboInitialization(w http.ResponseWriter, r *http.Request) {
	base := koboBaseURL(r)
	resources := map[string]any{
		"device_auth":                base + "/v1/auth/device",
		"device_refresh":             base + "/v1/auth/refresh",
		"image_host":                 base,
		"image_url_template":         base + "/{ImageId}/{Width}/{Height}/false/image.jpg",
		"image_url_quality_template": base + "/{ImageId}/{Width}/{Height}/{Quality}/{IsGreyscale}/image.jpg",
		"library_metadata":           base + "/v1/library/{Ids}/metadata",
		"library_sync":               base + "/v1/library/sync",
		"reading_state":              base + "/v1/library/{Ids}/state",
		"kobo_audiobooks_enabled":    "False",
		"kobo_subscriptions_enabled": "False",
		"use_one_store":              "True",
	}
	w.Header().Set("X-Kobo-ApiToken", "e30=")
	writeJSON(w, http.StatusOK, map[string]any{"Resources": resources})
}

func (s *Server) handleKoboAuth(w http.ResponseWriter, r *http.Request) {
	userKey, ok := readKoboUserKey(w, r)
	if !ok {
		return
	}
	accessToken := randomKoboValue(24)
	refreshToken := randomKoboValue(24)
	trackingID := randomKoboTrackingID()
	writeJSON(w, http.StatusOK, map[string]string{
		"AccessToken":  accessToken,
		"RefreshToken": refreshToken,
		"TokenType":    "Bearer",
		"TrackingId":   trackingID,
		"UserKey":      userKey,
	})
}

func readKoboUserKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONDecodeError(w, err)
		return "", false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return "", true
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSONDecodeError(w, err)
		return "", false
	}
	userKey, _ := payload["UserKey"].(string)
	return userKey, true
}

func randomKoboValue(size int) string {
	buf := make([]byte, size)
	rand.Read(buf)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func randomKoboTrackingID() string {
	buf := make([]byte, 16)
	rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func (s *Server) handleKoboLibrarySync(w http.ResponseWriter, r *http.Request) {
	connectionID := koboConnectionID(r.Context())
	after := parseKoboCursor(r.Header.Get("X-Kobo-Synctoken"), connectionID)
	changes, currentRevision, databaseMore, err := s.db.SyncKoboConnection(
		r.Context(), connectionID, after, db.KoboSyncPageLimit,
	)
	if errors.Is(err, db.ErrKoboInvalidCursor) {
		// A retained device cursor may be ahead of a restored database.
		after = 0
		changes, currentRevision, databaseMore, err = s.db.SyncKoboConnection(
			r.Context(), connectionID, after, db.KoboSyncPageLimit,
		)
	}
	if errors.Is(err, db.ErrKoboConnectionNotFound) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	body, nextRevision, more, err := marshalKoboSyncPage(
		changes, after, koboBaseURL(r), currentRevision, databaseMore,
		func(change db.KoboChange) (*kobowire.ReadingState, error) {
			return s.koboReadingState(r.Context(), UserID(r.Context()), change.AssetID, change.AddedAt)
		},
	)
	if err != nil {
		serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Kobo-Synctoken", fmt.Sprintf("polka:%d:%d", connectionID, nextRevision))
	if more {
		w.Header().Set("X-Kobo-Sync", "continue")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func marshalKoboSyncPage(changes []db.KoboChange, after int64, base string, currentRevision int64, databaseMore bool,
	readingState func(db.KoboChange) (*kobowire.ReadingState, error),
) ([]byte, int64, bool, error) {
	var body bytes.Buffer
	body.WriteByte('[')
	count := 0
	nextRevision := currentRevision
	for _, change := range changes {
		wire := mapKoboChange(change)
		if change.Present {
			var err error
			wire.ReadingState, err = readingState(change)
			if err != nil {
				return nil, 0, false, err
			}
		}
		encoded, err := json.Marshal(kobowire.BuildSyncItems(wire, after, base))
		if err != nil {
			return nil, 0, false, err
		}
		// Keep a book's metadata and reading events together under one cursor.
		group := encoded[1 : len(encoded)-1]
		separator := 0
		if count > 0 {
			separator = 1
		}
		if count > 0 && body.Len()+separator+len(group)+1 > maxKoboSyncResponseBytes {
			break
		}
		if separator != 0 {
			body.WriteByte(',')
		}
		body.Write(group)
		count++
		nextRevision = change.Revision
	}
	body.WriteByte(']')
	return body.Bytes(), nextRevision, databaseMore || count < len(changes), nil
}

func parseKoboCursor(raw string, connectionID int64) int64 {
	// Devices retain tokens from the store or a previous connection. Only a
	// cursor for this feed can acknowledge its books; anything else starts it.
	raw, ok := strings.CutPrefix(strings.TrimSpace(raw), fmt.Sprintf("polka:%d:", connectionID))
	if !ok {
		return 0
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cursor < 0 {
		return 0
	}
	return cursor
}

func mapKoboPublication(publication db.KoboPublication) kobowire.Publication {
	var seriesIndex *float64
	if publication.SeriesIndex.Valid {
		value := publication.SeriesIndex.Float64
		seriesIndex = &value
	}
	return kobowire.Publication{
		AssetID:       publication.AssetID,
		Size:          publication.Size,
		Title:         publication.Title,
		Description:   publication.Description,
		Publisher:     publication.Publisher,
		PublishedDate: publication.PublishedDate,
		Language:      publication.Language,
		Series:        publication.Series,
		SeriesIndex:   seriesIndex,
		Authors:       publication.Authors,
		AddedAt:       publication.AddedAt,
		ModifiedAt:    publication.ModifiedAt,
		CoverVersion:  publication.CoverVersion,
	}
}

func mapKoboChange(change db.KoboChange) kobowire.Change {
	return kobowire.Change{
		Publication:   mapKoboPublication(change.KoboPublication),
		FirstRevision: change.FirstRevision,
		Present:       change.Present,
		ChangedAt:     change.ChangedAt,
	}
}

func (s *Server) handleKoboMetadata(w http.ResponseWriter, r *http.Request) {
	asset, ok := s.requireKoboAsset(w, r)
	if !ok {
		return
	}
	publication, err := db.KoboPublicationForAsset(s.db.Read(r.Context()), asset.AssetID)
	if errors.Is(err, db.ErrAssetNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	metadata := kobowire.BuildMetadata(mapKoboPublication(*publication), koboBaseURL(r))
	writeJSON(w, http.StatusOK, []kobowire.Metadata{metadata})
}

// Cover and download use the shared serving and conversion rules after
// checking that the asset is available through this Kobo connection.
func (s *Server) handleKoboCover(w http.ResponseWriter, r *http.Request) {
	// The suffix refreshes Kobo's image cache; serving still uses the current cover.
	assetID, _, _ := strings.Cut(r.PathValue("id"), "-")
	r.SetPathValue("id", assetID)
	asset, ok := s.requireKoboAsset(w, r)
	if !ok {
		return
	}
	height, _ := strconv.Atoi(r.PathValue("height"))
	query := r.URL.Query()
	if height > 0 && height <= 500 {
		query.Set("variant", "thumb")
	} else {
		query.Set("variant", "display")
	}
	r.URL.RawQuery = query.Encode()
	s.serveCover(w, r, asset.BookID)
}

func (s *Server) handleKoboDownload(w http.ResponseWriter, r *http.Request) {
	asset, ok := s.requireKoboAsset(w, r)
	if !ok {
		return
	}
	if !strings.EqualFold(r.PathValue("format"), "kepub") {
		http.NotFound(w, r)
		return
	}
	switch format.FormatFromKey(asset.Format) {
	case format.FormatKEPUB:
		s.handleDownload(w, r)
		return
	case format.FormatEPUB:
		r.SetPathValue("target", "kepub")
		s.handleDownloadAs(w, r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) requireKoboAsset(w http.ResponseWriter, r *http.Request) (*db.KoboAsset, bool) {
	assetID, validID := pathID(w, r, "id")
	if !validID {
		return nil, false
	}
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return nil, false
	}
	asset, err := db.KoboAssetForConnection(s.db.Read(r.Context()), scope, koboConnectionID(r.Context()), assetID)
	if errors.Is(err, db.ErrAssetNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		serverError(w, r, err)
		return nil, false
	}
	return asset, true
}

func koboBaseURL(r *http.Request) string {
	path := "/kobo/" + url.PathEscape(r.PathValue("token"))
	return absoluteURL(r, path, nil)
}
