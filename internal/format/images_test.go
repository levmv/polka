package format

import (
	"bytes"
	"image"
	"testing"

	"github.com/levmv/polka/internal/testfixture"
)

var tinyWebP = testfixture.WebP()

func TestCoverImageHelpersSupportWebP(t *testing.T) {
	ext, ok := coverImageExtensionFromBytes(tinyWebP)
	if !ok || ext != ".webp" {
		t.Fatalf("coverImageExtensionFromBytes(WebP) = %q/%v; want .webp/true", ext, ok)
	}
	ext, ok = coverImageExtensionFromContentType("image/webp; charset=binary")
	if !ok || ext != ".webp" {
		t.Fatalf("coverImageExtensionFromContentType(WebP) = %q/%v; want .webp/true", ext, ok)
	}
	ext, ok = coverImageExtensionFromFormatName("webp")
	if !ok || ext != ".webp" {
		t.Fatalf("coverImageExtensionFromFormatName(webp) = %q/%v; want .webp/true", ext, ok)
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(tinyWebP)); err != nil {
		t.Fatalf("DecodeConfig WebP: %v", err)
	}
}

func TestConventionalCoverHrefNormalization(t *testing.T) {
	tests := []struct {
		href string
		want bool
	}{
		{href: `images\cover.jpg`, want: true},
		{href: "./images//cover.png", want: true},
		{href: "images/cover.gif?cache=1#preview", want: true},
		{href: "images/cover%2Ejpeg", want: true},
		{href: "images/not-cover.jpg", want: false},
	}
	for _, tt := range tests {
		if got := isConventionalCoverHref(tt.href); got != tt.want {
			t.Errorf("isConventionalCoverHref(%q) = %v; want %v", tt.href, got, tt.want)
		}
	}
}
