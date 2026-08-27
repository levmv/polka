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

func TestAppendUniqueTagList(t *testing.T) {
	got := appendUniqueTagList(
		[]string{"Existing"},
		[]string{"existing; New"},
		commaSemicolonSeparator,
		strings.TrimSpace,
	)
	want := []string{"Existing", "New"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("appendUniqueTagList = %#v; want %#v", got, want)
	}
}
