package web

import (
	"bytes"
	"database/sql"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/levmv/polka/internal/bookmeta"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestAPIEditFields(t *testing.T) {
	dataDir := t.TempDir()

	// Init DB
	dbPath := filepath.Join(dataDir, "library.db")
	database, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db init: %v", err)
	}

	bookID := int64(174)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, 'Title', 'Title')", bookID)

	srv := &Server{
		db:      database,
		dataDir: dataDir,
	}

	lang := "en"
	pub := "Penguin"
	date := "2024"
	reqBody, _ := json.Marshal(map[string]any{
		"title":     "New Title",
		"authors":   "New Author",
		"language":  &lang,
		"publisher": &pub,
		"date":      &date,
	})

	req := httptest.NewRequest("PATCH", "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()

	// Bypass auth by calling handler directly
	srv.handleAPIEditBook(rr, req, bookID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	defer database.Close()

	var sortTitle string
	var metadataRev int
	var newLang, newPub, newDate sql.NullString
	err = database.Read(req.Context()).QueryRow("SELECT sort_title, language, publisher, published_date, metadata_rev FROM books WHERE id = ?", bookID).Scan(&sortTitle, &newLang, &newPub, &newDate, &metadataRev)
	if err != nil {
		t.Fatalf("query books: %v", err)
	}

	if sortTitle != "New Title" {
		t.Errorf("sort_title = %q; want automatic New Title", sortTitle)
	}
	if newLang.String != "en" || newPub.String != "Penguin" || newDate.String != "2024" {
		t.Errorf("fields not persisted correctly: %s, %s, %s", newLang.String, newPub.String, newDate.String)
	}
	if metadataRev != 1 {
		t.Errorf("metadata_rev = %d; want 1", metadataRev)
	}

	var manualOverrides sql.NullString
	err = database.Read(req.Context()).QueryRow("SELECT manual_overrides FROM books WHERE id = ?", bookID).Scan(&manualOverrides)
	if err != nil {
		t.Fatalf("query books: %v", err)
	}

	overrides := make(map[string]bool)
	json.Unmarshal([]byte(manualOverrides.String), &overrides)
	if !overrides["title"] || !overrides["language"] || !overrides["publisher"] || !overrides["date"] {
		t.Errorf("overrides not tracked correctly: %v", overrides)
	}
	if overrides["sort_title"] {
		t.Errorf("implicit sort_title follow should not be a manual override: %v", overrides)
	}
}

func TestAPIEditPatchMergesStaleFieldsAndClearsNulls(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	bookID := int64(164)
	mustExec(t, database, `
		INSERT INTO books (
			id, title, sort_title, series, series_index, language, publisher, published_date
		) VALUES (?, 'Old Title', 'Old Title', 'Old Series', 2, 'fr', 'Old Press', '1942')
	`, bookID)
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Old Author', 'Author, Old')")
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", bookID)

	srv := &Server{db: database, dataDir: dataDir}
	patch := func(body map[string]any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal patch: %v", err)
		}
		req := httptest.NewRequest(http.MethodPatch, "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewReader(raw))
		rr := httptest.NewRecorder()
		srv.handleAPIEditBook(rr, req, bookID)
		if rr.Code != http.StatusOK {
			t.Fatalf("patch %s: status %d: %s", raw, rr.Code, rr.Body.String())
		}
	}

	// These represent two forms opened from the same old state. Because each
	// sends only its dirty field, the later publisher save preserves the title
	// that was committed after that form opened.
	patch(map[string]any{"title": "New Title"})
	patch(map[string]any{"publisher": "New Press"})

	var title, sortTitle string
	var publisher sql.NullString
	if err := database.Read(t.Context()).QueryRow("SELECT title, sort_title, publisher FROM books WHERE id = ?", bookID).Scan(&title, &sortTitle, &publisher); err != nil {
		t.Fatalf("query merged book: %v", err)
	}
	if title != "New Title" || sortTitle != "New Title" || publisher.String != "New Press" {
		t.Fatalf("merged title/sort/publisher = %q/%q/%q", title, sortTitle, publisher.String)
	}

	patch(map[string]any{
		"sort_title":   nil,
		"authors":      nil,
		"series":       nil,
		"series_index": nil,
		"publisher":    nil,
	})

	var series, language, date sql.NullString
	var seriesIndex sql.NullFloat64
	if err := database.Read(t.Context()).QueryRow(`
		SELECT sort_title, series, series_index, language, publisher, published_date
		FROM books WHERE id = ?
	`, bookID).Scan(&sortTitle, &series, &seriesIndex, &language, &publisher, &date); err != nil {
		t.Fatalf("query cleared book: %v", err)
	}
	if sortTitle != "New Title" {
		t.Fatalf("sort_title = %q; want automatic New Title", sortTitle)
	}
	if series.Valid || seriesIndex.Valid || publisher.Valid {
		t.Fatalf("nullable fields were not cleared: series=%+v index=%+v publisher=%+v", series, seriesIndex, publisher)
	}
	if !language.Valid || language.String != "fr" {
		t.Fatalf("omitted language = %+v; want preserved fr", language)
	}
	if !date.Valid || date.String != "1942" {
		t.Fatalf("omitted date = %+v; want preserved 1942", date)
	}

	authors, err := db.AuthorsByBookIDs(database.Read(t.Context()), []int64{bookID})
	if err != nil {
		t.Fatalf("authors: %v", err)
	}
	if got := authors[bookID]; len(got) != 0 {
		t.Fatalf("authors after null = %#v; want no authors", got)
	}

	var rawOverrides string
	if err := database.Read(t.Context()).QueryRow("SELECT manual_overrides FROM books WHERE id = ?", bookID).Scan(&rawOverrides); err != nil {
		t.Fatalf("query overrides: %v", err)
	}
	overrides := bookmeta.ParseOverrides(rawOverrides)
	for _, field := range []string{"title", "publisher", "authors", "series", "series_index"} {
		if !overrides[field] {
			t.Errorf("manual override %q missing from %v", field, overrides)
		}
	}
	if overrides["sort_title"] {
		t.Errorf("cleared sort_title stayed a manual override: %v", overrides)
	}
}

func TestAPIEditExplicitNullRecordsManualClear(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	bookID := int64(149)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, 'Title', 'Title')", bookID)

	srv := &Server{db: database, dataDir: dataDir}
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewBufferString(`{"description":null}`))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, bookID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}

	var rawOverrides string
	var metadataRev int
	if err := database.Read(req.Context()).QueryRow("SELECT manual_overrides, metadata_rev FROM books WHERE id = ?", bookID).Scan(&rawOverrides, &metadataRev); err != nil {
		t.Fatalf("query book: %v", err)
	}
	if !bookmeta.ParseOverrides(rawOverrides)["description"] {
		t.Fatalf("explicit clear did not protect description: %s", rawOverrides)
	}
	if metadataRev != 0 {
		t.Fatalf("metadata_rev = %d; clearing an already-empty field changed no file metadata", metadataRev)
	}
}

