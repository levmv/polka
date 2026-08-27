package bookmeta

import "strings"

// Language metadata in the wild is inconsistent: files and sidecars may carry
// ISO 639-2-ish three-letter codes, BCP 47 tags, casing variants, or separator
// variants. Polka stores normalized BCP 47-ish tags without pulling in a full
// language registry: common ISO 639-2 codes are mapped to their ISO 639-1 primary
// language, separators/casing are canonicalized, and structurally non-code values
// are left empty.

// languageNames maps the canonical primary language subtag to an English display
// name for the UI. It is intentionally a display aid, not the validation source.
var languageNames = map[string]string{
	"en": "English", "ru": "Russian", "de": "German", "fr": "French",
	"es": "Spanish", "it": "Italian", "pt": "Portuguese", "nl": "Dutch",
	"pl": "Polish", "uk": "Ukrainian", "cs": "Czech", "sv": "Swedish",
	"no": "Norwegian", "da": "Danish", "fi": "Finnish", "ja": "Japanese",
	"zh": "Chinese", "ko": "Korean", "ar": "Arabic", "he": "Hebrew",
	"tr": "Turkish", "el": "Greek", "hu": "Hungarian", "ro": "Romanian",
	"bg": "Bulgarian", "sr": "Serbian", "hr": "Croatian", "sk": "Slovak",
	"sl": "Slovenian", "lt": "Lithuanian", "lv": "Latvian", "et": "Estonian",
	"ca": "Catalan", "eu": "Basque", "ga": "Irish", "is": "Icelandic",
	"fa": "Persian", "hi": "Hindi", "bn": "Bengali", "ta": "Tamil",
	"th": "Thai", "vi": "Vietnamese", "id": "Indonesian", "ms": "Malay",
	"la": "Latin",
}

// languageCodeAliases maps common legacy, bibliographic, terminological, and
// frequently-mistyped primary subtags to the BCP 47 primary subtag we store.
var languageCodeAliases = map[string]string{
	"eng": "en", "rus": "ru", "ger": "de", "deu": "de", "fre": "fr", "fra": "fr",
	"spa": "es", "ita": "it", "por": "pt", "dut": "nl", "nld": "nl", "pol": "pl",
	"ukr": "uk", "cze": "cs", "ces": "cs", "swe": "sv", "nor": "no", "dan": "da",
	"fin": "fi", "jpn": "ja", "chi": "zh", "zho": "zh", "kor": "ko", "ara": "ar",
	"heb": "he", "tur": "tr", "gre": "el", "ell": "el", "hun": "hu", "rum": "ro",
	"ron": "ro", "bul": "bg", "srp": "sr", "hrv": "hr", "slo": "sk", "slk": "sk",
	"slv": "sl", "lit": "lt", "lav": "lv", "est": "et", "cat": "ca", "baq": "eu",
	"eus": "eu", "gle": "ga", "ice": "is", "isl": "is", "per": "fa", "fas": "fa",
	"hin": "hi", "ben": "bn", "tam": "ta", "tha": "th", "vie": "vi", "ind": "id",
	"may": "ms", "msa": "ms", "lat": "la",
	// Common old/non-standard primary tags still found in metadata.
	"iw": "he", "ji": "yi", "in": "id", "jp": "ja",
}

// NormalizeLanguage canonicalizes a raw language code to a BCP 47-ish tag. It
// accepts common ISO 639-2 primary subtags (eng -> en), fixes separators/casing
// (pt_BR -> pt-BR, zh_hans -> zh-Hans), preserves script/region subtags, and
// returns empty for blank/undetermined/non-code values. Human language names are
// intentionally not guessed here; add explicit aliases only after corpus demand.
func NormalizeLanguage(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "_", "-")

	parts := strings.Split(s, "-")
	if len(parts) == 0 {
		return ""
	}
	primary := strings.ToLower(parts[0])
	if primary == "und" {
		return ""
	}
	if alias, ok := languageCodeAliases[primary]; ok {
		primary = alias
	}
	if !isLanguagePrimarySubtag(primary) {
		return ""
	}

	normalized := []string{primary}
	for _, part := range parts[1:] {
		if part == "" || !isLanguageSubtag(part) {
			return ""
		}
		normalized = append(normalized, normalizeLanguageSubtag(part))
	}
	return strings.Join(normalized, "-")
}

// LanguageName returns the English display name for a language code, or the code
// unchanged when it is not in the curated table. It tolerates non-canonical code
// input (e.g. a 3-letter code left by older data) by normalizing first.
func LanguageName(code string) string {
	c := strings.TrimSpace(code)
	if c == "" {
		return ""
	}
	n := NormalizeLanguage(c)
	if n == "" {
		return c
	}
	primary, rest, _ := strings.Cut(n, "-")
	name, ok := languageNames[primary]
	if !ok {
		return n
	}
	if rest == "" {
		return name
	}
	return name + " (" + rest + ")"
}

func isLanguagePrimarySubtag(s string) bool {
	if len(s) != 2 && len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

func isLanguageSubtag(s string) bool {
	if len(s) < 2 || len(s) > 8 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func normalizeLanguageSubtag(s string) string {
	s = strings.ToLower(s)
	if len(s) == 4 && isAlphaString(s) {
		return strings.ToUpper(s[:1]) + s[1:]
	}
	if len(s) == 2 && isAlphaString(s) {
		return strings.ToUpper(s)
	}
	return s
}

func isAlphaString(s string) bool {
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}
