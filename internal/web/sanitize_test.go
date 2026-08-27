package web

import (
	"database/sql"
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/levmv/polka/internal/db"
)

func TestSanitizeHTML(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"script is removed", "hello <script>alert(1)</script> world", "hello  world"},
		{"allowed tags", "<p><b>bold</b></p>", "<p><b>bold</b></p>"},
		{"disallowed tags text kept", "<div>some text</div>", "some text"},
		{"attributes stripped except a[href]", `<a href="http://example.com" class="link" onclick="alert(1)">link</a>`, `<a href="http://example.com" rel="noopener noreferrer">link</a>`},
		{"unsafe href is stripped", `<a href="javascript:alert(1)">link</a>`, `<a>link</a>`},
		{"br tags", `line1<br>line2`, `line1<br>line2`},
		{"style stripped", `<style>body{color:red;}</style>text`, `text`},
		{"malformed html", `<p>unclosed`, `<p>unclosed</p>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeHTML(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeHTML(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBookDetailDescriptionSeparatesSourceFromDisplayHTML(t *testing.T) {
	const source = `<script>alert("keep as source")</script>`
	detail := detailRowDTO(db.BookDetailRow{Description: sql.NullString{String: source, Valid: true}})

	if detail.DescriptionSource == nil || *detail.DescriptionSource != source {
		t.Fatalf("description source = %v, want original", detail.DescriptionSource)
	}
	if detail.DescriptionHTML == nil || *detail.DescriptionHTML != "" {
		t.Fatalf("description HTML = %v, want sanitized empty string", detail.DescriptionHTML)
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	jsonText := string(raw)
	if strings.Contains(jsonText, `"description":`) {
		t.Fatalf("detail exposes ambiguous description field: %s", jsonText)
	}
	if !strings.Contains(jsonText, `"description_source":`) || !strings.Contains(jsonText, `"description_html":""`) {
		t.Fatalf("detail description forms = %s", jsonText)
	}
}
