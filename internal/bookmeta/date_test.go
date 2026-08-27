package bookmeta

import (
	"testing"
)

func TestFormatYear(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1968-01-01T00:00:00+00:00", "1968"},
		{"1968", "1968"},
		{"Published in 2024", "2024"},
		{"No date here", ""},
		{"0999", ""},
		{"2100", ""}, // Plausible range 1000-2099 based on regex
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := FormatYear(tt.input)
			if got != tt.want {
				t.Errorf("FormatYear(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeMetadataDate(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"1951-06-01T00:00:00+00:00", "1951-06-01"}, // recognized -> normalized
		{"2024-03", "2024-03"},                      // month precision kept
		{"1987", "1987"},                            // bare year kept
		{"Published 1999 by Foo", "1999"},           // best-effort year
		{"0101-01-01T00:00:00+00:00", ""},           // garbage litres date -> rejected
		{"not a date", ""},                          // no year -> empty
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := NormalizeMetadataDate(tt.input); got != tt.want {
				t.Errorf("NormalizeMetadataDate(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	tests := []struct {
		input    string
		wantNorm string
		wantPrec string
	}{
		{"2026-04-30T19:57:11+00:00", "2026-04-30", "day"},
		{"2026-04-30", "2026-04-30", "day"},
		{"2026/04/30", "2026-04-30", "day"},
		{"2026-04", "2026-04", "month"},
		{"2026", "2026", "year"},
		{"April 2026", "2026-04", "month"},
		{"Apr 2026", "2026-04", "month"},
		{"30 April 2026", "2026-04-30", "day"},
		{"April 30, 2026", "2026-04-30", "day"},
		{"1st May 2026", "2026-05-01", "day"},
		{"   2026-04-30   ", "2026-04-30", "day"},
		{"2026-02-30", "", ""}, // invalid day
		{"2026-13-01", "", ""}, // invalid month
		{"999", "", ""},        // out of range year
		{"Garbage", "", ""},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotNorm, gotPrec := ParseDate(tt.input)
			if gotNorm != tt.wantNorm || gotPrec != tt.wantPrec {
				t.Errorf("ParseDate(%q) = %q, %q; want %q, %q", tt.input, gotNorm, gotPrec, tt.wantNorm, tt.wantPrec)
			}
		})
	}
}

func TestFormatDateHuman(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2026", "2026"},
		{"2026-04", "April 2026"},
		{"2026-04-30", "30 April 2026"},
		{"2026-04-30T19:57:11+00:00", "30 April 2026"},
		{"April 2026", "April 2026"}, // gets normalized to 2026-04, then formatted
		{"Garbage", "Garbage"},       // pass through unparseable
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := FormatDateHuman(tt.input)
			if got != tt.want {
				t.Errorf("FormatDateHuman(%q) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}
