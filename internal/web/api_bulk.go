package web

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/relayout"
)

// bulkEditMaxIDs caps one bulk request. Loaded-page selection sits far below this;
// the cap just bounds a single transaction and guards against a pathological body.
const bulkEditMaxIDs = 1000

// bulkEditRequest is the body of PATCH /api/books/bulk: a set of works plus an
// ordered list of operations applied to each. Operations are deliberately typed,
// not a generic BookUpdate, so a bulk edit can never accidentally overwrite a
// field it did not mean to touch.
type bulkEditRequest struct {
	IDs        []string        `json:"ids"`
	Operations []bulkOperation `json:"operations"`
}

type bulkOperation struct {
	Type string `json:"type"`
	Mode string `json:"mode"`

	Values []string `json:"values"`

	Name  string           `json:"name"`
	Index *bulkSeriesIndex `json:"index"`

	// authors (semicolon-separated, same grammar as the single-book editor)
	Authors string `json:"authors"`
}

// bulkSeriesIndex controls series_index while setting a series. "keep" leaves the
// existing index, "clear" removes it, "assign" numbers the selection by its
// visible order (the request id order) as start + step*position.
type bulkSeriesIndex struct {
	Mode  string  `json:"mode"`
	Start float64 `json:"start"`
	Step  float64 `json:"step"`
}

type bulkEditResponse struct {
	Selected         int              `json:"selected"`
	Changed          int              `json:"changed"`
	Unchanged        int              `json:"unchanged"`
	RelayoutWarnings int              `json:"relayout_warnings"`
	Books            []BookSummaryDTO `json:"books"`
}

// bulkWritePlan is the resolved column set for one changed work.
type bulkWritePlan struct {
	tags      sql.NullString
	series    sql.NullString
	index     sql.NullFloat64
	overrides string
	// authors, when non-nil, replaces the work's authors with this
	// (semicolon-separated) list via replaceWorkAuthors inside the same tx.
	authors  *string
	relayout bool
}

func (s *Server) handleAPIBulkEdit(w http.ResponseWriter, r *http.Request) {
	var req bulkEditRequest
	if !readJSON(w, r, &req) {
		return
	}

	ids := dedupStrings(req.IDs)
	if len(ids) == 0 {
		http.Error(w, "no book ids provided", http.StatusBadRequest)
		return
	}
	if len(ids) > bulkEditMaxIDs {
		http.Error(w, fmt.Sprintf("too many books selected (max %d)", bulkEditMaxIDs), http.StatusBadRequest)
		return
	}
	if len(req.Operations) == 0 {
		http.Error(w, "no operations provided", http.StatusBadRequest)
		return
	}
	if err := validateBulkOperations(req.Operations); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}

	rows, err := db.BooksForBulkEdit(s.db, scope, ids)
	if err != nil {
		serverError(w, err)
		return
	}
	byID := make(map[string]db.BulkEditRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	// Authors live in their own table, so the current value each author op
	// compares against is loaded separately and passed into the plan.
	authorsByWork, err := db.AuthorsByWorkIDs(s.db, ids)
	if err != nil {
		serverError(w, err)
		return
	}

	// Walk ids in request (visible) order so "assign" numbering is stable and the
	// selection count is well defined. Works no longer visible are skipped.
	plans := make(map[string]bulkWritePlan)
	changedIDs := make([]string, 0, len(ids))
	selected := 0
	position := 0
	for _, id := range ids {
		row, ok := byID[id]
		if !ok {
			continue
		}
		selected++
		pos := position
		position++

		curAuthors := formatAuthorRows(authorsByWork[id])
		plan, changed := resolveBulkPlan(row, curAuthors, req.Operations, pos)
		if !changed {
			continue
		}
		plans[id] = plan
		changedIDs = append(changedIDs, id)
	}

	relayoutWarnings := 0
	if len(changedIDs) > 0 {
		releaseStorageSlot, err := s.acquireStorageWorkSlot(r.Context())
		if err != nil {
			serverError(w, err)
			return
		}
		defer releaseStorageSlot()

		mutation, err := relayout.MutateWorks(r.Context(), s.db, s.managedRoot(), func(tx *sql.Tx) (relayout.Changed, error) {
			pathIDs := make([]string, 0, len(changedIDs))
			for _, id := range changedIDs {
				p := plans[id]
				if _, err := tx.Exec(`
					UPDATE works SET
						tags = ?, series = ?, series_index = ?,
						manual_overrides = ?, updated_at = unixepoch()
					WHERE id = ?
				`, p.tags, p.series, p.index, p.overrides, id); err != nil {
					return relayout.Changed{}, fmt.Errorf("bulk update %s: %w", id, err)
				}
				// Re-link authors before reindexing so the search index picks up
				// the new author names in the same pass.
				if p.authors != nil {
					if err := replaceWorkAuthors(tx, id, *p.authors); err != nil {
						return relayout.Changed{}, fmt.Errorf("bulk authors %s: %w", id, err)
					}
				}
				if p.relayout {
					pathIDs = append(pathIDs, id)
				}
			}
			return relayout.Changed{BumpMetadataRev: changedIDs, Relayout: pathIDs}, nil
		})
		if err != nil {
			serverError(w, err)
			return
		}
		for _, warning := range mutation.Warnings {
			log.Printf("relayout after bulk edit: %v", warning)
		}
		relayoutWarnings = len(mutation.Warnings)
	}

	// Return summaries only for works that actually changed; unchanged selected
	// rows already match what the client rendered, so re-sending them would just
	// make the client rebuild identical DOM.
	summaryRows, err := db.BookSummaryRowsByIDs(s.db, scope, changedIDs)
	if err != nil {
		serverError(w, err)
		return
	}
	books, err := s.bookSummaryDTOs(orderSummaryRows(changedIDs, summaryRows))
	if err != nil {
		serverError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, bulkEditResponse{
		Selected:         selected,
		Changed:          len(changedIDs),
		Unchanged:        selected - len(changedIDs),
		RelayoutWarnings: relayoutWarnings,
		Books:            books,
	})
}

