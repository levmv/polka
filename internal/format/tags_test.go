package format

import (
	"reflect"
	"strings"
	"testing"
)

func TestUniqueTagList(t *testing.T) {
	got := uniqueTagList(
		[]string{" Alpha ; beta", "alpha, Gamma\tBeta", "\nDelta\r"},
		commaSemicolonNewlineTabSeparator,
		strings.TrimSpace,
	)
	want := []string{"Alpha", "beta", "Gamma", "Delta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueTagList = %#v; want %#v", got, want)
	}
}
