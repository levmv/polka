package web

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/opds"
)

func (s *Server) handleOPDSProgression(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	assetID, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}
	var state *db.ReaderState
	var err error
	if r.Method == http.MethodPut {
		var document opds.Progression
		if !readJSON(w, r, &document) {
			return
		}
		if document.Progression == nil || len(document.Title) > 1024 || len(document.References) > 16 {
			http.Error(w, "Invalid progression document", http.StatusBadRequest)
			return
		}
		locator, parseErr := progressionLocator(document.References)
		if parseErr != nil {
			http.Error(w, "Invalid progression reference", http.StatusBadRequest)
			return
		}
		modified, parseErr := time.Parse(time.RFC3339Nano, document.Modified)
		if parseErr != nil {
			http.Error(w, "Invalid progression date", http.StatusBadRequest)
			return
		}

		var expected *int64
		if match := r.Header.Get("If-Match"); match != "" {
			if !strings.HasPrefix(match, "\"") || !strings.HasSuffix(match, "\"") {
				http.Error(w, "Invalid revision ETag", http.StatusBadRequest)
				return
			}
			value, err := strconv.ParseInt(strings.Trim(match, "\""), 10, 64)
			if err != nil || value < 0 {
				http.Error(w, "Invalid revision ETag", http.StatusBadRequest)
				return
			}
			expected = &value
		}
		state, err = s.db.SaveOPDSProgression(r.Context(), UserID(r.Context()), assetID, db.ReaderPositionWrite{
			Progress: *document.Progression,
			Locator:  locator,
			DeviceID: document.Device.ID, DeviceName: document.Device.Name,
		}, modified, expected)
	} else {
		state, err = db.GetReaderState(s.db.Read(r.Context()), UserID(r.Context()), assetID)
	}
	if err != nil {
		if errors.Is(err, db.ErrReadingConflict) {
			http.Error(w, "A more recent progression point is already available.", http.StatusConflict)
			return
		}
		writeReaderStateError(w, r, err)
		return
	}
	// Deliberately use ETag/If-Match as position revision tokens. Opening or
	// repeating a save can refresh modified without changing this token; it
	// therefore does not guarantee byte-identical progression documents.
	w.Header().Set("ETag", strconv.Quote(strconv.FormatInt(state.Revision, 10)))
	w.Header().Set("Content-Type", opds.ProgressionType)
	if state.DeviceID == "" {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.MarshalWrite(w, readerProgression(state))
}

func readerProgression(state *db.ReaderState) opds.Progression {
	var references []string
	if state.Locator.Page > 0 {
		references = []string{"#page=" + strconv.Itoa(state.Locator.Page)}
	} else {
		if state.Locator.CFI != "" {
			references = append(references, (&url.URL{Fragment: state.Locator.CFI}).String())
		}
		if state.Locator.Path != "" || state.Locator.Fragment != "" {
			references = append(references, (&url.URL{Path: state.Locator.Path, Fragment: state.Locator.Fragment}).String())
		}
	}
	return opds.Progression{Modified: time.Unix(state.UpdatedAt, 0).UTC().Format(time.RFC3339),
		Device:      opds.ProgressionDevice{ID: state.DeviceID, Name: state.DeviceName},
		Progression: new(state.Progress), References: references}
}

// Keep a relative resource and its fragment together, including fragments the
// web reader cannot resolve. It can still use the percentage fallback.
func progressionLocator(references []string) (db.Locator, error) {
	var locator db.Locator
	for _, reference := range references {
		u, err := url.Parse(reference)
		if err != nil || len(reference) > 4096 {
			return db.Locator{}, db.ErrInvalidLocator
		}
		// An address in another publication cannot identify this asset's content.
		if u.IsAbs() || u.Host != "" || strings.HasPrefix(u.Path, "/") || u.RawQuery != "" {
			continue
		}
		if u.Path != "" && locator.Path == "" {
			locator.Path = u.Path
			locator.Fragment = u.Fragment
		} else if locator.Path == "" && locator.Fragment == "" {
			locator.Fragment = u.Fragment
		}
		if strings.HasPrefix(u.Fragment, "epubcfi(") && strings.HasSuffix(u.Fragment, ")") && locator.CFI == "" {
			locator.CFI = u.Fragment
		}
		if strings.HasPrefix(u.Fragment, "page=") && locator.Page == 0 {
			if page, err := strconv.Atoi(strings.TrimPrefix(u.Fragment, "page=")); err == nil && page > 0 {
				locator.Page = page
			}
		}
	}
	// CFI is the more precise address when several references are supplied.
	if locator.CFI != "" {
		locator.Page = 0
		locator.Fragment = ""
	}
	if locator.Page > 0 {
		locator.Path = ""
		locator.Fragment = ""
	}
	return locator, nil
}

// Wrap authentication and cross-origin protection too: their errors must use
// the same protocol payloads as errors from the progression handler.
func opdsProgressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/opds/progression/") {
			next.ServeHTTP(w, r)
			return
		}
		response := &progressionResponse{ResponseWriter: w}
		next.ServeHTTP(response, r)
		response.finish(r)
	})
}

type progressionResponse struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *progressionResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status < 400 {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *progressionResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status >= 400 {
		if w.body.Len() < 4096 {
			_, _ = w.body.Write(data[:min(len(data), 4096-w.body.Len())])
		}
		return len(data), nil
	}
	return w.ResponseWriter.Write(data)
}
func (w *progressionResponse) finish(r *http.Request) {
	if w.status < 400 {
		return
	}
	w.Header().Del("Content-Length")
	w.Header().Set("Cache-Control", "private, no-store")
	if w.status == http.StatusUnauthorized {
		w.Header().Set("Content-Type", "application/opds-authentication+json")
		w.ResponseWriter.WriteHeader(w.status)
		_ = json.MarshalWrite(w.ResponseWriter, map[string]any{
			"id": absoluteURL(r, "/opds", nil), "title": "polka",
			"authentication": []any{map[string]any{"type": "http://opds-spec.org/auth/basic"}},
		})
		return
	}
	kind := "about:blank"
	switch w.status {
	case 400:
		kind = "https://registry.opds.io/error#progression-invalid-payload"
	case 403:
		kind = "https://registry.opds.io/error#progression-incorrect-user"
	case 409:
		kind = "https://registry.opds.io/error#progression-date"
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.ResponseWriter.WriteHeader(w.status)
	_ = json.MarshalWrite(w.ResponseWriter, map[string]any{"type": kind, "title": http.StatusText(w.status), "detail": strings.TrimSpace(w.body.String()), "status": w.status})
}
