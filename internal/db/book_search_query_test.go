package db

import (
	"testing"
)

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"author qualifier", "author:asimov", `authors:"asimov"`},
		{"series quoted", `series:"Foundation"`, `series:"Foundation"`},
		{"tag qualifier", "tag:scifi", `tags:"scifi"`},
		{"title qualifier", "title:foo", `title:"foo"`},
		{"mixed free and qualified", "author:asimov foundation", `authors:"asimov" "foundation"`},
		{"mixed free and qualified quoted", `author:asimov "foundation base"`, `authors:"asimov" "foundation base"`},
		{"malformed qualifier with no value is ignored", "author:", ""},
		{"malformed colon in text", "some:text", `"some:text"`},
		{"multiple qualifiers", "author:asimov tag:scifi", `authors:"asimov" tags:"scifi"`},
		{"escaped quote stays one phrase", `series:"The ""Best"" Books"`, `series:"The ""Best"" Books"`},
		{"structural no filter has no FTS term", "no:cover", ""},
		{"structural no filter mixes with free text", "no:cover foundation", `"foundation"`},
		{"unsupported no filter is free text in lenient search", "no:format", `"no:format"`},
		{"status filter has no FTS term", "status:reading", ""},
		{"unsupported status is free text in lenient search", "status:paused", `"status:paused"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseQuery(tt.input)
			if got != tt.expected {
				t.Errorf("ParseQuery(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestQueryTermRoundTrips(t *testing.T) {
	tests := []struct {
		field string
		value string
		want  string
	}{
		{field: "series", value: `The "Best" Books`, want: `series:"The ""Best"" Books"`},
		{field: "title", value: `A "Quoted" Title`, want: `title:"A ""Quoted"" Title"`},
		{field: "tag", value: `sci-"fi"`, want: `tags:"sci-""fi"""`},
		{field: "author", value: "Ursula K. Le Guin", want: `authors:"Ursula K. Le Guin"`},
	}
	for _, tt := range tests {
		term := QueryTerm(tt.field, tt.value)
		// A quoted value must survive a build → parse cycle as a single phrase
		// (the FTS column carries the doubled quotes), otherwise a name with a
		// quote produces a broken query and an empty feed.
		if got := ParseQuery(term); got != tt.want {
			t.Errorf("ParseQuery(QueryTerm(%q, %q)) = %q; want %q", tt.field, tt.value, got, tt.want)
		}
	}
}

func TestValidateSearchQuery(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantValid bool
	}{
		{"free text", "foundation", true},
		{"qualified", "author:asimov tag:scifi", true},
		{"structural filter", "no:cover", true},
		{"structural filter with free text", "no:tags school", true},
		{"reading status filter", "status:finished", true},
		{"empty", "  ", false},
		{"missing qualifier value", "author:", false},
		{"unsupported structural filter", "no:format", false},
		{"unsupported reading status", "status:paused", false},
		{"missing qualifier value before next term", "author: tag:kids", false},
		{"unclosed quote", `series:"Foundation`, false},
		{"unknown qualifier remains free text", "format:epub", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateSearchQuery(tt.input)
			if got.Valid != tt.wantValid {
				t.Fatalf("Valid = %v; want %v; result = %+v", got.Valid, tt.wantValid, got)
			}
			if !tt.wantValid && got.Error == "" {
				t.Fatalf("invalid query returned empty error")
			}
		})
	}
}
