package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
)

// BookSummaryDTO is the list/cleanup shape; BookDetailDTO embeds it and adds
// the edition fields only the single-book view loads. The split mirrors the
// frontend's `BookSummary` / `Book extends BookSummary` and keeps detail-only
// fields off list rows that never selected them.
type BookSummaryDTO struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	AuthorsList    []Author `json:"authors_list"`
	AuthorsDisplay string   `json:"authors_display"`
	Series         *string  `json:"series"`
	SeriesIndex    *float64 `json:"series_index"`
	Tags           *string  `json:"tags"`
	Date           *string  `json:"date"`
	Year           string   `json:"year,omitempty"`
	HasCover       bool     `json:"has_cover"`
	// CoverVersion changes only when the cover image changes. Frontend includes
	// it in cover URLs for browser cache busting; /covers itself ignores it.
	CoverVersion int     `json:"cover_version"`
	Assets       []Asset `json:"assets"`
}

type BookDetailDTO struct {
	BookSummaryDTO
	SortTitle         string            `json:"sort_title,omitempty"`
	DescriptionSource *string           `json:"description_source"`
	DescriptionHTML   *string           `json:"description_html"`
	Language          *string           `json:"language"`
	LanguageName      string            `json:"language_name,omitempty"`
	Publisher         *string           `json:"publisher"`
	Identifiers       *string           `json:"identifiers"`
	DateHuman         string            `json:"date_human,omitempty"`
	AddedAt           int64             `json:"added_at,omitzero"`
	UpdatedAt         int64             `json:"updated_at,omitzero"`
	Writeback         *BookWritebackDTO `json:"writeback,omitzero"`
	ReadingStatus     ReadingStatusDTO  `json:"reading_status"`
}

// BookWritebackDTO drives the admin-only "Write metadata to file" action.
// Available is true only when write-back is in manual mode and the work has at
// least one writable asset (so the action renders); Dirty is true when some
// writable asset is behind the catalog (so it is enabled rather than "up to
// date"). The frontend additionally gates rendering on the admin role.
type BookWritebackDTO struct {
	Available bool `json:"available"`
	Dirty     bool `json:"dirty"`
}

type Author struct {
	Name     string `json:"name"`
	SortName string `json:"sort_name"`
	Role     string `json:"role,omitempty"`
}

// summaryRowDTO maps the columns shared by every books listing. Callers enrich
// AuthorsList/AuthorsDisplay and Assets separately (batch queries) where needed.
func summaryRowDTO(row db.BookSummaryRow) BookSummaryDTO {
	b := BookSummaryDTO{
		ID:           row.ID,
		Title:        row.Title,
		CoverVersion: row.CoverVersion,
		HasCover:     row.CoverVersion > 0,
		Assets:       []Asset{},
	}
	if row.Series.Valid {
		b.Series = &row.Series.String
	}
	if row.SeriesIndex.Valid {
		b.SeriesIndex = &row.SeriesIndex.Float64
	}
	if row.Tags.Valid {
		b.Tags = &row.Tags.String
	}
	if row.Date.Valid {
		b.Date = &row.Date.String
		b.Year = bookmeta.FormatYear(*b.Date)
	}
	return b
}

// detailRowDTO maps a full book record for the single-book view.
func detailRowDTO(row db.BookDetailRow) BookDetailDTO {
	b := BookDetailDTO{
		BookSummaryDTO: summaryRowDTO(row.BookSummaryRow),
		SortTitle:      row.SortTitle,
		AddedAt:        row.AddedAt,
		UpdatedAt:      row.UpdatedAt,
	}
	if row.Language.Valid {
		b.Language = &row.Language.String
		b.LanguageName = bookmeta.LanguageName(row.Language.String)
	}
	if row.Publisher.Valid {
		b.Publisher = &row.Publisher.String
	}
	if row.Description.Valid {
		b.DescriptionSource = &row.Description.String
		sanitized := SanitizeHTML(*b.DescriptionSource)
		b.DescriptionHTML = &sanitized
	}
	if row.Identifiers.Valid {
		b.Identifiers = &row.Identifiers.String
	}
	if row.Date.Valid {
		b.DateHuman = bookmeta.FormatDateHuman(row.Date.String)
	}
	return b
}

// authorsToDTO turns ordered author rows into the structured list and the
// display string. It uses " & " rather than a comma, which would be ambiguous
// for names like "Le Guin, Ursula K.".
func authorsToDTO(rows []db.AuthorRow) ([]Author, string) {
	list := make([]Author, 0, len(rows))
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		list = append(list, Author{Name: r.Name, SortName: r.SortName, Role: r.Role})
		names = append(names, r.Name)
	}
	return list, strings.Join(names, " & ")
}

