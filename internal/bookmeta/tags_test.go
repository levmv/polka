package bookmeta

import (
	"reflect"
	"testing"
)

func TestParseTagList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"blanks only", " , ,\t", nil},
		{"trim and split", "a, b ,c", []string{"a", "b", "c"}},
		{"drop empties", "a,,b,", []string{"a", "b"}},
		{"dedup case-insensitive keeps first", "Sci-Fi, sci-fi, SCI-FI, fantasy", []string{"Sci-Fi", "fantasy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseTagList(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseTagList(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyTagMode(t *testing.T) {
	tests := []struct {
		name    string
		current []string
		mode    TagMode
		values  []string
		want    []string
	}{
		{
			name:    "add appends new, keeps order",
			current: []string{"a", "b"},
			mode:    TagAdd,
			values:  []string{"c", "d"},
			want:    []string{"a", "b", "c", "d"},
		},
		{
			name:    "add skips existing case-insensitively",
			current: []string{"Sci-Fi", "b"},
			mode:    TagAdd,
			values:  []string{"sci-fi", "c"},
			want:    []string{"Sci-Fi", "b", "c"},
		},
		{
			name:    "add keeps typed casing for new tags",
			current: []string{"a"},
			mode:    TagAdd,
			values:  []string{"NewTag"},
			want:    []string{"a", "NewTag"},
		},
		{
			name:    "remove drops matches case-insensitively",
			current: []string{"a", "B", "c"},
			mode:    TagRemove,
			values:  []string{"b", "C"},
			want:    []string{"a"},
		},
		{
			name:    "remove of absent is a no-op",
			current: []string{"a", "b"},
			mode:    TagRemove,
			values:  []string{"z"},
			want:    []string{"a", "b"},
		},
		{
			name:    "replace sets exactly",
			current: []string{"a", "b", "c"},
			mode:    TagReplace,
			values:  []string{"x", "y"},
			want:    []string{"x", "y"},
		},
		{
			name:    "clear empties regardless of values",
			current: []string{"a", "b"},
			mode:    TagClear,
			values:  []string{"ignored"},
			want:    nil,
		},
		{
			name:    "values may carry embedded commas",
			current: nil,
			mode:    TagAdd,
			values:  []string{"a, b", "c"},
			want:    []string{"a", "b", "c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ApplyTagMode(tt.current, tt.mode, tt.values)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ApplyTagMode(%#v, %q, %#v) = %#v, want %#v",
					tt.current, tt.mode, tt.values, got, tt.want)
			}
		})
	}
}
