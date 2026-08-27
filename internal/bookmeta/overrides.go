package bookmeta

import "encoding/json/v2"

// ParseOverrides decodes the works.manual_overrides JSON column: the set of
// field names the user has manually edited.
// A blank or malformed value yields an empty (non-nil) map.
func ParseOverrides(s string) map[string]bool {
	m := make(map[string]bool)
	if s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

// MarshalOverrides encodes an overrides set back to JSON for storage.
func MarshalOverrides(m map[string]bool) string {
	b, _ := json.Marshal(m, json.Deterministic(true))
	return string(b)
}
