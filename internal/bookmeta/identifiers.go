package bookmeta

import (
	"strings"
	"uuid"
)

type Identifier struct {
	Type  string
	Value string
}

// looksLikeUUID reports whether value is a UUID in any RFC 9562 text form.
// Book UUIDs are internal record ids, not useful external identifiers.
func looksLikeUUID(value string) bool {
	_, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil
}

// IsInternalIdentifier reports whether an identifier is internal noise that
// should not be exposed: an explicitly internal scheme (uuid/calibre) or any
// UUID-shaped value, whatever scheme it arrived under (epub `unique-identifier`,
// litres `id:` ids, etc.).
func IsInternalIdentifier(id Identifier) bool {
	return IsInternalIdentifierScheme(id.Type) || looksLikeUUID(id.Value)
}

func ParseIdentifiers(s string) []Identifier {
	var ids []Identifier
	parts := strings.SplitSeq(s, ",")
	for p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if id := identifierFromUserToken(p); id.Type != "" || id.Value != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func identifierFromUserToken(value string) Identifier {
	lower := strings.ToLower(value)
	if doi, ok := doiFromURL(value); ok {
		return Identifier{Type: "doi", Value: doi}
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return Identifier{Type: "url", Value: value}
	}

	if id, ok := identifierFromLabelledToken(value); ok {
		return id
	}

	before, after, ok := strings.Cut(value, ":")
	if !ok {
		return Identifier{Type: "isbn", Value: value}
	}

	typ := strings.TrimSpace(before)
	val := strings.TrimSpace(after)
	typLower := strings.ToLower(typ)
	if typLower == "urn" {
		nextIdx := strings.Index(val, ":")
		if nextIdx != -1 {
			typLower = strings.ToLower(strings.TrimSpace(val[:nextIdx]))
			val = strings.TrimSpace(val[nextIdx+1:])
		}
	}
	return Identifier{Type: typLower, Value: val}
}

func identifierFromLabelledToken(value string) (Identifier, bool) {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return Identifier{}, false
	}
	label := strings.ToLower(strings.Trim(fields[0], " :"))
	rest := strings.TrimSpace(strings.TrimPrefix(value, fields[0]))
	switch label {
	case "isbn", "isbn-10", "isbn-13":
		return Identifier{Type: "isbn", Value: rest}, true
	case "doi":
		return Identifier{Type: "doi", Value: rest}, true
	}
	return Identifier{}, false
}

func doiFromURL(value string) (string, bool) {
	lower := strings.ToLower(value)
	prefixes := []string{
		"https://doi.org/",
		"http://doi.org/",
		"https://dx.doi.org/",
		"http://dx.doi.org/",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(lower, prefix) {
			doi := strings.TrimSpace(value[len(prefix):])
			if cut := strings.IndexAny(doi, "?#"); cut >= 0 {
				doi = doi[:cut]
			}
			return doi, doi != ""
		}
	}
	return "", false
}

// IsInternalIdentifierScheme returns true for tool-private identifier schemes
// that should not be exposed to the user.
func IsInternalIdentifierScheme(typ string) bool {
	switch typ {
	case "uuid", "calibre":
		return true
	}
	return false
}

func FormatIdentifiers(ids []Identifier) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, id.Type+":"+id.Value)
	}
	return strings.Join(parts, ", ")
}

func ValidISBN(value string) bool {
	clean := strings.ReplaceAll(value, "-", "")
	clean = strings.ReplaceAll(clean, " ", "")

	if len(clean) == 10 {
		var sum int
		for i := range 9 {
			if clean[i] < '0' || clean[i] > '9' {
				return false
			}
			sum += int(clean[i]-'0') * (10 - i)
		}
		last := clean[9]
		var check int
		if last == 'X' || last == 'x' {
			check = 10
		} else if last >= '0' && last <= '9' {
			check = int(last - '0')
		} else {
			return false
		}
		sum += check * 1
		return sum%11 == 0
	} else if len(clean) == 13 {
		var sum int
		for i := range 13 {
			if clean[i] < '0' || clean[i] > '9' {
				return false
			}
			weight := 1
			if i%2 != 0 {
				weight = 3
			}
			sum += int(clean[i]-'0') * weight
		}
		return sum%10 == 0
	}
	return false
}

// IdentifierFromOPF maps an EPUB OPF identifier (scheme and raw value) to a typed Identifier.
func IdentifierFromOPF(scheme, value string) Identifier {
	value = strings.TrimSpace(value)
	if value == "" {
		return Identifier{}
	}

	var typ string
	if scheme != "" {
		typ = strings.ToLower(strings.TrimSpace(scheme))
	}

	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "urn:isbn:") {
		if typ == "" {
			typ = "isbn"
		}
		value = strings.TrimSpace(value[9:])
	} else if strings.HasPrefix(lower, "isbn:") {
		if typ == "" {
			typ = "isbn"
		}
		value = strings.TrimSpace(value[5:])
	} else if strings.HasPrefix(lower, "urn:doi:") {
		if typ == "" {
			typ = "doi"
		}
		value = strings.TrimSpace(value[8:])
	} else if strings.HasPrefix(lower, "doi:") {
		if typ == "" {
			typ = "doi"
		}
		value = strings.TrimSpace(value[4:])
	} else if strings.HasPrefix(lower, "amazon:") {
		if typ == "" {
			typ = "amazon"
		}
		value = strings.TrimSpace(value[7:])
	} else if strings.HasPrefix(lower, "asin:") {
		if typ == "" {
			typ = "amazon"
		}
		value = strings.TrimSpace(value[5:])
	} else if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if typ == "" {
			typ = "url"
		}
	} else if typ == "" {
		if labelled := identifierFromPlainOPFValue(value); labelled.Type != "" {
			typ = labelled.Type
			value = labelled.Value
		}
	}

	if typ == "" {
		if ValidISBN(value) {
			typ = "isbn"
		} else {
			typ = "unknown"
		}
	}

	return Identifier{Type: typ, Value: value}
}

func identifierFromPlainOPFValue(value string) Identifier {
	idx := strings.IndexByte(value, ':')
	if idx <= 0 {
		return Identifier{}
	}
	typ := strings.ToLower(strings.TrimSpace(value[:idx]))
	if !validIdentifierType(typ) || typ == "urn" || typ == "http" || typ == "https" {
		return Identifier{}
	}
	rest := strings.TrimSpace(value[idx+1:])
	if rest == "" {
		return Identifier{}
	}
	return Identifier{Type: typ, Value: rest}
}

func validIdentifierType(typ string) bool {
	if typ == "" {
		return false
	}
	for _, r := range typ {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}
