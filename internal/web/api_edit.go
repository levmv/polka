package web

import (
	"bytes"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/relayout"
	"github.com/levmv/polka/internal/writeback"
)

// patchValue distinguishes an omitted JSON field from an explicit null. That
// distinction is the PATCH contract: omitted fields keep the transaction's
// current value, while null clears a nullable field.
type patchValue[T any] struct {
	Present bool
	Null    bool
	Value   T
}

func (v *patchValue[T]) UnmarshalJSON(data []byte) error {
	v.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		v.Null = true
		var zero T
		v.Value = zero
		return nil
	}
	v.Null = false
	return json.Unmarshal(data, &v.Value)
}

type BookPatch struct {
	Title       patchValue[string]  `json:"title"`
	SortTitle   patchValue[string]  `json:"sort_title"`
	Authors     patchValue[string]  `json:"authors"`
	Series      patchValue[string]  `json:"series"`
	SeriesIndex patchValue[float64] `json:"series_index"`
	Description patchValue[string]  `json:"description"`
	Tags        patchValue[string]  `json:"tags"`
	Language    patchValue[string]  `json:"language"`
	Publisher   patchValue[string]  `json:"publisher"`
	Date        patchValue[string]  `json:"date"`
	Identifiers patchValue[string]  `json:"identifiers"`
}

type bookEditState struct {
	Title        string
	SortTitle    string
	Authors      string
	Series       sql.NullString
	SeriesIndex  sql.NullFloat64
	Description  sql.NullString
	Tags         sql.NullString
	OverridesStr string
	Language     sql.NullString
	Publisher    sql.NullString
	Date         sql.NullString
	Identifiers  sql.NullString
}

func loadBookEditState(queryer db.Queryer, workID string) (bookEditState, error) {
	b, err := db.GetBook(queryer, db.FullVisibilityScope(), workID)
	if err != nil {
		return bookEditState{}, err
	}
	authorsByWork, err := db.AuthorsByWorkIDs(queryer, []string{workID})
	if err != nil {
		return bookEditState{}, err
	}
	authorNames := make([]string, 0, len(authorsByWork[workID]))
	for _, a := range authorsByWork[workID] {
		authorNames = append(authorNames, a.Name)
	}

	var existingOverrides sql.NullString
	if err := queryer.QueryRow("SELECT manual_overrides FROM works WHERE id = ?", workID).Scan(&existingOverrides); err != nil {
		return bookEditState{}, err
	}

	return bookEditState{
		Title:        b.Title,
		SortTitle:    b.SortTitle,
		Authors:      bookmeta.FormatAuthorList(authorNames),
		Series:       b.Series,
		SeriesIndex:  b.SeriesIndex,
		Description:  b.Description,
		Tags:         b.Tags,
		OverridesStr: existingOverrides.String,
		Language:     b.Language,
		Publisher:    b.Publisher,
		Date:         b.Date,
		Identifiers:  b.Identifiers,
	}, nil
}

func nullableText(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func trimmedNullableText(value string) sql.NullString {
	return nullableText(strings.TrimSpace(value))
}

func sameNullableText(a, b sql.NullString) bool {
	return a.Valid == b.Valid && (!a.Valid || a.String == b.String)
}

func sameNullableNumber(a, b sql.NullFloat64) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Float64 == b.Float64)
}

func normalizedAuthors(value string) string {
	names := bookmeta.ParseAuthorList(value)
	if len(names) == 0 {
		return "Unknown Author"
	}
	return bookmeta.FormatAuthorList(names)
}

func replaceWorkAuthors(tx *sql.Tx, workID string, authorsStr string) error {
	var parsedAuthors []bookmeta.AuthorMeta
	for _, n := range bookmeta.ParseAuthorList(authorsStr) {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		parsedAuthors = append(parsedAuthors, bookmeta.AuthorMeta{
			Name:     n,
			SortName: bookmeta.AuthorSort(n),
		})
	}

	if len(parsedAuthors) == 0 {
		parsedAuthors = append(parsedAuthors, bookmeta.AuthorMeta{
			Name:     "Unknown Author",
			SortName: bookmeta.AuthorSort("Unknown Author"),
		})
	}

	// Shared with import: find-or-insert each author (adopting an existing row's
	// sort_name, which the canonical path buckets on) and re-link work_authors.
	if _, _, err := db.UpsertWorkAuthors(tx, workID, parsedAuthors); err != nil {
		return err
	}

	// Re-linking to a different spelling can orphan the previous authors row;
	// sweep any author no longer referenced by a work.
	if _, err := db.DeleteOrphanAuthors(tx); err != nil {
		return err
	}

	return nil
}

