package web

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/levmv/polka/internal/db"
	"github.com/levmv/polka/internal/format"
)

type annotationExportDocument struct {
	Title       string
	Authors     string
	Format      string
	Summary     string
	Annotations []annotationExportItem
}

type annotationExportItem struct {
	Quote   string
	Note    string
	CFI     string
	Created annotationExportTime
	Updated annotationExportTime
}

type annotationExportTime struct {
	ISO   string
	Human string
}

var annotationExportTemplate = template.Must(template.New("annotation-export").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'">
<title>Highlights and notes — {{.Title}}</title>
<style>
:root {
  color-scheme: light dark;
  --background: #f6f3ed;
  --paper: #fffdf8;
  --text: #292722;
  --muted: #716d64;
  --line: #dcd5c9;
  --accent: #8a6530;
  --highlight: #fff1a8;
}
@media (prefers-color-scheme: dark) {
  :root {
    --background: #1e1d1a;
    --paper: #292722;
    --text: #f2eee6;
    --muted: #b8b1a5;
    --line: #4a463e;
    --accent: #dfb879;
    --highlight: #554a20;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--background);
  color: var(--text);
  font: 17px/1.6 ui-serif, Georgia, Cambria, "Times New Roman", serif;
}
main {
  width: min(48rem, calc(100% - 2rem));
  margin: 3rem auto;
}
header {
  padding: 0 0 2rem;
  border-bottom: 1px solid var(--line);
}
h1 {
  margin: 0;
  font-size: clamp(2rem, 7vw, 3.5rem);
  line-height: 1.08;
}
.authors {
  margin: .7rem 0 0;
  color: var(--muted);
  font-size: 1.15rem;
}
.summary {
  margin: 1rem 0 0;
  color: var(--muted);
  font: .85rem/1.4 ui-sans-serif, system-ui, sans-serif;
}
ol {
  margin: 0;
  padding: 0;
  list-style: none;
  counter-reset: highlights;
}
li {
  counter-increment: highlights;
  margin: 2rem 0;
  padding: 1.5rem;
  background: var(--paper);
  border: 1px solid var(--line);
  border-radius: .7rem;
  break-inside: avoid;
}
li::before {
  content: counter(highlights);
  display: block;
  margin-bottom: .7rem;
  color: var(--accent);
  font: 600 .75rem/1 ui-sans-serif, system-ui, sans-serif;
}
blockquote {
  margin: 0;
  padding: 0;
  white-space: pre-wrap;
}
mark {
  background: var(--highlight);
  color: inherit;
}
.note {
  margin: 1.1rem 0 0;
  padding: .9rem 1rem;
  border-left: .22rem solid var(--accent);
  white-space: pre-wrap;
}
.meta, details {
  margin-top: 1rem;
  color: var(--muted);
  font: .78rem/1.45 ui-sans-serif, system-ui, sans-serif;
}
details summary { cursor: pointer; }
code {
  display: block;
  margin-top: .45rem;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
.empty {
  margin: 2rem 0;
  color: var(--muted);
}
footer {
  margin-top: 2rem;
  color: var(--muted);
  font: .78rem/1.4 ui-sans-serif, system-ui, sans-serif;
}
@media print {
  :root {
    --background: #fff;
    --paper: #fff;
    --text: #111;
    --muted: #555;
    --line: #bbb;
    --accent: #555;
    --highlight: #fff0a3;
  }
  body { font-size: 11pt; }
  main { width: auto; margin: 0; }
  li { padding: 1rem 0; border-width: 1px 0 0; border-radius: 0; }
  details { display: none; }
}
</style>
</head>
<body>
<main>
  <header>
    <h1>{{.Title}}</h1>
    {{if .Authors}}<p class="authors">{{.Authors}}</p>{{end}}
    <p class="summary">{{.Summary}}{{if .Format}} · {{.Format}}{{end}}</p>
  </header>
  {{if .Annotations}}
  <ol>
    {{range .Annotations}}
    <li>
      <blockquote><mark>{{.Quote}}</mark></blockquote>
      {{if .Note}}<p class="note">{{.Note}}</p>{{end}}
      <p class="meta">Highlighted <time datetime="{{.Created.ISO}}">{{.Created.Human}}</time>{{if .Updated.Human}} · Updated <time datetime="{{.Updated.ISO}}">{{.Updated.Human}}</time>{{end}}</p>
      <details>
        <summary>Source location</summary>
        <code>{{.CFI}}</code>
      </details>
    </li>
    {{end}}
  </ol>
  {{else}}
  <p class="empty">No highlights or notes for this file.</p>
  {{end}}
  <footer>Exported from polka</footer>
</main>
</body>
</html>
`))

func (s *Server) handleAPIAnnotationExport(w http.ResponseWriter, r *http.Request) {
	exportFormat := r.URL.Query().Get("format")
	if exportFormat == "" {
		exportFormat = "html"
	}
	if exportFormat != "html" && exportFormat != "markdown" {
		http.Error(w, "Unsupported export format", http.StatusBadRequest)
		return
	}

	assetID := r.PathValue("id")
	if _, ok := s.requireAssetAccess(w, r, assetID); !ok {
		return
	}

	asset, err := s.assetFile(assetID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Asset not found", http.StatusNotFound)
		return
	} else if err != nil {
		serverError(w, err)
		return
	}
	rows, err := s.db.ListAnnotations(UserID(r.Context()), assetID)
	if writeReaderStateError(w, err) {
		return
	}
	authorsByWork, err := db.AuthorsByWorkIDs(s.db, []string{asset.WorkID})
	if err != nil {
		serverError(w, err)
		return
	}
	_, authors := authorsToDTO(authorsByWork[asset.WorkID])

	document := buildAnnotationExportDocument(asset, authors, rows)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if exportFormat == "markdown" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", fileContentDisposition("attachment", annotationExportFilename(asset.Title, "md")))
		_ = writeAnnotationMarkdown(w, document)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fileContentDisposition("attachment", annotationExportFilename(asset.Title, "html")))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
	_ = writeAnnotationExport(w, document)
}

func buildAnnotationExportDocument(asset assetFileRow, authors string, rows []db.Annotation) annotationExportDocument {
	items := make([]annotationExportItem, 0, len(rows))
	for _, row := range rows {
		item := annotationExportItem{
			Quote:   row.Quote,
			Note:    row.Note,
			CFI:     row.CFI,
			Created: annotationExportTimestamp(row.CreatedAt),
		}
		if row.UpdatedAt > row.CreatedAt {
			item.Updated = annotationExportTimestamp(row.UpdatedAt)
		}
		items = append(items, item)
	}

	summary := "No highlights"
	if len(items) == 1 {
		summary = "1 highlight"
	} else if len(items) > 1 {
		summary = fmt.Sprintf("%d highlights", len(items))
	}
	title := strings.TrimSpace(asset.Title)
	if title == "" {
		title = "Untitled"
	}
	formatLabel := ""
	if asset.Format != format.FormatUnknown {
		formatLabel = format.FormatLabel(asset.Format)
	}
	return annotationExportDocument{
		Title:       title,
		Authors:     strings.TrimSpace(authors),
		Format:      formatLabel,
		Summary:     summary,
		Annotations: items,
	}
}

func annotationExportTimestamp(unix int64) annotationExportTime {
	if unix <= 0 {
		return annotationExportTime{}
	}
	value := time.Unix(unix, 0).UTC()
	return annotationExportTime{
		ISO:   value.Format(time.RFC3339),
		Human: value.Format("2 Jan 2006, 15:04 UTC"),
	}
}

func writeAnnotationExport(w io.Writer, document annotationExportDocument) error {
	return annotationExportTemplate.Execute(w, document)
}

func writeAnnotationMarkdown(w io.Writer, document annotationExportDocument) error {
	var export strings.Builder
	fmt.Fprintf(&export, "# %s\n\n", escapeMarkdownText(document.Title))
	if document.Authors != "" {
		fmt.Fprintf(&export, "%s\n\n", escapeMarkdownText(document.Authors))
	}
	export.WriteString(escapeMarkdownText(document.Summary))
	if document.Format != "" {
		fmt.Fprintf(&export, " · %s", escapeMarkdownText(document.Format))
	}
	export.WriteString("\n")

	if len(document.Annotations) == 0 {
		export.WriteString("\n_No highlights or notes for this file._\n")
	} else {
		for index, annotation := range document.Annotations {
			fmt.Fprintf(&export, "\n## Highlight %d\n\n", index+1)
			writeMarkdownQuote(&export, annotation.Quote)
			if annotation.Note != "" {
				export.WriteString("\n### Note\n\n")
				export.WriteString(escapeMarkdownText(annotation.Note))
				export.WriteString("\n")
			}
			fmt.Fprintf(&export, "\nHighlighted %s", escapeMarkdownText(annotation.Created.Human))
			if annotation.Updated.Human != "" {
				fmt.Fprintf(&export, " · Updated %s", escapeMarkdownText(annotation.Updated.Human))
			}
			fmt.Fprintf(&export, "\n\nSource: %s\n", markdownCodeSpan(annotation.CFI))
		}
	}
	export.WriteString("\n---\n\nExported from polka\n")
	_, err := io.WriteString(w, export.String())
	return err
}

func writeMarkdownQuote(export *strings.Builder, quote string) {
	for line := range strings.SplitSeq(escapeMarkdownText(quote), "\n") {
		if line == "" {
			export.WriteString(">\n")
			continue
		}
		fmt.Fprintf(export, "> %s\n", line)
	}
}

func escapeMarkdownText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"[", "\\[",
		"]", "\\]",
		"<", "&lt;",
		">", "&gt;",
	).Replace(value)
}

func markdownCodeSpan(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(value)
	longestRun := 0
	currentRun := 0
	for _, char := range value {
		if char == '`' {
			currentRun++
			longestRun = max(longestRun, currentRun)
		} else {
			currentRun = 0
		}
	}
	delimiter := strings.Repeat("`", longestRun+1)
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") ||
		strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return delimiter + " " + value + " " + delimiter
	}
	return delimiter + value + delimiter
}

func annotationExportFilename(title, extension string) string {
	base := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return ' '
		}
		return r
	}, title)
	base = strings.Join(strings.Fields(base), " ")
	if base == "" {
		base = "Book"
	}
	return base + " - highlights." + extension
}
