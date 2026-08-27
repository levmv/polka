package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/levmv/polka/internal/db"
)

func TestAPIAnnotationExportIsStandaloneEscapedAndUserScoped(t *testing.T) {
	database, dir := setupTestDB(t)
	defer database.Close()

	alice := mustUser(t, database, "alice-export", db.RoleMember)
	bob := mustUser(t, database, "bob-export", db.RoleMember)
	if _, err := database.Exec(`
		UPDATE works SET title = 'A/B: <Book>' WHERE id = 'w_1';
		UPDATE authors SET name = 'Writer & <script>Co</script>' WHERE id = 'a_1';
		UPDATE assets SET format = 'epub', can_read = 1, is_primary = 1 WHERE id = 'asset_1';
	`); err != nil {
		t.Fatalf("prepare export metadata: %v", err)
	}
	ann, err := database.CreateAnnotation(alice.ID, "asset_1", db.AnnotationCreate{
		CFI:   `epubcfi(/6/2[<bad>])`,
		Quote: `Quoted </mark><script>alert("quote")</script>`,
		Note:  `<img src=x onerror="alert('note')">`,
	})
	if err != nil {
		t.Fatalf("create annotation: %v", err)
	}
	created := time.Date(2026, time.July, 27, 12, 30, 0, 0, time.UTC).Unix()
	if _, err := database.Exec(`
		UPDATE user_annotations SET created_at = ?, updated_at = ? WHERE id = ?
	`, created, created+3600, ann.ID); err != nil {
		t.Fatalf("set annotation times: %v", err)
	}

	s := newTestServer(database, dir)
	handler := testRoutes(t, s)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/asset_1/annotations/export", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	wantDisposition := `attachment; filename="A B Book - highlights.html"; filename*=UTF-8''A%20B%20Book%20-%20highlights.html`
	if got := w.Header().Get("Content-Disposition"); got != wantDisposition {
		t.Fatalf("content disposition = %q, want %q", got, wantDisposition)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("cache control = %q", got)
	}
	body := w.Body.String()
	for _, want := range []string{
		"<!doctype html>",
		"A/B: &lt;Book&gt;",
		"Writer &amp; &lt;script&gt;Co&lt;/script&gt;",
		`Quoted &lt;/mark&gt;&lt;script&gt;alert(&#34;quote&#34;)&lt;/script&gt;`,
		`&lt;img src=x onerror=&#34;alert(&#39;note&#39;)&#34;&gt;`,
		`epubcfi(/6/2[&lt;bad&gt;])`,
		"27 Jul 2026, 12:30 UTC",
		"27 Jul 2026, 13:30 UTC",
		"1 highlight · EPUB",
		"Exported from polka",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("export missing %q", want)
		}
	}
	for _, unsafe := range []string{"<script>Co</script>", `<script>alert("quote")</script>`, "<img src=x"} {
		if strings.Contains(body, unsafe) {
			t.Errorf("export contains unescaped markup %q", unsafe)
		}
	}
	if strings.Contains(body, "href=") {
		t.Error("standalone export unexpectedly contains an external link")
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/asset_1/annotations/export?format=markdown", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("Markdown export status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "text/markdown; charset=utf-8" {
		t.Fatalf("Markdown content type = %q", got)
	}
	wantDisposition = `attachment; filename="A B Book - highlights.md"; filename*=UTF-8''A%20B%20Book%20-%20highlights.md`
	if got := w.Header().Get("Content-Disposition"); got != wantDisposition {
		t.Fatalf("Markdown content disposition = %q, want %q", got, wantDisposition)
	}
	body = w.Body.String()
	for _, want := range []string{
		"# A/B: &lt;Book&gt;",
		"Writer & &lt;script&gt;Co&lt;/script&gt;",
		`> Quoted &lt;/mark&gt;&lt;script&gt;alert("quote")&lt;/script&gt;`,
		`&lt;img src=x onerror="alert('note')"&gt;`,
		"Highlighted 27 Jul 2026, 12:30 UTC · Updated 27 Jul 2026, 13:30 UTC",
		"Source: `epubcfi(/6/2[<bad>])`",
		"Exported from polka",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Markdown export missing %q", want)
		}
	}
	for _, unsafe := range []string{"<script>Co</script>", `<script>alert("quote")</script>`, "<img src=x"} {
		if strings.Contains(body, unsafe) {
			t.Errorf("Markdown export contains unescaped markup %q", unsafe)
		}
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/asset_1/annotations/export?format=pdf", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported export format status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, bob.ID, http.MethodGet, "/api/reader/assets/asset_1/annotations/export", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("second user export status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	if body := w.Body.String(); strings.Contains(body, "Quoted") || !strings.Contains(body, "No highlights or notes for this file.") {
		t.Fatalf("second user export leaked another user's annotation: %s", body)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, jsonRequest(t, s, alice.ID, http.MethodGet, "/api/reader/assets/missing/annotations/export", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want %d", w.Code, http.StatusNotFound)
	}

	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/reader/assets/asset_1/annotations/export", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated export status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAnnotationExportFilename(t *testing.T) {
	for _, tt := range []struct {
		title string
		want  string
	}{
		{title: "  The   Hobbit  ", want: "The Hobbit - highlights.html"},
		{title: `A/B:C\D*E?"F"<G>|H`, want: "A B C D E F G H - highlights.html"},
		{title: "\x00\t", want: "Book - highlights.html"},
	} {
		if got := annotationExportFilename(tt.title, "html"); got != tt.want {
			t.Errorf("annotationExportFilename(%q, html) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestMarkdownCodeSpanPreservesBackticksAndWhitespace(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  string
	}{
		{value: "epubcfi(/6/2)", want: "`epubcfi(/6/2)`"},
		{value: "epubcfi(`one``two)", want: "```epubcfi(`one``two)```"},
		{value: " surrounded ", want: "`  surrounded  `"},
		{value: "line\r\nbreak", want: "`line break`"},
	} {
		if got := markdownCodeSpan(tt.value); got != tt.want {
			t.Errorf("markdownCodeSpan(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}
