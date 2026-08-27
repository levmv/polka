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

	workID := "w_test"
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, 'Title', 'Title')", workID)

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

	req := httptest.NewRequest("PATCH", "/api/works/"+workID, bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()

	// Bypass auth by calling handler directly
	srv.handleAPIEditBook(rr, req, workID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	defer database.Close()

	var sortTitle string
	var metadataRev int
	var newLang, newPub, newDate sql.NullString
	err = database.QueryRow("SELECT sort_title, language, publisher, published_date, metadata_rev FROM works WHERE id = ?", workID).Scan(&sortTitle, &newLang, &newPub, &newDate, &metadataRev)
	if err != nil {
		t.Fatalf("query works: %v", err)
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
	err = database.QueryRow("SELECT manual_overrides FROM works WHERE id = ?", workID).Scan(&manualOverrides)
	if err != nil {
		t.Fatalf("query works: %v", err)
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

func TestAPIEditPreservesFields(t *testing.T) {
	dataDir := t.TempDir()

	// Init DB
	dbPath := filepath.Join(dataDir, "library.db")
	database, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	workID := "w_test"
	database.Exec("INSERT INTO works (id, title, sort_title, language, publisher, published_date) VALUES (?, 'Title', 'Title', 'fr', 'Gallimard', '1942')", workID)

	srv := &Server{
		db:      database,
		dataDir: dataDir,
	}

	reqBody, _ := json.Marshal(map[string]any{
		"title": "New Title",
	})

	req := httptest.NewRequest("PATCH", "/api/works/"+workID, bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()

	srv.handleAPIEditBook(rr, req, workID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var newLang, newPub, newDate sql.NullString
	err = database.QueryRow("SELECT language, publisher, published_date FROM works WHERE id = ?", workID).Scan(&newLang, &newPub, &newDate)
	if err != nil {
		t.Fatalf("query works: %v", err)
	}

	if newLang.String != "fr" || newPub.String != "Gallimard" || newDate.String != "1942" {
		t.Errorf("fields not preserved: %s, %s, %s", newLang.String, newPub.String, newDate.String)
	}
}

func TestAPIEditPatchMergesStaleFieldsAndClearsNulls(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	workID := "w_patch_merge"
	if _, err := database.Exec(`
		INSERT INTO works (
			id, title, sort_title, series, series_index, language, publisher
		) VALUES (?, 'Old Title', 'Old Title', 'Old Series', 2, 'fr', 'Old Press')
	`, workID); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	if _, err := database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('au_patch_merge', 'Old Author', 'Author, Old')"); err != nil {
		t.Fatalf("insert author: %v", err)
	}
	if _, err := database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'au_patch_merge', 0)", workID); err != nil {
		t.Fatalf("link author: %v", err)
	}

	srv := &Server{db: database, dataDir: dataDir}
	patch := func(body map[string]any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal patch: %v", err)
		}
		req := httptest.NewRequest(http.MethodPatch, "/api/books/"+workID, bytes.NewReader(raw))
		rr := httptest.NewRecorder()
		srv.handleAPIEditBook(rr, req, workID)
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
	if err := database.QueryRow("SELECT title, sort_title, publisher FROM works WHERE id = ?", workID).Scan(&title, &sortTitle, &publisher); err != nil {
		t.Fatalf("query merged work: %v", err)
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

	var series, language sql.NullString
	var seriesIndex sql.NullFloat64
	if err := database.QueryRow(`
		SELECT sort_title, series, series_index, language, publisher
		FROM works WHERE id = ?
	`, workID).Scan(&sortTitle, &series, &seriesIndex, &language, &publisher); err != nil {
		t.Fatalf("query cleared work: %v", err)
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

	authors, err := db.AuthorsByWorkIDs(database, []string{workID})
	if err != nil {
		t.Fatalf("authors: %v", err)
	}
	if got := authors[workID]; len(got) != 1 || got[0].Name != "Unknown Author" {
		t.Fatalf("authors after null = %#v; want Unknown Author", got)
	}

	var rawOverrides string
	if err := database.QueryRow("SELECT manual_overrides FROM works WHERE id = ?", workID).Scan(&rawOverrides); err != nil {
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

	workID := "w_manual_clear"
	if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, 'Title', 'Title')", workID); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	srv := &Server{db: database, dataDir: dataDir}
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+workID, bytes.NewBufferString(`{"description":null}`))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, workID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}

	var rawOverrides string
	var metadataRev int
	if err := database.QueryRow("SELECT manual_overrides, metadata_rev FROM works WHERE id = ?", workID).Scan(&rawOverrides, &metadataRev); err != nil {
		t.Fatalf("query work: %v", err)
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

	workID := "w_null_title"
	if _, err := database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, 'Title', 'Title')", workID); err != nil {
		t.Fatalf("insert work: %v", err)
	}
	srv := &Server{db: database, dataDir: dataDir}
	req := httptest.NewRequest(http.MethodPatch, "/api/books/"+workID, bytes.NewBufferString(`{"title":null}`))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, workID)
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

			const workID = "w_sort"
			if _, err := database.Exec(
				"INSERT INTO works (id, title, sort_title, tags) VALUES (?, ?, ?, 'old')",
				workID, tt.initialTitle, tt.initialSort,
			); err != nil {
				t.Fatalf("insert work: %v", err)
			}
			reqBody, err := json.Marshal(tt.patch)
			if err != nil {
				t.Fatalf("marshal patch: %v", err)
			}
			req := httptest.NewRequest(http.MethodPatch, "/api/works/"+workID, bytes.NewReader(reqBody))
			rr := httptest.NewRecorder()
			(&Server{db: database, dataDir: dataDir}).handleAPIEditBook(rr, req, workID)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200: %s", rr.Code, rr.Body.String())
			}

			var title, sortTitle string
			if err := database.QueryRow("SELECT title, sort_title FROM works WHERE id = ?", workID).Scan(&title, &sortTitle); err != nil {
				t.Fatalf("query work: %v", err)
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

	workID := "w_author_commas"
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, 'Book', 'Book')", workID)

	srv := &Server{db: database, dataDir: dataDir}
	reqBody, _ := json.Marshal(map[string]any{
		"title":   "Book",
		"authors": "Le Guin, Ursula K.; New Coauthor & Research && Development",
	})
	req := httptest.NewRequest("PATCH", "/api/works/"+workID, bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, workID)

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
	req = httptest.NewRequest("PATCH", "/api/works/"+workID, bytes.NewBuffer(reqBody))
	rr = httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, workID)
	if rr.Code != http.StatusOK {
		t.Fatalf("title-only edit: expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	rows, err := database.Query(`
		SELECT a.name
		FROM work_authors wa
		JOIN authors a ON a.id = wa.author_id
		WHERE wa.work_id = ?
		ORDER BY wa.author_order
	`, workID)
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
	if err := database.QueryRow("SELECT primary_author_sort FROM works WHERE id = ?", workID).Scan(&primaryAuthorSort); err != nil {
		t.Fatalf("query primary_author_sort: %v", err)
	}
	if primaryAuthorSort != "Le Guin, Ursula K." {
		t.Fatalf("primary_author_sort = %q; want Le Guin, Ursula K.", primaryAuthorSort)
	}
}

func defaultStoragePath(t *testing.T, title, author, authorSort, assetID, ext string) string {
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

	workID := "w_relayout"
	assetID := "as_relayout"
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

	database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, ?, ?)", workID, oldTitle, oldTitle)
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, original_sha256, current_sha256) VALUES (?, ?, ?, ?, ?, ?, ?)",
		assetID, workID, oldPath, filepath.Base(oldPath), ext, "deadbeef", "deadbeef")
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('au_relayout', ?, ?)", authorName, authorSort)
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'au_relayout', 0)", workID)

	srv := &Server{db: database, dataDir: dataDir}

	newTitle := "Brand New Title"
	reqBody, _ := json.Marshal(map[string]any{"title": newTitle})
	req := httptest.NewRequest("PATCH", "/api/works/"+workID, bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, workID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var sp string
	if err := database.QueryRow("SELECT storage_path FROM assets WHERE id = ?", assetID).Scan(&sp); err != nil {
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

	workID := "w_metadata_only"
	assetID := "as_metadata_only"
	storagePath := defaultStoragePath(t, "Stored Title", "Jane Doe", bookmeta.AuthorSort("Jane Doe"), assetID, ".epub")
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, 'Stored Title', 'Stored Title')", workID)
	database.Exec("INSERT INTO assets (id, work_id, storage_path, filename, extension, format) VALUES (?, ?, ?, ?, '.epub', 'epub')",
		assetID, workID, storagePath, filepath.Base(storagePath))
	database.Exec("INSERT INTO authors (id, name, sort_name) VALUES ('au_metadata_only', 'Jane Doe', ?)", bookmeta.AuthorSort("Jane Doe"))
	database.Exec("INSERT INTO work_authors (work_id, author_id, author_order) VALUES (?, 'au_metadata_only', 0)", workID)

	srv := &Server{db: database, dataDir: dataDir}
	reqBody, _ := json.Marshal(map[string]any{
		"title":       "Stored Title",
		"description": "Small note",
	})
	req := httptest.NewRequest("PATCH", "/api/works/"+workID, bytes.NewBuffer(reqBody))
	rr := httptest.NewRecorder()
	srv.handleAPIEditBook(rr, req, workID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var desc sql.NullString
	if err := database.QueryRow("SELECT description FROM works WHERE id = ?", workID).Scan(&desc); err != nil {
		t.Fatalf("query description: %v", err)
	}
	if desc.String != "Small note" {
		t.Fatalf("description = %q; want Small note", desc.String)
	}
	counts, err := db.CountDirtyMetadataWritebackAssets(database, db.FullVisibilityScope())
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

// TestAPIEditPatchRoute drives the real mux (not the edit handler directly) to
// confirm PATCH /api/books/{id} routes to the edit handler and is not rejected.
func TestAPIEditPatchRoute(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer database.Close()

	workID := "w_patch"
	database.Exec("INSERT INTO works (id, title, sort_title) VALUES (?, 'Title', 'Title')", workID)
	member := mustUser(t, database, "member", db.RoleMember)

	srv := &Server{db: database, dataDir: dataDir}
	mux, err := srv.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	reqBody, _ := json.Marshal(map[string]any{"title": "Patched Title"})
	req := httptest.NewRequest("PATCH", "/api/books/"+workID, bytes.NewBuffer(reqBody))
	req = req.WithContext(withUserID(req.Context(), member.ID))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH via mux: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var title string
	database.QueryRow("SELECT title FROM works WHERE id = ?", workID).Scan(&title)
	if title != "Patched Title" {
		t.Errorf("title not updated via PATCH route: %q", title)
	}

	for _, method := range []string{http.MethodPut, http.MethodPost} {
		req := httptest.NewRequest(method, "/api/books/"+workID, bytes.NewBufferString(`{"title":"Legacy write"}`))
		req = req.WithContext(withUserID(req.Context(), member.ID))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d; want 405", method, rr.Code)
		}
	}
}
