package web

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json/jsontext"
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
	after, err := parseKoboCursor(r.Header.Get("X-Kobo-Synctoken"))
	if err != nil {
		http.Error(w, "Invalid sync token", http.StatusBadRequest)
		return
	}
	connectionID := koboConnectionID(r.Context())
	changes, currentRevision, databaseMore, err := s.db.SyncKoboConnection(
		r.Context(), connectionID, after, db.KoboSyncPageLimit,
	)
	if errors.Is(err, db.ErrKoboInvalidCursor) {
		http.Error(w, "Invalid sync token", http.StatusBadRequest)
		return
	}
	if errors.Is(err, db.ErrKoboConnectionNotFound) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	body, nextRevision, more, err := marshalKoboSyncPage(
		changes, after, koboBaseURL(r), currentRevision, databaseMore,
	)
	if err != nil {
		serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Kobo-Synctoken", strconv.FormatInt(nextRevision, 10))
	if more {
		w.Header().Set("X-Kobo-Sync", "continue")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func marshalKoboSyncPage(changes []db.KoboChange, after int64, base string, currentRevision int64, databaseMore bool) ([]byte, int64, bool, error) {
	items := make([]jsontext.Value, 0, len(changes))
	responseBytes := 2 // []
	nextRevision := currentRevision
	for _, change := range changes {
		wire := kobowire.BuildSyncItem(mapKoboChange(change), after, base)
		encoded, err := json.Marshal(wire)
		if err != nil {
			return nil, 0, false, err
		}
		separator := 0
		if len(items) > 0 {
			separator = 1
		}
		if len(items) > 0 && responseBytes+separator+len(encoded) > maxKoboSyncResponseBytes {
			break
		}
		items = append(items, jsontext.Value(encoded))
		responseBytes += separator + len(encoded)
		nextRevision = change.Revision
	}
	if len(changes) == 0 {
		nextRevision = currentRevision
	}
	body, err := json.Marshal(items)
	return body, nextRevision, databaseMore || len(items) < len(changes), err
}

func parseKoboCursor(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cursor < 0 {
		return 0, db.ErrKoboInvalidCursor
	}
	return cursor, nil
}

func mapKoboPublication(publication db.KoboPublication) kobowire.Publication {
	var seriesIndex *float64
	if publication.SeriesIndex.Valid {
		value := publication.SeriesIndex.Float64
		seriesIndex = &value
	}
	return kobowire.Publication{
		AssetID:       publication.AssetID,
		WorkID:        publication.WorkID,
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
	}
}

func mapKoboChange(change db.KoboChange) kobowire.Change {
	return kobowire.Change{
		Publication:   mapKoboPublication(change.KoboPublication),
		Revision:      change.Revision,
		FirstRevision: change.FirstRevision,
		Present:       change.Present,
		ChangedAt:     change.ChangedAt,
	}
}

func (s *Server) handleKoboMetadata(w http.ResponseWriter, r *http.Request) {
	publication, ok := s.requireKoboPublication(w, r)
	if !ok {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, publication.AssetID); !ok {
		return
	}
	metadata := kobowire.BuildMetadata(mapKoboPublication(*publication), koboBaseURL(r))
	writeJSON(w, http.StatusOK, []kobowire.Metadata{metadata})
}

// The connection projection is the first gate for Kobo content. Cover and
// download then delegate to the ordinary handlers so a later account-scope
// change is enforced too, alongside the shared serving and conversion rules.
func (s *Server) handleKoboCover(w http.ResponseWriter, r *http.Request) {
	publication, ok := s.requireKoboPublication(w, r)
	if !ok {
		return
	}
	r.SetPathValue("id", publication.WorkID)
	height, _ := strconv.Atoi(r.PathValue("height"))
	query := r.URL.Query()
	if height > 0 && height <= 500 {
		query.Set("variant", "thumb")
	} else {
		query.Set("variant", "display")
	}
	r.URL.RawQuery = query.Encode()
	s.handleCover(w, r)
}

func (s *Server) handleKoboDownload(w http.ResponseWriter, r *http.Request) {
	publication, ok := s.requireKoboPublication(w, r)
	if !ok {
		return
	}
	if !strings.EqualFold(r.PathValue("format"), "kepub") {
		http.NotFound(w, r)
		return
	}
	r.SetPathValue("id", publication.AssetID)
	switch format.FormatFromKey(publication.Format) {
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

func (s *Server) requireKoboPublication(w http.ResponseWriter, r *http.Request) (*db.KoboPublication, bool) {
	connectionID := koboConnectionID(r.Context())
	assetID := r.PathValue("id")
	if connectionID == "" || assetID == "" {
		http.NotFound(w, r)
		return nil, false
	}
	publication, err := s.db.KoboPublicationForAsset(connectionID, assetID)
	if errors.Is(err, db.ErrKoboConnectionNotFound) || errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		serverError(w, err)
		return nil, false
	}
	return publication, true
}

func koboBaseURL(r *http.Request) string {
	path := "/kobo/" + url.PathEscape(r.PathValue("token"))
	return absoluteURL(r, path, nil)
}
