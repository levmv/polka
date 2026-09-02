package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json/v2"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/bootstrap"
	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/importer"
	"github.com/levmv/polka/internal/storage"
)

// writeEPUB writes a minimal valid EPUB with the given title, creator and
// creator file-as (sort hint) to path.
func writeEPUB(t *testing.T, path, title, creator, fileAs string) {
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
	w.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}
}

// When a later book reuses an existing author (matched by name), its file lands
// under that author's *persisted* sort_name folder — not this book's per-file
// file-as — so import stays in agreement with `polka check`/relayout.
func TestImportReusedAuthorUsesPersistedSortName(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()
	root := storage.NewRoot(dataDir)
	if err := storage.EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}

	// First import establishes author "qntm" with a combined sidecar file-as.
	epubA := filepath.Join(tempDir, "ra.epub")
	writeEPUB(t, epubA, "Ra", "qntm", "qntm & Hughes, Sam")
	resA, err := importer.ImportFile(context.Background(), database, root, epubA, nil, importer.Options{})
	if err != nil {
		t.Fatalf("import A: %v", err)
	}

	// Second book by the same author carries a different (solo) file-as.
	epubB := filepath.Join(tempDir, "antimemetics.epub")
	writeEPUB(t, epubB, "Antimemetics Division", "qntm", "qntm")
	resB, err := importer.ImportFile(context.Background(), database, root, epubB, nil, importer.Options{})
	if err != nil {
		t.Fatalf("import B: %v", err)
	}

	dirA := filepath.Dir(resA.StoragePath)
	dirB := filepath.Dir(resB.StoragePath)
	if dirA != dirB {
		t.Errorf("reused author landed in a divergent folder:\n  A: %s\n  B: %s\n(want both under the persisted sort_name)", dirA, dirB)
	}
}

func TestImportDryRunRequiresExistingLibrary(t *testing.T) {
	base := t.TempDir()
	dataDir := filepath.Join(base, "data")
	src := filepath.Join(base, "book.epub")
	writeEPUB(t, src, "Dry Run", "Ada Writer", "Writer, Ada")

	err := runImport(context.Background(), dataDir, []string{"--dry-run", src})
	if !errors.Is(err, bootstrap.ErrLibraryNotFound) {
		t.Fatalf("runImport --dry-run error = %v; want ErrLibraryNotFound", err)
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("dry-run touched data dir; stat = %v", statErr)
	}
}

func TestImportJSONSingleFile(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	src := filepath.Join(tempDir, "book.epub")
	writeEPUB(t, src, "JSON Import", "Ada Writer", "Writer, Ada")

	out, err := captureStdout(t, func() error {
		return runImport(context.Background(), dataDir, []string{"--json", src})
	})
	if err != nil {
		t.Fatalf("runImport --json: %v", err)
	}

	var report importReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("unmarshal import JSON: %v\n%s", err, out)
	}
	if report.Source != src || report.DryRun {
		t.Fatalf("report source/dry_run = %q/%v", report.Source, report.DryRun)
	}
	if report.Summary.Imported != 1 || report.Summary.Errors != 0 {
		t.Fatalf("summary = %+v; want one imported and no errors", report.Summary)
	}
	if len(report.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(report.Items))
	}
	item := report.Items[0]
	if item.Status != "imported" || item.AssetID == "" || item.WorkID == "" {
		t.Fatalf("item status/ids = %+v", item)
	}
	if item.Title != "JSON Import" || item.Format != "epub" {
		t.Fatalf("item title/format = %q/%q", item.Title, item.Format)
	}
	if len(item.Authors) != 1 || item.Authors[0] != "Ada Writer" {
		t.Fatalf("item authors = %+v", item.Authors)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("reopen library: %v", err)
	}
	if _, err := database.Exec("UPDATE works SET deleted_at = unixepoch()"); err != nil {
		database.Close()
		t.Fatalf("trash imported work: %v", err)
	}
	database.Close()

	out, err = captureStdout(t, func() error {
		return runImport(context.Background(), dataDir, []string{"--json", "--dry-run", src})
	})
	if err != nil {
		t.Fatalf("runImport duplicate dry-run: %v", err)
	}
	var duplicateReport importReport
	if err := json.Unmarshal([]byte(out), &duplicateReport); err != nil {
		t.Fatalf("unmarshal duplicate import JSON: %v\n%s", err, out)
	}
	if duplicateReport.Summary.Duplicates != 1 || duplicateReport.Summary.Trashed != 1 {
		t.Fatalf("duplicate summary = %+v; want one duplicate in Trash", duplicateReport.Summary)
	}
	if len(duplicateReport.Items) != 1 || !duplicateReport.Items[0].InTrash {
		t.Fatalf("duplicate items = %+v; want one item in Trash", duplicateReport.Items)
	}
}

