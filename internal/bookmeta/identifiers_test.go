package bookmeta

import (
	"reflect"
	"testing"
)

func TestParseIdentifiers(t *testing.T) {
	tests := []struct {
		input    string
		expected []Identifier
	}{
		{"", nil},
		{"   ", nil},
		{"isbn:123, doi:10.x", []Identifier{{"isbn", "123"}, {"doi", "10.x"}}},
		{"barevalue", []Identifier{{"isbn", "barevalue"}}},
		{"  type:  value  ", []Identifier{{"type", "value"}}},
		{"amazon:B000:123", []Identifier{{"amazon", "B000:123"}}},
		{",,", nil},
		{"TYPE:val", []Identifier{{"type", "val"}}},
		{"urn:isbn:9780123456789", []Identifier{{"isbn", "9780123456789"}}},
		{"URN:doi:10.1234", []Identifier{{"doi", "10.1234"}}},
		{"ISBN 978-0-306-40615-7", []Identifier{{"isbn", "978-0-306-40615-7"}}},
		{"ISBN-13: 978-0-306-40615-7", []Identifier{{"isbn", "978-0-306-40615-7"}}},
		{"DOI 10.1000/182", []Identifier{{"doi", "10.1000/182"}}},
		{"https://doi.org/10.1000/182?tracked=true", []Identifier{{"doi", "10.1000/182"}}},
		{"https://example.org/books/ref", []Identifier{{"url", "https://example.org/books/ref"}}},
	}

	for _, tt := range tests {
		got := ParseIdentifiers(tt.input)
		if len(got) == 0 && len(tt.expected) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tt.expected) {
			t.Errorf("ParseIdentifiers(%q) = %v; want %v", tt.input, got, tt.expected)
		}
	}
}

func TestIsInternalIdentifier(t *testing.T) {
	tests := []struct {
		id   Identifier
		want bool
	}{
		{Identifier{"isbn", "9780306406157"}, false},
		{Identifier{"amazon", "0679728732"}, false},
		{Identifier{"google", "wvqYx2ii3fsC"}, false},
		{Identifier{"uuid", "anything"}, true},                                         // internal scheme
		{Identifier{"calibre", "45"}, true},                                            // internal scheme
		{Identifier{"unknown", "urn:uuid:2d538d20-5e91-4b4f-b6c2-2cb3d0a1674b"}, true}, // scheme-less epub id
		{Identifier{"id", "5ad02664-23aa-102c-96f3-af3a14b75ca4"}, true},               // litres uuid under "id"
		{Identifier{"id", "5ad0266423aa102c96f3af3a14b75ca4"}, true},                   // compact RFC form
		{Identifier{"id", "{5AD02664-23AA-102C-96F3-AF3A14B75CA4}"}, true},             // braced, uppercase form
		{Identifier{"unknown", "not-a-uuid"}, false},
	}
	for _, tt := range tests {
		if got := IsInternalIdentifier(tt.id); got != tt.want {
			t.Errorf("IsInternalIdentifier(%+v) = %v; want %v", tt.id, got, tt.want)
		}
	}
}

func TestFormatIdentifiers(t *testing.T) {
	ids := []Identifier{{"isbn", "123"}, {"doi", "10.x"}}
	want := "isbn:123, doi:10.x"
	got := FormatIdentifiers(ids)
	if got != want {
		t.Errorf("FormatIdentifiers() = %q; want %q", got, want)
	}
}

func TestValidISBN(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"0-306-40615-2", true},
		{"0306406152", true},
		{"0-306-40615-X", false},
		{"0-8044-2957-X", true},
		{"978-0-306-40615-7", true},
		{"9780306406157", true},
		{"978-0-306-40615-8", false},
		{"garbage", false},
		{"123456789", false},
	}

	for _, tt := range tests {
		if got := ValidISBN(tt.input); got != tt.valid {
			t.Errorf("ValidISBN(%q) = %v; want %v", tt.input, got, tt.valid)
		}
	}
}

func TestIdentifierFromOPF(t *testing.T) {
	tests := []struct {
		scheme   string
		value    string
		expected Identifier
	}{
		{"ISBN", "978-1-2345-6789-0", Identifier{"isbn", "978-1-2345-6789-0"}},
		{"isbn", "urn:isbn:978-1-2345-6789-0", Identifier{"isbn", "978-1-2345-6789-0"}},
		{"", "urn:isbn:9780306406157", Identifier{"isbn", "9780306406157"}},
		{"", "ISBN: 9780306406157", Identifier{"isbn", "9780306406157"}},
		{"", "urn:doi:10.1000/182", Identifier{"doi", "10.1000/182"}},
		{"", "doi:10.1000/182", Identifier{"doi", "10.1000/182"}},
		{"", "amazon:B000123", Identifier{"amazon", "B000123"}},
		{"", "asin: B000123", Identifier{"amazon", "B000123"}},
		{"", "google:polka-writeback-probe", Identifier{"google", "polka-writeback-probe"}},
		{"", "vendor.id:abc-123", Identifier{"vendor.id", "abc-123"}},
		{"", "https://example.org/books/123", Identifier{"url", "https://example.org/books/123"}},
		{"", "9780306406157", Identifier{"isbn", "9780306406157"}},  // Bare ISBN
		{"", "randomstring", Identifier{"unknown", "randomstring"}}, // Bare non-ISBN
		{"", "   ", Identifier{"", ""}},                             // Empty
		{"calibre", "xyz", Identifier{"calibre", "xyz"}},
	}

	for _, tt := range tests {
		got := IdentifierFromOPF(tt.scheme, tt.value)
		if got != tt.expected {
			t.Errorf("IdentifierFromOPF(%q, %q) = %v; want %v", tt.scheme, tt.value, got, tt.expected)
		}
	}
}
