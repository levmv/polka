package bookmeta

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var yearRegex = regexp.MustCompile(`\b(1[0-9]{3}|20[0-9]{2})\b`)

var (
	// YYYY-MM-DD or YYYY/MM/DD
	reISO = regexp.MustCompile(`^(\d{4})[-\/](\d{2})[-\/](\d{2})(?:T.*)?$`)
	// YYYY-MM or YYYY/MM
	reYM = regexp.MustCompile(`^(\d{4})[-\/](\d{2})$`)
	// YYYY
	reY = regexp.MustCompile(`^(\d{4})$`)
	// DD Month YYYY or Month DD, YYYY
	reText = regexp.MustCompile(`^(?i)(?:(\d{1,2})(?:st|nd|rd|th)?\s+)?([a-z]+)\s+(?:(\d{1,2})(?:st|nd|rd|th)?(?:,\s*|\s+))?(\d{4})$`)
)

func parseMonth(m string) (int, bool) {
	m = strings.ToLower(m)
	if len(m) < 3 {
		return 0, false
	}
	m = m[:3]
	months := map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	val, ok := months[m]
	return val, ok
}

// ParseDate parses a loose date string into a normalized value and precision.
func ParseDate(s string) (normalized string, precision string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}

	if m := reISO.FindStringSubmatch(s); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		if isValidDate(year, month, day) {
			return fmt.Sprintf("%04d-%02d-%02d", year, month, day), "day"
		}
	}

	if m := reYM.FindStringSubmatch(s); m != nil {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		if isValidDate(year, month, 1) {
			return fmt.Sprintf("%04d-%02d", year, month), "month"
		}
	}

	if m := reY.FindStringSubmatch(s); m != nil {
		year, _ := strconv.Atoi(m[1])
		if year >= 1000 && year <= 2099 {
			return fmt.Sprintf("%04d", year), "year"
		}
	}

	if m := reText.FindStringSubmatch(s); m != nil {
		year, _ := strconv.Atoi(m[4])
		monthStr := m[2]

		month, ok := parseMonth(monthStr)
		if !ok {
			return "", ""
		}

		dayStr := m[1]
		if dayStr == "" {
			dayStr = m[3]
		}

		if dayStr == "" {
			if isValidDate(year, month, 1) {
				return fmt.Sprintf("%04d-%02d", year, month), "month"
			}
		} else {
			day, _ := strconv.Atoi(dayStr)
			if isValidDate(year, month, day) {
				return fmt.Sprintf("%04d-%02d-%02d", year, month, day), "day"
			}
		}
	}

	return "", ""
}

func isValidDate(y, m, d int) bool {
	if y < 1000 || y > 9999 {
		return false
	}
	if m < 1 || m > 12 {
		return false
	}
	if d < 1 || d > 31 {
		return false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == m && t.Day() == d
}

// FormatDateHuman renders a stored/normalized value at its precision.
func FormatDateHuman(s string) string {
	if s == "" {
		return ""
	}
	norm, prec := ParseDate(s)
	if prec == "" {
		return s
	}

	switch prec {
	case "year":
		return norm
	case "month":
		t, _ := time.Parse("2006-01", norm)
		return t.Format("January 2006")
	case "day":
		t, _ := time.Parse("2006-01-02", norm)
		return t.Format("2 January 2006")
	}
	return s
}

// NormalizeMetadataDate maps a raw metadata date (from a book file or sidecar)
// onto a value we are willing to store: the ParseDate normal form if recognized,
// else a best-effort 4-digit year, else empty. Rejecting to empty is deliberate —
// better no date than leaking a garbage string like "0101-01-01T00:00:00+00:00"
// (seen on real litres EPUBs) into the UI.
func NormalizeMetadataDate(raw string) string {
	if norm, prec := ParseDate(raw); prec != "" {
		return norm
	}
	return FormatYear(raw)
}

// FormatYear extracts the first plausible 4-digit year from a date string.
func FormatYear(date string) string {
	norm, prec := ParseDate(date)
	if prec != "" {
		return norm[:4]
	}
	match := yearRegex.FindString(date)
	if match != "" {
		return match
	}
	return ""
}