func TestAPIEditRejectsNullTitle(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	bookID := int64(159)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, 'Title', 'Title')", bookID)

	srv := &Server{db: database, dataDir: dataDir}
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewBufferString(`{"title":null}`))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, bookID)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestAPIEditSortTitleBehavior(t *testing.T) {
	for _, tt := range []struct {
		name          string
		initialTitle  string
		initialSort   string
		patch         map[string]any
		wantTitle     string
		wantSortTitle string
	}{
		{
			name:          "preserve custom sort title on unrelated edit",
			initialTitle:  "The Book",
			initialSort:   "Book, The",
			patch:         map[string]any{"title": "The Book", "tags": "new"},
			wantTitle:     "The Book",
			wantSortTitle: "Book, The",
		},
		{
			name:          "apply explicit sort title",
			initialTitle:  "The Old Book",
			initialSort:   "Old Book, The",
			patch:         map[string]any{"title": "The New Book", "sort_title": "New Book, The"},
			wantTitle:     "The New Book",
			wantSortTitle: "New Book, The",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
			if err != nil {
				t.Fatalf("db init: %v", err)
			}
			defer database.Close()

			const bookID = 171
			mustExec(t, database, "INSERT INTO books (id, title, sort_title, tags) VALUES (?, ?, ?, 'old')",
				bookID, tt.initialTitle, tt.initialSort)

			reqBody, err := json.Marshal(tt.patch)
			if err != nil {
				t.Fatalf("marshal patch: %v", err)
			}
			req := httptest.NewRequest(http.MethodPatch, "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewReader(reqBody))
			rr := httptest.NewRecorder()
			(&Server{db: database, dataDir: dataDir}).handleAPIEditBook(rr, req, bookID)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200: %s", rr.Code, rr.Body.String())
			}

			var title, sortTitle string
			if err := database.Read(req.Context()).QueryRow("SELECT title, sort_title FROM books WHERE id = ?", bookID).Scan(&title, &sortTitle); err != nil {
				t.Fatalf("query book: %v", err)
			}
			if title != tt.wantTitle || sortTitle != tt.wantSortTitle {
				t.Fatalf("title/sort_title = %q/%q; want %q/%q", title, sortTitle, tt.wantTitle, tt.wantSortTitle)
			}
		})
	}
}