func TestImportJSONFolderDryRun(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	database.Close()

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	writeEPUB(t, filepath.Join(srcDir, "book.epub"), "Dry JSON", "Ada Writer", "Writer, Ada")
	if err := os.WriteFile(filepath.Join(srcDir, "cover.jpg"), []byte("not a book"), 0o644); err != nil {
		t.Fatalf("write cover sidecar: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runImport(context.Background(), dataDir, []string{"--json", "--dry-run", srcDir})
	})
	if err != nil {
		t.Fatalf("runImport --json --dry-run: %v", err)
	}

	var report importReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("unmarshal import JSON: %v\n%s", err, out)
	}
	if !report.DryRun || report.Source != srcDir {
		t.Fatalf("report source/dry_run = %q/%v", report.Source, report.DryRun)
	}
	if report.Summary.WouldImport != 1 || report.Summary.Skipped != 1 || report.Summary.Errors != 0 {
		t.Fatalf("summary = %+v; want one would_import, one skipped, no errors", report.Summary)
	}
	statuses := map[string]int{}
	for _, item := range report.Items {
		statuses[item.Status]++
	}
	if statuses["would_import"] != 1 || statuses["skipped"] != 1 {
		t.Fatalf("item statuses = %+v", statuses)
	}
}

func TestImportJSONUnsupportedSingleFileFails(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	src := filepath.Join(tempDir, "cover.jpg")
	if err := os.WriteFile(src, []byte("not a book"), 0o644); err != nil {
		t.Fatalf("write unsupported source: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runImport(context.Background(), dataDir, []string{"--json", src})
	})
	if err == nil {
		t.Fatal("runImport --json unsupported file returned nil error")
	}
	if !errors.Is(err, errImportItemsFailed) {
		t.Fatalf("runImport --json unsupported error = %v; want reported failure", err)
	}

	var report importReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("unmarshal import JSON: %v\n%s", err, out)
	}
	if report.Summary.Errors != 1 || len(report.Items) != 1 {
		t.Fatalf("report = %+v; want one error item", report)
	}
	if report.Items[0].Status != "error" || !strings.Contains(report.Items[0].Error, "unsupported book file extension") {
		t.Fatalf("item = %+v", report.Items[0])
	}
	if _, statErr := os.Stat(dataDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsupported import created data dir; stat = %v", statErr)
	}
}

func TestImportHumanUnsupportedSingleFileReportsOnce(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	src := filepath.Join(tempDir, "notes.txt.bak")
	if err := os.WriteFile(src, []byte("not a book"), 0o644); err != nil {
		t.Fatalf("write unsupported source: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runImport(context.Background(), dataDir, []string{src})
	})
	if !errors.Is(err, errImportItemsFailed) || !IsReportedFailure(err) {
		t.Fatalf("runImport unsupported error = %v; want top-level-suppressed reported failure", err)
	}
	want := "unsupported book file extension: notes.txt.bak"
	if count := strings.Count(out, want); count != 1 {
		t.Fatalf("reported error count = %d; want 1 in output:\n%s", count, out)
	}
	if !strings.Contains(out, "ERR   "+src+": "+want) {
		t.Fatalf("human output omitted source context:\n%s", out)
	}
}

