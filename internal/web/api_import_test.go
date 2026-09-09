package web

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json/v2"
	"encoding/xml"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/storage"
)

func TestAPIImportUploadImportsAndDuplicates(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	ensureTestStorageLayout(t, dataDir)

	u := mustUser(t, database, "alice", db.RoleMember)
	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	sid, err := s.sessions.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	handler := testRoutes(t, s)

	epub := testEPUB(t, "Uploaded Book", "Upload Author", "Author, Upload")
	req := uploadBookRequest(t, "uploaded.epub", epub)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var got ImportUploadDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "imported" || got.Book.Title != "Uploaded Book" || got.Book.AuthorsDisplay != "Upload Author" {
		t.Fatalf("upload response = %+v, want imported Uploaded Book by Upload Author", got)
	}
	if got.AssetID == 0 || len(got.Book.Assets) != 1 {
		t.Fatalf("upload asset response = %+v", got)
	}
	if !got.Book.Assets[0].IsPrimary {
		t.Fatalf("uploaded asset is_primary = false; want true")
	}

	var storagePath string
	if err := database.Read(req.Context()).QueryRow("SELECT storage_path FROM assets WHERE id = ?", got.AssetID).Scan(&storagePath); err != nil {
		t.Fatalf("query asset path: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(dataDir, storagePath))
	if err != nil {
		t.Fatalf("read stored upload: %v", err)
	}
	if !bytes.Equal(stored, epub) {
		t.Fatal("stored upload differs from the original file")
	}

	req = uploadBookRequest(t, "uploaded.epub", epub)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	got = ImportUploadDTO{}
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode duplicate response: %v", err)
	}
	if got.Status != "duplicate" || got.Book.Title != "Uploaded Book" || got.AssetID == 0 {
		t.Fatalf("duplicate response = %+v", got)
	}

	var assets int
	if err := database.Read(req.Context()).QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 1 {
		t.Fatalf("asset count = %d, want 1 after duplicate upload", assets)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "tmp", "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("successful uploads left %d temporary files", len(entries))
	}
}

func TestAPIImportUploadRestoresTrashedDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	ensureTestStorageLayout(t, dataDir)

	u := mustUser(t, database, "alice", db.RoleMember)
	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	sid, err := s.sessions.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	handler := testRoutes(t, s)

	epub := testEPUB(t, "Restore Upload", "Trash Author", "Author, Trash")
	req := uploadBookRequest(t, "restore-upload.epub", epub)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("initial upload status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var imported ImportUploadDTO
	if err := json.UnmarshalRead(w.Body, &imported); err != nil {
		t.Fatalf("decode initial response: %v", err)
	}
	if imported.Book.ID == 0 || imported.AssetID == 0 {
		t.Fatalf("initial response missing ids: %+v", imported)
	}

	if err := db.SoftDeleteBook(database.Write(t.Context()), imported.Book.ID, u.ID); err != nil {
		t.Fatalf("soft delete imported book: %v", err)
	}

	req = uploadBookRequest(t, "restore-upload.epub", epub)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("restore duplicate status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	var restored ImportUploadDTO
	if err := json.UnmarshalRead(w.Body, &restored); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	if restored.Status != "restored" || restored.Book.ID != imported.Book.ID || restored.AssetID != imported.AssetID {
		t.Fatalf("restore response = %+v; want restored original book/asset", restored)
	}

	var deletedAt sql.NullInt64
	if err := database.Read(req.Context()).QueryRow("SELECT deleted_at FROM books WHERE id = ?", imported.Book.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("query restored book: %v", err)
	}
	if deletedAt.Valid {
		t.Fatalf("deleted_at = %d, want NULL after duplicate upload restore", deletedAt.Int64)
	}

	var assets int
	if err := database.Read(req.Context()).QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 1 {
		t.Fatalf("asset count = %d, want 1 after restoring duplicate", assets)
	}
}

func TestAPIImportUploadAcceptsZippedFB2(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	ensureTestStorageLayout(t, dataDir)

	u := mustUser(t, database, "alice", db.RoleMember)
	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	sid, err := s.sessions.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	req := uploadBookRequest(t, "zipped.fb2.zip", testFB2ZipUpload(t))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
	w := httptest.NewRecorder()
	testRoutes(t, s).ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
	var got ImportUploadDTO
	if err := json.UnmarshalRead(w.Body, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Status != "imported" || got.Book.Title != "Zipped Upload" || got.Book.AuthorsDisplay != "Zip Author" {
		t.Fatalf("upload response = %+v, want imported Zipped Upload by Zip Author", got)
	}
	if len(got.Book.Assets) != 1 || got.Book.Assets[0].Extension != ".fb2.zip" || !got.Book.Assets[0].IsPrimary || !got.Book.Assets[0].CanRead {
		t.Fatalf("assets = %+v; want readable primary .fb2.zip asset", got.Book.Assets)
	}

	var storagePath string
	if err := database.Read(req.Context()).QueryRow("SELECT storage_path FROM assets WHERE id = ?", got.AssetID).Scan(&storagePath); err != nil {
		t.Fatalf("query asset path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, storagePath)); err != nil {
		t.Fatalf("stored zipped FB2 missing: %v", err)
	}
}

func TestAPIImportUploadRejectsInvalidRequests(t *testing.T) {
	dataDir := t.TempDir()
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	ensureTestStorageLayout(t, dataDir)

	u := mustUser(t, database, "alice", db.RoleMember)
	s := &Server{db: database, dataDir: dataDir, sessions: newSessionStore(database)}
	sid, err := s.sessions.issue(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}

	handler := testRoutes(t, s)
	for _, tc := range []struct {
		name        string
		filename    string
		truncate    bool
		limitOffset int64
		status      int
	}{
		{name: "unsupported filename", filename: "notes.xyz", status: http.StatusBadRequest},
		{name: "interrupted upload", filename: "book.txt", truncate: true, status: http.StatusBadRequest},
		{name: "oversized book", filename: "book.txt", limitOffset: -128, status: http.StatusRequestEntityTooLarge},
		{name: "oversized trailing field", filename: "book.txt", limitOffset: 256, status: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			form := multipart.NewWriter(&body)
			part, err := form.CreateFormFile("book", tc.filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(part, strings.Repeat("Book text.\n", 64)); err != nil {
				t.Fatal(err)
			}
			bookEnd := int64(body.Len())
			if err := form.WriteField("extra", strings.Repeat("x", 1024)); err != nil {
				t.Fatal(err)
			}
			if err := form.Close(); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/import", &body)
			req.Header.Set("Content-Type", form.FormDataContentType())
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
			req.ContentLength = -1
			w := httptest.NewRecorder()
			if tc.truncate {
				req.Body = io.NopCloser(io.LimitReader(req.Body, bookEnd-128))
			} else if tc.limitOffset != 0 {
				// Exercise the production error path with a small request limit.
				req.Body = http.MaxBytesReader(w, req.Body, bookEnd+tc.limitOffset)
			}
			handler.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tc.status, w.Body.String())
			}
			var books int
			if err := database.Read(req.Context()).QueryRow("SELECT COUNT(*) FROM books").Scan(&books); err != nil {
				t.Fatalf("count books: %v", err)
			}
			if books != 0 {
				t.Fatalf("books = %d, want 0", books)
			}
			entries, err := os.ReadDir(filepath.Join(dataDir, "tmp", "uploads"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed upload left %d temporary files", len(entries))
			}
		})
	}
}

func TestAPIImportRequiresLayoutBeforeWrite(t *testing.T) {
	// The books root is the books tree. A write must be refused with 503 both when
	// the root is missing and when it is empty over an already-populated catalog
	// (a dropped mount), and it must never silently create or repopulate the root.
	cases := []struct {
		name   string
		setup  func(t *testing.T, dataDir string, database *db.DB) storage.Root
		verify func(t *testing.T, root storage.Root)
	}{
		{
			name: "missing root blocks a fresh import",
			setup: func(t *testing.T, dataDir string, _ *db.DB) storage.Root {
				return storage.NewRoot(filepath.Join(dataDir, "managed"))
			},
			verify: func(t *testing.T, root storage.Root) {
				if _, err := os.Stat(root.Path); !os.IsNotExist(err) {
					t.Fatalf("import created the managed root despite missing layout; stat err=%v", err)
				}
			},
		},
		{
			name: "empty root over an existing catalog blocks import",
			setup: func(t *testing.T, dataDir string, database *db.DB) storage.Root {
				root := storage.NewRoot(filepath.Join(dataDir, "managed"))
				if err := storage.EnsureLayout(root); err != nil {
					t.Fatalf("EnsureLayout: %v", err)
				}
				mustExec(t, database, `INSERT INTO books (id, title, sort_title) VALUES (169, 'Seed', 'Seed');
					 INSERT INTO assets (id, book_id, storage_path, filename, extension, original_sha256, current_sha256)
					   VALUES (1, 169, 'Seed/a_seed.epub', 'a_seed.epub', '.epub', randomblob(32), randomblob(32));`)

				return root
			},
			verify: func(t *testing.T, root storage.Root) {
				empty, err := storage.RootLooksEmpty(root)
				if err != nil {
					t.Fatalf("RootLooksEmpty: %v", err)
				}
				if !empty {
					t.Fatalf("import wrote a book into the root over a dropped mount")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
			if err != nil {
				t.Fatalf("db.Init: %v", err)
			}
			defer database.Close()

			u := mustUser(t, database, "alice", db.RoleMember)
			root := tc.setup(t, dataDir, database)

			s := &Server{db: database, dataDir: dataDir, storageRoot: root, sessions: newSessionStore(database)}
			sid, err := s.sessions.issue(t.Context(), u.ID)
			if err != nil {
				t.Fatalf("issue session: %v", err)
			}

			req := uploadBookRequest(t, "uploaded.epub", testEPUB(t, "Uploaded Book", "Upload Author", "Author, Upload"))
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid})
			w := httptest.NewRecorder()
			testRoutes(t, s).ServeHTTP(w, req)

			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusServiceUnavailable, w.Body.String())
			}
			tc.verify(t, root)
		})
	}
}

func uploadBookRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	part, err := mw.CreateFormFile("book", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/import", &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func testFB2ZipUpload(t *testing.T) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	f, err := w.Create("book.fb2")
	if err != nil {
		t.Fatalf("create fb2 zip entry: %v", err)
	}
	if _, err := f.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
  <description>
    <title-info>
      <author><first-name>Zip</first-name><last-name>Author</last-name></author>
      <book-title>Zipped Upload</book-title>
      <lang>en</lang>
    </title-info>
  </description>
</FictionBook>`)); err != nil {
		t.Fatalf("write fb2 zip entry: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fb2 zip: %v", err)
	}
	return buf.Bytes()
}

func testEPUB(t *testing.T, title, creator, fileAs string) []byte {
	t.Helper()
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)
	f0, _ := w.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	f0.Write([]byte("application/epub+zip"))
	f1, _ := w.Create("META-INF/container.xml")
	f1.Write([]byte(`<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`))
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	f2, _ := w.Create("OEBPS/content.opf")
	f2.Write([]byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="2.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
		<dc:title>` + esc(title) + `</dc:title>
		<dc:creator opf:file-as="` + esc(fileAs) + `">` + esc(creator) + `</dc:creator>
	</metadata></package>`))
	if err := w.Close(); err != nil {
		t.Fatalf("close epub: %v", err)
	}
	return buf.Bytes()
}