func TestAPIEditAuthorsKeepCommasInsideNames(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	bookID := int64(110)
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, 'Book', 'Book')", bookID)

	srv := &Server{db: database, dataDir: dataDir}
	reqBody, _ := json.Marshal(map[string]any{
		"title":   "Book",
		"authors": "Le Guin, Ursula K.; New Coauthor & Research && Development",
	})
	req := httptest.NewRequest("PATCH", "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, bookID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var response BookDetailDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	responseNames := make([]string, 0, len(response.AuthorsList))
	for _, author := range response.AuthorsList {
		responseNames = append(responseNames, author.Name)
	}
	wantNames := []string{"Le Guin, Ursula K.", "New Coauthor", "Research & Development"}
	if !slices.Equal(responseNames, wantNames) {
		t.Fatalf("response author names = %#v; want %#v", responseNames, wantNames)
	}

	// A later edit that omits authors must preserve the structured names. In
	// particular, the comma inside the first name is not a list delimiter.
	reqBody, _ = json.Marshal(map[string]any{"title": "Updated Book"})
	req = httptest.NewRequest("PATCH", "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewBuffer(reqBody))
	rr = httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, bookID)
	if rr.Code != http.StatusOK {
		t.Fatalf("title-only edit: expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	rows, err := database.Read(req.Context()).Query(`
		SELECT a.name
		FROM book_authors ba
		JOIN authors a ON a.id = ba.author_id
		WHERE ba.book_id = ?
		ORDER BY ba.author_order
	`, bookID)
	if err != nil {
		t.Fatalf("query authors: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan author: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("authors rows: %v", err)
	}

	want := []string{"Le Guin, Ursula K.", "New Coauthor", "Research & Development"}
	if len(got) != len(want) {
		t.Fatalf("authors = %#v; want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("authors = %#v; want %#v", got, want)
		}
	}

	var primaryAuthorSort string
	if err := database.Read(req.Context()).QueryRow("SELECT primary_author_sort FROM books WHERE id = ?", bookID).Scan(&primaryAuthorSort); err != nil {
		t.Fatalf("query primary_author_sort: %v", err)
	}
	if primaryAuthorSort != "Le Guin, Ursula K." {
		t.Fatalf("primary_author_sort = %q; want Le Guin, Ursula K.", primaryAuthorSort)
	}
}

func defaultStoragePath(t *testing.T, title, author, authorSort string, assetID int64, ext string) string {
	t.Helper()
	rel, err := storage.BookPath(storage.DefaultBookPathTemplate, storage.BookPathData{
		Title:      title,
		Author:     author,
		AuthorSort: authorSort,
		AssetID:    assetID,
		Ext:        ext,
	})
	if err != nil {
		t.Fatalf("StoragePath: %v", err)
	}
	return rel
}

// TestAPIEditRelayoutKeepsDBConsistent verifies that a title change relocates
// the asset on disk and that assets.storage_path is only updated to the new path
// after the physical move succeeds — so the path recorded in the DB always
// points at a file that actually exists (no silent DB/disk divergence).
func TestAPIEditRelayoutKeepsDBConsistent(t *testing.T) {
	dataDir := t.TempDir()

	dbPath := filepath.Join(dataDir, "library.db")
	database, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	bookID := int64(166)
	assetID := int64(1)
	authorName := "Jane Doe"
	authorSort := bookmeta.AuthorSort(authorName)
	oldTitle := "Old Title"
	ext := ".epub"

	oldPath := defaultStoragePath(t, oldTitle, authorName, authorSort, assetID, ext)
	absOld := filepath.Join(dataDir, oldPath)
	if err := os.MkdirAll(filepath.Dir(absOld), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(absOld, []byte("epub-bytes"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, ?, ?)", bookID, oldTitle, oldTitle)
	fileHash := bytes.Repeat([]byte{0xaa}, 32)
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (?, ?, ?, ?, ?, ?, ?)",
		assetID, bookID, oldPath, filepath.Base(oldPath), ext, fileHash, fileHash)
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, ?, ?)", authorName, authorSort)
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", bookID)

	srv := &Server{db: database, dataDir: dataDir}

	newTitle := "Brand New Title"
	reqBody, _ := json.Marshal(map[string]any{"title": newTitle})
	req := httptest.NewRequest("PATCH", "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, bookID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var sp string
	if err := database.Read(req.Context()).QueryRow("SELECT storage_path FROM assets WHERE id = ?", assetID).Scan(&sp); err != nil {
		t.Fatalf("query storage_path: %v", err)
	}

	wantNew := defaultStoragePath(t, newTitle, authorName, authorSort, assetID, ext)
	if sp != wantNew {
		t.Errorf("storage_path not updated: got %q, want %q", sp, wantNew)
	}

	// Core invariant: the path recorded in the DB must exist on disk.
	if _, err := os.Stat(filepath.Join(dataDir, sp)); err != nil {
		t.Fatalf("DB points at a file that does not exist on disk: %v", err)
	}

	// The file must no longer be at the old path.
	if _, err := os.Stat(absOld); !os.IsNotExist(err) {
		t.Errorf("old file still present after relayout: %v", err)
	}
}

func TestAPIEditMetadataOnlyDoesNotRequireStorage(t *testing.T) {
	dataDir := t.TempDir()

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	bookID := int64(150)
	assetID := int64(1)
	storagePath := defaultStoragePath(t, "Stored Title", "Jane Doe", bookmeta.AuthorSort("Jane Doe"), assetID, ".epub")
	mustExec(t, database, "INSERT INTO books (id, title, sort_title) VALUES (?, 'Stored Title', 'Stored Title')", bookID)
	mustExec(t, database, "INSERT INTO assets (id, book_id, storage_path, filename, extension, format, original_sha256, current_sha256) VALUES (?, ?, ?, ?, '.epub', 'epub', randomblob(32), randomblob(32))",
		assetID, bookID, storagePath, filepath.Base(storagePath))
	mustExec(t, database, "INSERT INTO authors (id, name, sort_name) VALUES (1, 'Jane Doe', ?)", bookmeta.AuthorSort("Jane Doe"))
	mustExec(t, database, "INSERT INTO book_authors (book_id, author_id, author_order) VALUES (?, 1, 0)", bookID)

	srv := &Server{db: database, dataDir: dataDir}
	reqBody, _ := json.Marshal(map[string]any{
		"title":       "Stored Title",
		"description": "Small note",
	})
	req := httptest.NewRequest("PATCH", "/api/books/"+strconv.FormatInt(bookID, 10), bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, bookID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var desc sql.NullString
	if err := database.Read(req.Context()).QueryRow("SELECT description FROM books WHERE id = ?", bookID).Scan(&desc); err != nil {
		t.Fatalf("query description: %v", err)
	}
	if desc.String != "Small note" {
		t.Fatalf("description = %q; want Small note", desc.String)
	}
	counts, err := db.CountDirtyMetadataWritebackAssets(database.Read(req.Context()), db.FullVisibilityScope())
	if err != nil {
		t.Fatalf("dirty writeback count: %v", err)
	}
	if counts.Dirty != 1 {
		t.Fatalf("dirty writeback assets = %d; want 1", counts.Dirty)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "books")); !os.IsNotExist(err) {
		t.Fatalf("metadata-only edit touched storage layout; stat err=%v", err)
	}
}
