//go:build windows

package fsprofile

import "testing"

func TestIsUNCPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{`\\nas\books`, true},
		{`//nas/books`, true},
		{`\\?\UNC\nas\books`, true},
		{`\\.\UNC\nas\books`, true},
		{`\\?\C:\books`, false},
		{`\\.\C:\books`, false},
		{`C:\books`, false},
	}
	for _, tt := range tests {
		if got := isUNCPath(tt.path); got != tt.want {
			t.Fatalf("isUNCPath(%q) = %v; want %v", tt.path, got, tt.want)
		}
	}
}