func (s *Server) handleAPIEditBook(w http.ResponseWriter, r *http.Request, workID string) {
	var req BookPatch
	if !readJSON(w, r, &req) {
		return
	}

	if req.Title.Present {
		req.Title.Value = strings.TrimSpace(req.Title.Value)
	}
	if req.Title.Present && (req.Title.Null || req.Title.Value == "") {
		http.Error(w, "title cannot be empty", http.StatusBadRequest)
		return
	}

	releaseStorageSlot, err := s.acquireStorageWorkSlot(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	defer releaseStorageSlot()

	mutation, err := relayout.MutateWorks(r.Context(), s.db, s.managedRoot(), func(tx *sql.Tx) (relayout.Changed, error) {
		existing, err := loadBookEditState(tx, workID)
		if err != nil {
			return relayout.Changed{}, err
		}

		next := existing
		overrides := bookmeta.ParseOverrides(existing.OverridesStr)
		beforeOverrides := bookmeta.MarshalOverrides(overrides)

		if req.Title.Present {
			next.Title = req.Title.Value
			overrides["title"] = true
		}
		titleChanged := next.Title != existing.Title

		if req.SortTitle.Present {
			sortTitle := strings.TrimSpace(req.SortTitle.Value)
			if req.SortTitle.Null || sortTitle == "" {
				next.SortTitle = next.Title
				delete(overrides, "sort_title")
			} else {
				next.SortTitle = sortTitle
				overrides["sort_title"] = true
			}
		} else if titleChanged && existing.SortTitle == existing.Title {
			// Equality is the persisted "sort title follows title" state. A title-only
			// patch keeps following without requiring the stale form to send sort_title.
			next.SortTitle = next.Title
		}

		if req.Series.Present {
			next.Series = sql.NullString{}
			if !req.Series.Null {
				next.Series = trimmedNullableText(req.Series.Value)
			}
			overrides["series"] = true
		}
		if req.SeriesIndex.Present {
			next.SeriesIndex = sql.NullFloat64{}
			if !req.SeriesIndex.Null {
				next.SeriesIndex = sql.NullFloat64{Float64: req.SeriesIndex.Value, Valid: true}
			}
			overrides["series_index"] = true
		}
		if req.Description.Present {
			next.Description = sql.NullString{}
			if !req.Description.Null {
				next.Description = nullableText(req.Description.Value)
			}
			overrides["description"] = true
		}
		if req.Tags.Present {
			next.Tags = sql.NullString{}
			if !req.Tags.Null {
				next.Tags = trimmedNullableText(req.Tags.Value)
			}
			overrides["tags"] = true
		}
		if req.Language.Present {
			next.Language = sql.NullString{}
			if !req.Language.Null {
				// Match import normalization so "eng", "en_US", and "en" converge.
				next.Language = trimmedNullableText(bookmeta.NormalizeLanguage(req.Language.Value))
			}
			overrides["language"] = true
		}
		if req.Publisher.Present {
			next.Publisher = sql.NullString{}
			if !req.Publisher.Null {
				next.Publisher = trimmedNullableText(req.Publisher.Value)
			}
			overrides["publisher"] = true
		}
		if req.Date.Present {
			next.Date = sql.NullString{}
			if !req.Date.Null {
				date := strings.TrimSpace(req.Date.Value)
				if normalized, _ := bookmeta.ParseDate(date); normalized != "" {
					date = normalized
				}
				next.Date = nullableText(date)
			}
			overrides["date"] = true
		}
		if req.Identifiers.Present {
			next.Identifiers = sql.NullString{}
			if !req.Identifiers.Null {
				formatted := bookmeta.FormatIdentifiers(bookmeta.ParseIdentifiers(req.Identifiers.Value))
				next.Identifiers = nullableText(formatted)
			}
			overrides["identifiers"] = true
		}

		authorsChanged := false
		nextAuthors := existing.Authors
		if req.Authors.Present {
			nextAuthors = "Unknown Author"
			if !req.Authors.Null {
				nextAuthors = normalizedAuthors(req.Authors.Value)
			}
			authorsChanged = nextAuthors != existing.Authors
			overrides["authors"] = true
		}

		sortTitleChanged := next.SortTitle != existing.SortTitle
		seriesChanged := !sameNullableText(next.Series, existing.Series)
		seriesIndexChanged := !sameNullableNumber(next.SeriesIndex, existing.SeriesIndex)
		pathInputsChanged := titleChanged || sortTitleChanged || seriesChanged || seriesIndexChanged || authorsChanged
		metadataChanged := pathInputsChanged ||
			!sameNullableText(next.Description, existing.Description) ||
			!sameNullableText(next.Tags, existing.Tags) ||
			!sameNullableText(next.Language, existing.Language) ||
			!sameNullableText(next.Publisher, existing.Publisher) ||
			!sameNullableText(next.Date, existing.Date) ||
			!sameNullableText(next.Identifiers, existing.Identifiers)
		overridesJSON := bookmeta.MarshalOverrides(overrides)
		overridesChanged := overridesJSON != beforeOverrides

		if !metadataChanged && !overridesChanged {
			return relayout.Changed{}, nil
		}

		_, err = tx.Exec(`
			UPDATE works SET
				title = ?, sort_title = ?, series = ?, series_index = ?,
				description = ?, tags = ?, manual_overrides = ?,
				language = ?, publisher = ?, published_date = ?, identifiers = ?,
				updated_at = unixepoch()
			WHERE id = ?
		`, next.Title, next.SortTitle, next.Series, next.SeriesIndex, next.Description, next.Tags, overridesJSON,
			next.Language, next.Publisher, next.Date, next.Identifiers, workID)
		if err != nil {
			return relayout.Changed{}, fmt.Errorf("update work: %w", err)
		}

		if authorsChanged {
			if err := replaceWorkAuthors(tx, workID, nextAuthors); err != nil {
				return relayout.Changed{}, fmt.Errorf("replace work authors: %w", err)
			}
		}

		changed := relayout.Changed{}
		if metadataChanged {
			changed.BumpMetadataRev = []string{workID}
		}
		if pathInputsChanged {
			changed.Relayout = []string{workID}
		}
		return changed, nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		serverError(w, err)
		return
	}
	for _, warning := range mutation.Warnings {
		log.Printf("relayout after edit of %s: %v", workID, warning)
	}

	s.handleAPIBookDetailReturn(w, r, workID)
}

func (s *Server) handleAPIBookDetailReturn(w http.ResponseWriter, r *http.Request, workID string) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	// Apply visibility in the root book query so missing and out-of-scope works
	// share the same 404 without fetching a forbidden row first.
	b, err := s.bookDetailDTO(scope, UserID(r.Context()), workID, s.viewerIsAdmin(r))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Book not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, b)
}