type Asset struct {
	ID         string             `json:"id"`
	Extension  string             `json:"extension"`
	Size       int64              `json:"size,omitzero"`
	IsPrimary  bool               `json:"is_primary"`
	CanRead    bool               `json:"can_read"`
	DownloadAs []DownloadAsOption `json:"download_as,omitempty"`
}

type DownloadAsOption struct {
	Target string `json:"target"`
	Label  string `json:"label"`
}

func assetDTO(row db.AssetRow) Asset {
	return Asset{
		ID:         row.ID,
		Extension:  row.Extension,
		Size:       row.Size,
		IsPrimary:  row.IsPrimary,
		CanRead:    row.CanRead,
		DownloadAs: downloadAsOptions(row.Format),
	}
}

func downloadAsOptions(sourceFormat format.Format) []DownloadAsOption {
	specs := converter.TargetSpecsForFormat(sourceFormat)
	if len(specs) == 0 {
		return nil
	}

	options := make([]DownloadAsOption, 0, len(specs))
	for _, spec := range specs {
		options = append(options, DownloadAsOption{
			Target: string(spec.Target),
			Label:  spec.Label,
		})
	}
	return options
}

type BookSequenceItemDTO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type BookSequenceDTO struct {
	Items        []BookSequenceItemDTO `json:"items"`
	CurrentIndex int                   `json:"current_index"`
	Total        int                   `json:"total"`
}

type BookJumpsDTO struct {
	Items []BookJumpDTO `json:"items"`
	Total int           `json:"total"`
}

type BookJumpDTO struct {
	Label  string `json:"label"`
	Offset int    `json:"offset"`
}

func bookSortFromParams(q, sortParam string) db.BookSort {
	sort := db.SortAdded
	if q != "" {
		sort = db.SortRelevance
	}

	switch sortParam {
	case "added":
		sort = db.SortAdded
	case "title":
		sort = db.SortTitle
	case "author":
		sort = db.SortAuthor
	case "year":
		sort = db.SortYear
	case "series":
		sort = db.SortSeries
	case "relevance":
		if q != "" {
			sort = db.SortRelevance
		}
	}
	return sort
}

// maxBooksLimit caps ?limit on /api/books the way opdsMaxLimit does for OPDS,
// so a huge value can't force a scan where every row runs the correlated author
// subquery.
const (
	maxBooksLimit    = 200
	minBookJumpTotal = 500
)

func (s *Server) handleAPIBooks(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}

	q := r.URL.Query().Get("q")
	shelfID := r.URL.Query().Get("shelf")
	sortParam := r.URL.Query().Get("sort")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
		limit = min(l, maxBooksLimit)
	}

	offset := 0
	if o, err := strconv.Atoi(offsetStr); err == nil && o >= 0 {
		offset = o
	}

	sort := bookSortFromParams(q, sortParam)

	var bookRows []db.BookSummaryRow
	if shelfID != "" {
		// Note: assign with = (not :=) so we don't shadow the outer err that
		// the `if err != nil` below checks. A shadowed err would silently turn
		// a failed shelf query into an empty 200 response.
		shelf, gerr := s.db.GetShelfForUser(shelfID, UserID(r.Context()))
		if errors.Is(gerr, db.ErrShelfNotFound) {
			http.Error(w, "Shelf not found", http.StatusNotFound)
			return
		}
		if gerr != nil {
			serverError(w, gerr)
			return
		}
		if shelf.Kind == db.ShelfQuery {
			// A query shelf is a saved search; default it to relevance order
			// unless the request explicitly asked for another sort.
			if sortParam == "" && shelf.Query != "" {
				sort = db.SortRelevance
			}
			bookRows, err = db.ListBooks(s.db, scope, UserID(r.Context()), shelf.Query, sort, limit, offset)
		} else {
			bookRows, err = db.ListBooksInManualShelf(s.db, scope, shelf.ID, sort, limit, offset)
		}
	} else {
		bookRows, err = db.ListBooks(s.db, scope, UserID(r.Context()), q, sort, limit, offset)
	}
	if err != nil {
		serverError(w, err)
		return
	}

	books, err := s.bookSummaryDTOs(bookRows)
	if err != nil {
		serverError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, books)
}

