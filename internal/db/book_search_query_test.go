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
		{"author qualifier", "author:asimov", `authors:"asimov"*`},
		{"single alphabetic character stays exact", "a", `-tag_keys:"a"`},
		{"single number stays exact", "1", `-tag_keys:"1"`},
		{"two-character term uses prefix", "ab", `-tag_keys:"ab"*`},
		{"single Han character uses prefix", "地", `-tag_keys:"地"*`},
		{"single kana character uses prefix", "ね", `-tag_keys:"ね"*`},
		{"series quoted", `series:"Foundation"`, `series:"Foundation"`},
		{"tag qualifier", "tag:scifi", `tags:"scifi"*`},
		{"title qualifier", "title:foo", `title:"foo"*`},
		{"mixed free and qualified", "author:asimov foundation", `authors:"asimov" -tag_keys:"foundation"*`},
		{"mixed free and qualified quoted", `author:asimov "foundation base"`, `authors:"asimov" -tag_keys:"foundation base"`},
		{"unfinished quoted phrase uses prefix", `"foundation ba`, `-tag_keys:"foundation ba"*`},
		{"malformed qualifier with no value is ignored", "author:", ""},
		{"malformed colon in text", "some:text", `-tag_keys:"some:text"*`},
		{"multiple qualifiers", "author:asimov tag:scifi", `authors:"asimov" tags:"scifi"*`},
		{"escaped quote stays one phrase", `series:"The ""Best"" Books"`, `series:"The ""Best"" Books"`},
		{"structural no filter has no FTS term", "no:cover", ""},
		{"structural no filter mixes with free text", "no:cover foundation", `-tag_keys:"foundation"*`},
		{"trailing structural filter completes free text", "foundation no:cover", `-tag_keys:"foundation"`},
		{"unsupported no filter is free text in lenient search", "no:format", `-tag_keys:"no:format"*`},
		{"status filter has no FTS term", "status:reading", ""},
		{"unsupported status is free text in lenient search", "status:paused", `-tag_keys:"status:paused"*`},
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
		{field: "tag", value: `sci-"fi"`, want: `tag_keys:"t4577904a8649a2a6c36a4250d68d0b60167aff6d446d1af7f4651f930f4a4faa"`},
		{field: "author", value: "Ursula K. Le Guin", want: `authors:"Ursula K. Le Guin"`},
	}
	for _, tt := range tests {
		term := QueryTerm(tt.field, tt.value)
		// Quotes inside a value must survive build → parse, including exact tags.
		// Otherwise a literal quote changes the selected books or breaks the feed.
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