// bulkTrashRequest is the body of POST /api/books/bulk/trash: the selected works
// to move to Trash.
type bulkTrashRequest struct {
	IDs []string `json:"ids"`
}

type bulkTrashResponse struct {
	Trashed int      `json:"trashed"`
	IDs     []string `json:"ids"`
}

// handleAPIBulkTrash soft-deletes (moves to Trash) every selected work the caller
// can see. Like the single delete it is a catalog mutation (member/admin) that
// leaves files in place until an admin purges the trash; stale or out-of-scope
// ids are skipped rather than failing the batch.
func (s *Server) handleAPIBulkTrash(w http.ResponseWriter, r *http.Request) {
	u := contextUser(r.Context())
	var req bulkTrashRequest
	if !readJSON(w, r, &req) {
		return
	}
	ids := dedupStrings(req.IDs)
	if len(ids) == 0 {
		http.Error(w, "no book ids provided", http.StatusBadRequest)
		return
	}
	if len(ids) > bulkEditMaxIDs {
		http.Error(w, fmt.Sprintf("too many books selected (max %d)", bulkEditMaxIDs), http.StatusBadRequest)
		return
	}

	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	// BooksForBulkEdit returns only the live works visible in scope, so trashing
	// its result never touches an unknown, already-trashed, or hidden book.
	rows, err := db.BooksForBulkEdit(s.db, scope, ids)
	if err != nil {
		serverError(w, err)
		return
	}
	visible := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		visible[row.ID] = struct{}{}
	}
	trashed := make([]string, 0, len(rows))
	for _, id := range ids {
		if _, ok := visible[id]; ok {
			trashed = append(trashed, id)
		}
	}

	if len(trashed) > 0 {
		if err := s.db.Transact(r.Context(), func(tx *sql.Tx) error {
			for _, id := range trashed {
				if err := db.SoftDeleteWork(tx, id, u.ID); err != nil {
					return fmt.Errorf("bulk trash %s: %w", id, err)
				}
			}
			return nil
		}); err != nil {
			serverError(w, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, bulkTrashResponse{Trashed: len(trashed), IDs: trashed})
}

// resolveBulkPlan applies the operations to one work's current state and returns
// the columns to write plus whether anything actually changed. curAuthors is the
// work's current authors formatted with bookmeta.FormatAuthorList, so an author op
// compares like-for-like. pos is the work's zero-based position in the visible
// selection, used by "assign" numbering.
func resolveBulkPlan(row db.BulkEditRow, curAuthors string, ops []bulkOperation, pos int) (bulkWritePlan, bool) {
	overrides := bookmeta.ParseOverrides(row.Overrides.String)
	curTags := bookmeta.ParseTagList(row.Tags.String)
	newTags := curTags
	newSeries := row.Series
	newIndex := row.SeriesIndex
	newAuthors := curAuthors

	for _, op := range ops {
		switch op.Type {
		case "tags":
			newTags = bookmeta.ApplyTagMode(newTags, bookmeta.TagMode(op.Mode), op.Values)
		case "authors":
			// Only "set" (replace) — authors are required, so there is no clear.
			newAuthors = bookmeta.FormatAuthorList(bookmeta.ParseAuthorList(op.Authors))
		case "series":
			switch op.Mode {
			case "clear":
				newSeries = sql.NullString{}
				newIndex = sql.NullFloat64{}
			case "set":
				newSeries = sql.NullString{String: strings.TrimSpace(op.Name), Valid: true}
				if op.Index != nil {
					switch op.Index.Mode {
					case "clear":
						newIndex = sql.NullFloat64{}
					case "assign":
						newIndex = sql.NullFloat64{
							Float64: op.Index.Start + op.Index.Step*float64(pos),
							Valid:   true,
						}
					case "keep":
						// leave newIndex unchanged
					}
				}
			}
		}
	}

	tagsChanged := bookmeta.FormatTagList(newTags) != bookmeta.FormatTagList(curTags)
	seriesChanged := seriesString(newSeries) != seriesString(row.Series)
	authorsChanged := newAuthors != curAuthors

	curIdx, curHasIdx := effectiveIndex(row.SeriesIndex)
	newIdx, newHasIdx := effectiveIndex(newIndex)
	indexChanged := curHasIdx != newHasIdx || (newHasIdx && curIdx != newIdx)

	if !tagsChanged && !seriesChanged && !indexChanged && !authorsChanged {
		return bulkWritePlan{}, false
	}

	if tagsChanged {
		overrides["tags"] = true
	}
	if seriesChanged {
		overrides["series"] = true
	}
	if indexChanged {
		overrides["series_index"] = true
	}
	if authorsChanged {
		overrides["authors"] = true
	}

	var tagsCol sql.NullString
	if joined := bookmeta.FormatTagList(newTags); joined != "" {
		tagsCol = sql.NullString{String: joined, Valid: true}
	}
	var seriesCol sql.NullString
	if name := seriesString(newSeries); name != "" {
		seriesCol = sql.NullString{String: name, Valid: true}
	}
	var indexCol sql.NullFloat64
	if newHasIdx {
		indexCol = sql.NullFloat64{Float64: newIdx, Valid: true}
	}
	var authorsCol *string
	if authorsChanged {
		authorsCol = &newAuthors
	}

	return bulkWritePlan{
		tags:      tagsCol,
		series:    seriesCol,
		index:     indexCol,
		authors:   authorsCol,
		overrides: bookmeta.MarshalOverrides(overrides),
		// Authors are part of the default canonical path, and series/series_index
		// feed it when a storage template opts them in, so a change to any of them
		// must relayout the work.
		relayout: seriesChanged || indexChanged || authorsChanged,
	}, true
}

// formatAuthorRows renders a work's current authors in the editor grammar so an
// author op can compare its target against them like-for-like.
func formatAuthorRows(rows []db.AuthorRow) string {
	names := make([]string, 0, len(rows))
	for _, a := range rows {
		names = append(names, a.Name)
	}
	return bookmeta.FormatAuthorList(names)
}

// validateBulkOperations rejects malformed operations before any DB work so a bad
// request fails cleanly rather than silently no-op'ing per book.
func validateBulkOperations(ops []bulkOperation) error {
	for _, op := range ops {
		switch op.Type {
		case "tags":
			switch bookmeta.TagMode(op.Mode) {
			case bookmeta.TagAdd, bookmeta.TagRemove:
				if len(bookmeta.ParseTagList(strings.Join(op.Values, ","))) == 0 {
					return fmt.Errorf("tags %s requires at least one tag", op.Mode)
				}
			case bookmeta.TagReplace, bookmeta.TagClear:
				// replace with no values is an explicit clear; both are fine.
			default:
				return fmt.Errorf("unknown tags mode %q", op.Mode)
			}
		case "authors":
			switch op.Mode {
			case "set":
				if len(bookmeta.ParseAuthorList(op.Authors)) == 0 {
					return fmt.Errorf("authors set requires at least one author")
				}
			default:
				return fmt.Errorf("unknown authors mode %q", op.Mode)
			}
		case "series":
			switch op.Mode {
			case "set":
				if strings.TrimSpace(op.Name) == "" {
					return fmt.Errorf("series set requires a name")
				}
				if op.Index != nil {
					switch op.Index.Mode {
					case "keep", "clear", "assign":
					default:
						return fmt.Errorf("unknown series index mode %q", op.Index.Mode)
					}
				}
			case "clear":
			default:
				return fmt.Errorf("unknown series mode %q", op.Mode)
			}
		default:
			return fmt.Errorf("unknown operation type %q", op.Type)
		}
	}
	return nil
}

func seriesString(ns sql.NullString) string {
	if !ns.Valid {
		return ""
	}
	return strings.TrimSpace(ns.String)
}

// effectiveIndex canonicalizes series_index: a NULL or non-positive value both
// mean "no index" (matching the series ordering queries), so they compare equal.
func effectiveIndex(nf sql.NullFloat64) (float64, bool) {
	if nf.Valid && nf.Float64 > 0 {
		return nf.Float64, true
	}
	return 0, false
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func orderSummaryRows(ids []string, rows []db.BookSummaryRow) []db.BookSummaryRow {
	byID := make(map[string]db.BookSummaryRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	out := make([]db.BookSummaryRow, 0, len(rows))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out
}