func (s *Server) bookDetailDTO(scope db.VisibilityScope, viewerID, workID string, viewerIsAdmin bool) (BookDetailDTO, error) {
	bRow, err := db.GetBook(s.db, scope, workID)
	if err != nil {
		return BookDetailDTO{}, err
	}

	b := detailRowDTO(bRow)

	assetRows, err := db.AssetsByWorkIDs(s.db, []string{b.ID})
	if err != nil {
		return BookDetailDTO{}, err
	}
	for _, aRow := range assetRows {
		b.Assets = append(b.Assets, assetDTO(aRow))
	}

	authorsByWork, err := db.AuthorsByWorkIDs(s.db, []string{b.ID})
	if err != nil {
		return BookDetailDTO{}, err
	}
	b.AuthorsList, b.AuthorsDisplay = authorsToDTO(authorsByWork[b.ID])

	readingStatus := db.ReadingStatusState{WorkID: b.ID, Status: db.ReadingStatusUnread}
	if viewerID != "" {
		readingStatus, err = db.GetReadingStatus(s.db, viewerID, b.ID)
		if err != nil {
			return BookDetailDTO{}, err
		}
	}
	b.ReadingStatus = readingStatusDTO(readingStatus)

	wb, err := s.bookWritebackDTO(b.ID, viewerIsAdmin)
	if err != nil {
		return BookDetailDTO{}, err
	}
	b.Writeback = wb

	return b, nil
}

// bookWritebackDTO computes the write-back affordance for one work. It is an
// admin-only surface, so non-admins get no object at all (the field is omitted);
// gating on the viewer's role server-side keeps every render path (detail, edit
// save, cover, import) honest without the frontend re-deriving the role. For an
// admin the action is available in manual mode with at least one writable asset,
// and dirty when some writable asset is behind the catalog.
func (s *Server) bookWritebackDTO(workID string, viewerIsAdmin bool) (*BookWritebackDTO, error) {
	if !viewerIsAdmin {
		return nil, nil
	}
	state, err := db.GetWorkWritebackState(s.db, workID)
	if err != nil {
		return nil, err
	}
	available := false
	if state.Writable > 0 {
		mode, err := writeback.OpenMode(s.db.DB)
		if err != nil {
			return nil, err
		}
		available = mode == writeback.ModeManual
	}
	return &BookWritebackDTO{
		Available: available,
		Dirty:     state.Dirty > 0,
	}, nil
}
