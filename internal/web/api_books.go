package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/converter"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
	"github.com/levmv/polka/internal/writeback"
)

// BookSummaryDTO is the list/cleanup shape; BookDetailDTO embeds it and adds
// the edition fields only the single-book view loads. The split mirrors the
// frontend's `BookSummary` / `Book extends BookSummary` and keeps detail-only
// fields off list rows that never selected them.
type BookSummaryDTO struct {
	ID             int64    `json:"id"`
	Title          string   `json:"title"`
	AuthorsList    []Author `json:"authors_list"`
	AuthorsDisplay string   `json:"authors_display"`
	Series         *string  `json:"series"`
	SeriesIndex    *float64 `json:"series_index"`
	Genres         *string  `json:"genres"`
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
// Available is true only when write-back is in manual mode and the book has at
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

func tagsDTO(tags []string) *string {
	if len(tags) == 0 {
		return nil
	}
	text := bookmeta.FormatTagList(tags)
	return &text
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
	ID                   int64              `json:"id"`
	Extension            string             `json:"extension"`
	Size                 int64              `json:"size,omitzero"`
	PageCount            int                `json:"page_count,omitzero"`
	PageCountApproximate bool               `json:"page_count_approximate"`
	IsPrimary            bool               `json:"is_primary"`
	CanRead              bool               `json:"can_read"`
	DownloadAs           []DownloadAsOption `json:"download_as,omitempty"`
}

type DownloadAsOption struct {
	Target string `json:"target"`
	Label  string `json:"label"`
}

func assetDTO(row db.AssetRow) Asset {
	return Asset{
		ID:                   row.ID,
		Extension:            row.Extension,
		Size:                 row.Size,
		PageCount:            row.PageCount,
		PageCountApproximate: format.IsPageCountApproximate(row.Format),
		IsPrimary:            row.IsPrimary,
		CanRead:              row.CanRead,
		DownloadAs:           downloadAsOptions(row.Format),
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
	ID    int64  `json:"id"`
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

// Bound list responses while allowing retained views to refresh in larger
// batches, without reevaluating the same search for every small browse page.
const (
	// Must allow REFRESH_PAGE_SIZE in frontend/src/views/library-view.ts;
	// the client treats a shorter response as the end of the list.
	maxBooksLimit    = 1000
	minBookJumpTotal = 500
)

func (s *Server) handleAPIBooks(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}

	q := r.URL.Query().Get("q")
	shelfID, validShelf := queryID(w, r, "shelf")
	if !validShelf {
		return
	}
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
	manualShelf := false
	if shelfID != 0 {
		shelf, gerr := db.GetShelfForUser(s.db.Read(r.Context()), shelfID, UserID(r.Context()))
		if errors.Is(gerr, db.ErrShelfNotFound) {
			http.Error(w, "Shelf not found", http.StatusNotFound)
			return
		}
		if gerr != nil {
			serverError(w, r, gerr)
			return
		}
		if shelf.Kind == db.ShelfQuery {
			q = shelf.Query
			// A query shelf is a saved search; default it to relevance order
			// unless the request explicitly asked for another sort.
			if sortParam == "" && shelf.Query != "" {
				sort = db.SortRelevance
			}
			bookRows, err = db.ListBooks(s.db.Read(r.Context()), scope, UserID(r.Context()), shelf.Query, sort, limit, offset)
		} else {
			q = ""
			manualShelf = true
			bookRows, err = db.ListBooksInManualShelf(s.db.Read(r.Context()), scope, shelf.ID, sort, limit, offset)
		}
	} else {
		bookRows, err = db.ListBooks(s.db.Read(r.Context()), scope, UserID(r.Context()), q, sort, limit, offset)
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	books, err := s.bookSummaryDTOs(r.Context(), bookRows)
	if err != nil {
		serverError(w, r, err)
		return
	}

	// Response metadata lets the retained catalog recognize safe in-place edits
	// without duplicating the search parser or making an additional request.
	w.Header().Set("X-Polka-List-Dependencies", strings.Join(db.BookListDependencies(scope, q, sort, manualShelf), ","))
	writeJSON(w, http.StatusOK, books)
}

func (s *Server) handleAPIBookJumps(w http.ResponseWriter, r *http.Request) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
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
	rows, total, err := db.ListBookJumps(s.db.Read(r.Context()), scope, sort)
	if err != nil {
		serverError(w, r, err)
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
	bookID, scope, ok := s.requireBookPathAccess(w, r, "id")
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
		shelfID, validShelf := queryID(w, r, "shelf")
		if !validShelf {
			return
		}
		sortParam := params.Get("sort")
		sort := bookSortFromParams(q, sortParam)
		if shelfID != 0 {
			shelf, gerr := db.GetShelfForUser(s.db.Read(r.Context()), shelfID, UserID(r.Context()))
			if errors.Is(gerr, db.ErrShelfNotFound) {
				http.Error(w, "Shelf not found", http.StatusNotFound)
				return
			}
			if gerr != nil {
				serverError(w, r, gerr)
				return
			}
			if shelf.Kind == db.ShelfQuery {
				if sortParam == "" && shelf.Query != "" {
					sort = db.SortRelevance
				}
				sequence, err = db.BookSequenceInList(s.db.Read(r.Context()), scope, UserID(r.Context()), bookID, shelf.Query, sort, before, after)
			} else {
				sequence, err = db.BookSequenceInManualShelf(s.db.Read(r.Context()), scope, bookID, shelf.ID, sort, before, after)
			}
		} else {
			sequence, err = db.BookSequenceInList(s.db.Read(r.Context()), scope, UserID(r.Context()), bookID, q, sort, before, after)
		}
	default:
		http.Error(w, "Missing or unsupported list context", http.StatusBadRequest)
		return
	}
	if err != nil {
		serverError(w, r, err)
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

// bookSummaryDTOs enriches every input row without changing its position.
func (s *Server) bookSummaryDTOs(ctx context.Context, bookRows []db.BookSummaryRow) ([]BookSummaryDTO, error) {
	books := make([]BookSummaryDTO, len(bookRows))
	bookIDs := make([]int64, len(bookRows))
	bookMap := make(map[int64]*BookSummaryDTO)

	for i, row := range bookRows {
		books[i] = summaryRowDTO(row)
		bookIDs[i] = row.ID
		bookMap[row.ID] = &books[i]
	}

	assetRows, err := db.AssetsByBookIDs(s.db.Read(ctx), bookIDs)
	if err != nil {
		return nil, err
	}
	for _, aRow := range assetRows {
		if b, ok := bookMap[aRow.BookID]; ok {
			b.Assets = append(b.Assets, assetDTO(aRow))
		}
	}

	authorsByBook, err := db.AuthorsByBookIDs(s.db.Read(ctx), bookIDs)
	if err != nil {
		return nil, err
	}
	tagsByBook, err := db.TagsByBookIDs(s.db.Read(ctx), bookIDs)
	if err != nil {
		return nil, err
	}
	for id, b := range bookMap {
		b.Genres = tagsDTO(tagsByBook[id].Genres)
		b.Tags = tagsDTO(tagsByBook[id].Tags)
		b.AuthorsList, b.AuthorsDisplay = authorsToDTO(authorsByBook[id])
	}

	return books, nil
}

// handleAPIBookDetail serves GET /api/books/{id}. PATCH routes to
// handleAPIBookEdit; the cover sub-path to handleAPICoverUpload.
func (s *Server) handleAPIBookDetail(w http.ResponseWriter, r *http.Request) {
	bookID, validID := pathID(w, r, "id")
	if !validID {
		return
	}
	s.writeBookDetail(w, r, bookID)
}

func (s *Server) writeBookDetail(w http.ResponseWriter, r *http.Request, bookID int64) {
	scope, err := s.visibilityScope(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	// Apply visibility in the root book query so missing and out-of-scope books
	// share the same 404 without fetching a forbidden row first.
	b, err := s.bookDetailDTO(r.Context(), scope, UserID(r.Context()), bookID, s.viewerIsAdmin(r))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Book not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, b)
}

func (s *Server) bookDetailDTO(ctx context.Context, scope db.VisibilityScope, viewerID int64, bookID int64, viewerIsAdmin bool) (BookDetailDTO, error) {
	queryer := s.db.Read(ctx)
	bRow, err := db.GetBook(queryer, scope, bookID)
	if err != nil {
		return BookDetailDTO{}, err
	}

	b := detailRowDTO(bRow)

	assetRows, err := db.AssetsByBookIDs(queryer, []int64{b.ID})
	if err != nil {
		return BookDetailDTO{}, err
	}
	for _, aRow := range assetRows {
		b.Assets = append(b.Assets, assetDTO(aRow))
	}

	authorsByBook, err := db.AuthorsByBookIDs(queryer, []int64{b.ID})
	if err != nil {
		return BookDetailDTO{}, err
	}
	b.AuthorsList, b.AuthorsDisplay = authorsToDTO(authorsByBook[b.ID])
	tagsByBook, err := db.TagsByBookIDs(queryer, []int64{b.ID})
	if err != nil {
		return BookDetailDTO{}, err
	}
	b.Genres = tagsDTO(tagsByBook[b.ID].Genres)
	b.Tags = tagsDTO(tagsByBook[b.ID].Tags)

	readingStatus := db.ReadingStatusState{BookID: b.ID, Status: db.ReadingStatusUnread}
	if viewerID > 0 {
		readingStatus, err = db.GetReadingStatus(queryer, viewerID, b.ID)
		if err != nil {
			return BookDetailDTO{}, err
		}
	}
	b.ReadingStatus = readingStatusDTO(readingStatus)

	wb, err := s.bookWritebackDTO(ctx, b.ID, viewerIsAdmin)
	if err != nil {
		return BookDetailDTO{}, err
	}
	b.Writeback = wb

	return b, nil
}

// bookWritebackDTO computes the write-back affordance for one book. It is an
// admin-only surface, so non-admins get no object at all (the field is omitted);
// gating on the viewer's role server-side keeps every render path (detail, edit
// save, cover, import) honest without the frontend re-deriving the role. For an
// admin the action is available in manual mode with at least one writable asset,
// and dirty when some writable asset is behind the catalog.
func (s *Server) bookWritebackDTO(ctx context.Context, bookID int64, viewerIsAdmin bool) (*BookWritebackDTO, error) {
	if !viewerIsAdmin {
		return nil, nil
	}
	state, err := db.GetBookWritebackState(s.db.Read(ctx), bookID)
	if err != nil {
		return nil, err
	}
	available := false
	if state.Writable > 0 {
		mode, err := writeback.OpenMode(s.db.Read(ctx))
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