func (s *Server) handleAPIBookJumps(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, err)
		return
	}
	var sort db.BookSort
	switch r.URL.Query().Get("sort") {
	case "title":
		sort = db.SortTitle
	case "author":
		sort = db.SortAuthor
	default:
		http.Error(w, "Book jumps require title or author sort", http.StatusBadRequest)
		return
	}
	rows, total, err := db.ListBookJumps(s.db, scope, sort)
	if err != nil {
		serverError(w, err)
		return
	}
	out := BookJumpsDTO{Items: []BookJumpDTO{}, Total: total}
	if total >= minBookJumpTotal {
		for _, row := range rows {
			out.Items = append(out.Items, BookJumpDTO{Label: row.Label, Offset: row.Offset})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIBookSequence(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	scope, ok := s.requireWorkAccess(w, r, workID)
	if !ok {
		return
	}

	params := r.URL.Query()
	before := sequenceWindowSize(params.Get("before"), 25)
	after := sequenceWindowSize(params.Get("after"), 25)

	var sequence db.BookSequenceWindow
	var err error
	switch params.Get("from") {
	case "library":
		q := params.Get("q")
		shelfID := params.Get("shelf")
		sortParam := params.Get("sort")
		sort := bookSortFromParams(q, sortParam)
		if shelfID != "" {
			shelf, gerr := s.db.GetShelfForUser(shelfID, UserID(r.Context()))
			if errors.Is(gerr, db.ErrShelfNotFound) {
				http.Error(w, "Shelf not found", http.StatusNotFound)
				return
			}
			if gerr != nil {
				serverError(w, gerr)
				return
			}
			if shelf.Kind == db.ShelfQuery {
				if sortParam == "" && shelf.Query != "" {
					sort = db.SortRelevance
				}
				sequence, err = db.BookSequenceInList(s.db, scope, UserID(r.Context()), workID, shelf.Query, sort, before, after)
			} else {
				sequence, err = db.BookSequenceInManualShelf(s.db, scope, workID, shelf.ID, sort, before, after)
			}
		} else {
			sequence, err = db.BookSequenceInList(s.db, scope, UserID(r.Context()), workID, q, sort, before, after)
		}
	default:
		http.Error(w, "Missing or unsupported list context", http.StatusBadRequest)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, bookSequenceDTO(sequence))
}

func sequenceWindowSize(raw string, fallback int) int {
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		if n > 100 {
			return 100
		}
		return n
	}
	return fallback
}

func bookSequenceDTO(sequence db.BookSequenceWindow) BookSequenceDTO {
	items := make([]BookSequenceItemDTO, 0, len(sequence.Items))
	for _, item := range sequence.Items {
		items = append(items, BookSequenceItemDTO{ID: item.ID, Title: item.Title})
	}
	return BookSequenceDTO{
		Items:        items,
		CurrentIndex: sequence.CurrentIndex,
		Total:        sequence.Total,
	}
}

func (s *Server) bookSummaryDTOs(bookRows []db.BookSummaryRow) ([]BookSummaryDTO, error) {
	var books []BookSummaryDTO
	var workIDs []string
	bookMap := make(map[string]*BookSummaryDTO)

	for _, bRow := range bookRows {
		books = append(books, summaryRowDTO(bRow))
		workIDs = append(workIDs, bRow.ID)
	}

	for i := range books {
		bookMap[books[i].ID] = &books[i]
	}

	assetRows, err := db.AssetsByWorkIDs(s.db, workIDs)
	if err != nil {
		return nil, err
	}
	for _, aRow := range assetRows {
		if b, ok := bookMap[aRow.WorkID]; ok {
			b.Assets = append(b.Assets, assetDTO(aRow))
		}
	}

	authorsByWork, err := db.AuthorsByWorkIDs(s.db, workIDs)
	if err != nil {
		return nil, err
	}
	for id, b := range bookMap {
		b.AuthorsList, b.AuthorsDisplay = authorsToDTO(authorsByWork[id])
	}

	if books == nil {
		books = []BookSummaryDTO{}
	}
	return books, nil
}

// handleAPIBookDetail serves GET /api/books/{id}. PATCH routes to
// handleAPIBookEdit; the cover sub-path to handleAPICoverUpload.
func (s *Server) handleAPIBookDetail(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	s.handleAPIBookDetailReturn(w, r, workID)
}

// handleAPIBookEdit serves PATCH /api/books/{id}.
func (s *Server) handleAPIBookEdit(w http.ResponseWriter, r *http.Request) {
	workID := r.PathValue("id")
	if _, ok := s.requireWorkAccess(w, r, workID); !ok {
		return
	}
	s.handleAPIEditBook(w, r, workID)
}