func TestImportFolderIdempotency(t *testing.T) {
	tempDir := t.TempDir()

	dataDir := filepath.Join(tempDir, "data")
	dbPath := filepath.Join(dataDir, "library.db")
	initDefaultTestLibrary(t, dataDir)

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(filepath.Join(srcDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	epubPath := filepath.Join(srcDir, "nested", "test1.epub")
	writeEPUB(t, epubPath, "Idempotent Import", "Ada Writer", "Writer, Ada")

	err := runImport(context.Background(), dataDir, []string{srcDir})
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}

	db2, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db2.Close()
	var count int
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&count); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 asset, got %d", count)
	}

	err = runImport(context.Background(), dataDir, []string{srcDir})
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}

	var countAfter int
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&countAfter); err != nil {
		t.Fatalf("count assets after repeat: %v", err)
	}
	if countAfter != 1 {
		t.Fatalf("expected 1 asset after second import, got %d", countAfter)
	}
}

func TestImportFolderFollowsRootSymlink(t *testing.T) {
	tempDir := t.TempDir()
	sourceDir := filepath.Join(tempDir, "source")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	writeEPUB(t, filepath.Join(sourceDir, "book.epub"), "Linked Import", "Ada Writer", "Writer, Ada")

	link := filepath.Join(tempDir, "books-link")
	if err := os.Symlink(sourceDir, link); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}
	dataDir := filepath.Join(tempDir, "data")
	if err := runImport(context.Background(), dataDir, []string{link}); err != nil {
		t.Fatalf("import symlinked folder: %v", err)
	}

	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	var assets int
	if err := database.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 1 {
		t.Fatalf("assets = %d; want 1", assets)
	}
}

func TestImportFolderDryRunDoesNotPersist(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}

	dbPath := filepath.Join(dataDir, "library.db")
	database, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db.Init failed: %v", err)
	}
	database.Close()

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	epubPath := filepath.Join(srcDir, "dry.epub")
	if err := os.WriteFile(epubPath, []byte("dry run epub bytes"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}

	if err := runImport(context.Background(), dataDir, []string{"--dry-run", srcDir}); err != nil {
		t.Fatalf("dry-run import folder: %v", err)
	}
	if _, err := os.Stat(epubPath); err != nil {
		t.Fatalf("source file changed by dry-run: %v", err)
	}

	db2, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db.Init reopen: %v", err)
	}
	defer db2.Close()
	var assets int
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 0 {
		t.Fatalf("assets after dry-run = %d; want 0", assets)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "books")); !os.IsNotExist(err) {
		t.Fatalf("managed books dir exists after dry-run; err=%v", err)
	}
}

func TestImportFolderDryRunAfterImportDoesNotAddRows(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	dbPath := filepath.Join(dataDir, "library.db")
	initDefaultTestLibrary(t, dataDir)

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "existing.epub"), []byte("existing epub bytes"), 0o644); err != nil {
		t.Fatalf("write epub: %v", err)
	}

	if err := runImport(context.Background(), dataDir, []string{srcDir}); err != nil {
		t.Fatalf("import folder: %v", err)
	}
	if err := runImport(context.Background(), dataDir, []string{"--dry-run", srcDir}); err != nil {
		t.Fatalf("dry-run import folder: %v", err)
	}

	db2, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db.Init reopen: %v", err)
	}
	defer db2.Close()
	var assets int
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 1 {
		t.Fatalf("assets after import + dry-run = %d; want 1", assets)
	}
}

func TestImportFolderDeleteSources(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	dbPath := filepath.Join(dataDir, "library.db")
	initDefaultTestLibrary(t, dataDir)

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	importedPath := filepath.Join(srcDir, "new.epub")
	writeEPUB(t, importedPath, "Delete Source", "Ada Writer", "Writer, Ada")
	if err := runImport(context.Background(), dataDir, []string{"--delete-sources", srcDir}); err != nil {
		t.Fatalf("import folder with delete: %v", err)
	}
	if _, err := os.Stat(importedPath); !os.IsNotExist(err) {
		t.Fatalf("imported source exists after --delete-sources; err=%v", err)
	}

	duplicatePath := filepath.Join(srcDir, "duplicate.epub")
	writeEPUB(t, duplicatePath, "Delete Source", "Ada Writer", "Writer, Ada")
	if err := runImport(context.Background(), dataDir, []string{"--delete-sources", srcDir}); err != nil {
		t.Fatalf("duplicate import folder with delete: %v", err)
	}
	if _, err := os.Stat(duplicatePath); !os.IsNotExist(err) {
		t.Fatalf("duplicate source exists after --delete-sources; err=%v", err)
	}

	db2, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db.Init reopen: %v", err)
	}
	defer db2.Close()
	var assets int
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 1 {
		t.Fatalf("assets after import + duplicate cleanup = %d; want 1", assets)
	}
}

