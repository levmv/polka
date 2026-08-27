package bookmeta

import "testing"

func TestNormalizeLanguage(t *testing.T) {
	cases := []struct{ in, want string }{
		{"en", "en"},
		{"EN", "en"},
		{" en ", "en"},
		{"eng", "en"}, // 3-letter ISO 639-2
		{"en-US", "en-US"},
		{"EN-us", "en-US"},
		{"pt_BR", "pt-BR"},
		{"fra-be", "fr-BE"},
		{"zh_hans", "zh-Hans"},
		{"zh-hant-hk", "zh-Hant-HK"},
		{"rus", "ru"},
		{"ger", "de"}, // bibliographic /B
		{"deu", "de"}, // terminological /T
		{"jpn", "ja"},
		{"jp", "ja"}, // common bad primary tag
		{"und", ""},
		{"English", ""}, // language names are a future explicit alias layer
		{"", ""},
		{"xx", "xx"},    // structurally valid unknown code preserved
		{"tlh", "tlh"},  // structurally valid 3-letter code preserved
		{"klingon", ""}, // not a language code
		{"en--US", ""},
	}
	for _, c := range cases {
		if got := NormalizeLanguage(c.in); got != c.want {
			t.Errorf("NormalizeLanguage(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestLanguageName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"en", "English"},
		{"eng", "English"}, // tolerates a non-canonical stored code
		{"en-US", "English (US)"},
		{"zh-Hans", "Chinese (Hans)"},
		{"ru", "Russian"},
		{"RU", "Russian"},
		{"", ""},
		{"xx", "xx"}, // unknown shown as-is
		{"English", "English"},
	}
	for _, c := range cases {
		if got := LanguageName(c.in); got != c.want {
			t.Errorf("LanguageName(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