func TestImportRefusesSourcesOverlappingManagedRoot(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	source := filepath.Join(tempDir, "book.epub")
	writeEPUB(t, source, "Managed Source", "Ada Writer", "Writer, Ada")

	if err := runImport(context.Background(), dataDir, []string{source}); err != nil {
		t.Fatalf("initial import: %v", err)
	}
	database, err := db.InitPath(filepath.Join(dataDir, "library.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	root, err := storage.OpenRoot(database.DB, dataDir)
	if err != nil {
		database.Close()
		t.Fatalf("open books root: %v", err)
	}
	var storagePath string
	if err := database.QueryRow("SELECT storage_path FROM assets LIMIT 1").Scan(&storagePath); err != nil {
		database.Close()
		t.Fatalf("query storage path: %v", err)
	}
	database.Close()
	managedSource := root.Abs(storagePath)

	err = runImport(context.Background(), dataDir, []string{"--delete-sources", managedSource})
	if err == nil || !strings.Contains(err.Error(), "overlaps the managed books folder") {
		t.Fatalf("managed source import error = %v; want overlap refusal", err)
	}
	if _, err := os.Stat(managedSource); err != nil {
		t.Fatalf("managed source was touched: %v", err)
	}

	err = runImport(context.Background(), dataDir, []string{"--delete-sources", tempDir})
	if err == nil || !strings.Contains(err.Error(), "overlaps the managed books folder") {
		t.Fatalf("ancestor source import error = %v; want overlap refusal", err)
	}
	if _, err := os.Stat(managedSource); err != nil {
		t.Fatalf("managed source was touched through ancestor import: %v", err)
	}
}

func TestImportFolderCalibreDirectoryGroupsFormats(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	dbPath := filepath.Join(dataDir, "library.db")
	initDefaultTestLibrary(t, dataDir)

	bookDir := filepath.Join(tempDir, "calibre", "Calibre Author", "Calibre Book (1)")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}

	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Calibre Book</dc:title>
    <dc:creator opf:file-as="Author, Calibre">Calibre Author</dc:creator>
    <dc:publisher>Calibre Press</dc:publisher>
    <dc:subject>Imported</dc:subject>
  </metadata>
</package>`
	if err := os.WriteFile(filepath.Join(bookDir, "metadata.opf"), []byte(opf), 0o644); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	writeEPUB(t, filepath.Join(bookDir, "Calibre Book.epub"), "Embedded Title", "Embedded Author", "Author, Embedded")
	const pdf = `%PDF-1.4
1 0 obj
<< /Type /Info /Title (Embedded PDF) /Author (PDF Author) >>
endobj
trailer
<< /Info 1 0 R >>
%%EOF`
	if err := os.WriteFile(filepath.Join(bookDir, "Calibre Book.pdf"), []byte(pdf), 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runImport(context.Background(), dataDir, []string{filepath.Join(tempDir, "calibre")})
	})
	if err != nil {
		t.Fatalf("import folder: %v", err)
	}
	wantLine := "OK    " + filepath.Join("Calibre Author", "Calibre Book (1)") + " (assets 2)\n"
	if !strings.Contains(out, wantLine) {
		t.Fatalf("import output omitted concise relative group result %q:\n%s", wantLine, out)
	}
	if strings.Contains(out, tempDir) || strings.Contains(out, "(work ") {
		t.Fatalf("import output exposed source root or internal work ID:\n%s", out)
	}

	db2, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db.Init reopen: %v", err)
	}
	defer db2.Close()

	var works, assets, distinctWorks int
	if err := db2.QueryRow("SELECT COUNT(*) FROM works").Scan(&works); err != nil {
		t.Fatalf("count works: %v", err)
	}
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if err := db2.QueryRow("SELECT COUNT(DISTINCT work_id) FROM assets").Scan(&distinctWorks); err != nil {
		t.Fatalf("count asset works: %v", err)
	}
	if works != 1 || assets != 2 || distinctWorks != 1 {
		t.Fatalf("counts works/assets/distinct asset works = %d/%d/%d; want 1/2/1", works, assets, distinctWorks)
	}
	var primaryAssets int
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets WHERE is_primary = 1").Scan(&primaryAssets); err != nil {
		t.Fatalf("count primary assets: %v", err)
	}
	if primaryAssets != 1 {
		t.Fatalf("primary assets = %d; want 1", primaryAssets)
	}
	var primaryExt string
	if err := db2.QueryRow("SELECT extension FROM assets WHERE is_primary = 1").Scan(&primaryExt); err != nil {
		t.Fatalf("query primary asset: %v", err)
	}
	if primaryExt != ".epub" {
		t.Fatalf("primary extension = %q; want .epub", primaryExt)
	}

	var title, publisher, author string
	if err := db2.QueryRow(`
		SELECT w.title, w.publisher, a.name
		FROM works w
		JOIN work_authors wa ON wa.work_id = w.id
		JOIN authors a ON a.id = wa.author_id
		LIMIT 1
	`).Scan(&title, &publisher, &author); err != nil {
		t.Fatalf("query metadata: %v", err)
	}
	if title != "Calibre Book" || publisher != "Calibre Press" || author != "Calibre Author" {
		t.Fatalf("metadata = title %q publisher %q author %q; want calibre sidecar values", title, publisher, author)
	}

	if err := runImport(context.Background(), dataDir, []string{filepath.Join(tempDir, "calibre")}); err != nil {
		t.Fatalf("second import folder: %v", err)
	}
	if err := db2.QueryRow("SELECT COUNT(*) FROM works").Scan(&works); err != nil {
		t.Fatalf("count works after second import: %v", err)
	}
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets after second import: %v", err)
	}
	if works != 1 || assets != 2 {
		t.Fatalf("after second import works/assets = %d/%d; want 1/2", works, assets)
	}
}

func TestImportFolderDeleteSourcesRemovesCalibreDirectory(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	dbPath := filepath.Join(dataDir, "library.db")
	initDefaultTestLibrary(t, dataDir)

	bookDir := filepath.Join(tempDir, "calibre", "Calibre Author", "Calibre Book (1)")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	const opf = `<?xml version='1.0' encoding='utf-8'?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:opf="http://www.idpf.org/2007/opf">
    <dc:title>Calibre Book</dc:title>
    <dc:creator opf:file-as="Author, Calibre">Calibre Author</dc:creator>
  </metadata>
</package>`
	if err := os.WriteFile(filepath.Join(bookDir, "metadata.opf"), []byte(opf), 0o644); err != nil {
		t.Fatalf("write opf: %v", err)
	}
	writeEPUB(t, filepath.Join(bookDir, "Calibre Book.epub"), "Embedded Title", "Embedded Author", "Author, Embedded")

	out, err := captureStdout(t, func() error {
		return runImport(context.Background(), dataDir, []string{"--delete-sources", filepath.Join(tempDir, "calibre")})
	})
	if err != nil {
		t.Fatalf("import folder with delete: %v", err)
	}
	wantLine := "OK    " + filepath.Join("Calibre Author", "Calibre Book (1)") + "\n"
	if !strings.Contains(out, wantLine) {
		t.Fatalf("single-asset group output omitted concise result %q:\n%s", wantLine, out)
	}
	if strings.Contains(out, "assets 1") || strings.Contains(out, "(work ") {
		t.Fatalf("single-asset group output included default count or internal ID:\n%s", out)
	}
	if _, err := os.Stat(bookDir); !os.IsNotExist(err) {
		t.Fatalf("calibre source dir exists after --delete-sources; err=%v", err)
	}

	db2, err := db.InitPath(dbPath)
	if err != nil {
		t.Fatalf("db.Init reopen: %v", err)
	}
	defer db2.Close()
	var assets int
	if err := db2.QueryRow("SELECT COUNT(*) FROM assets").Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if assets != 1 {
		t.Fatalf("assets after calibre cleanup import = %d; want 1", assets)
	}
}
