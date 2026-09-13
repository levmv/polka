package db

import (
	"database/sql/driver"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

var ErrInvalidLocator = errors.New("invalid locator")

// Locator identifies a position or selection within one asset. Reading positions
// with an empty locator fall back to progress. PDF rectangles use unrotated
// page coordinates, independent of zoom.
type Locator struct {
	CFI      string `json:"cfi,omitzero"`
	Path     string `json:"path,omitzero"`
	Fragment string `json:"fragment,omitzero"` // URI fragment without #, retained for external readers.
	Page     int    `json:"page,omitzero"`     // One-based PDF page number.
	Rects    []Rect `json:"rects,omitempty"`
}

type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (locator Locator) IsZero() bool {
	return locator.CFI == "" && locator.Path == "" && locator.Fragment == "" && locator.Page == 0 && len(locator.Rects) == 0
}

func (locator Locator) Equal(other Locator) bool {
	return locator.CFI == other.CFI && locator.Path == other.Path && locator.Fragment == other.Fragment && locator.Page == other.Page && slices.Equal(locator.Rects, other.Rects)
}

func normalizeLocator(locator Locator) (Locator, error) {
	locator.CFI = strings.TrimSpace(locator.CFI)
	if len(locator.CFI) > 2048 || len(locator.Path) > 4096 || len(locator.Fragment) > 4096 || locator.Page < 0 || len(locator.Rects) > 512 {
		return Locator{}, ErrInvalidLocator
	}
	if locator.CFI != "" && (!strings.HasPrefix(locator.CFI, "epubcfi(") || !strings.HasSuffix(locator.CFI, ")")) {
		return Locator{}, ErrInvalidLocator
	}
	if locator.Page > 0 && (locator.CFI != "" || locator.Path != "" || locator.Fragment != "") || len(locator.Rects) > 0 && locator.Page == 0 {
		return Locator{}, ErrInvalidLocator
	}
	for _, rect := range locator.Rects {
		for _, value := range []float64{rect.X, rect.Y, rect.Width, rect.Height, rect.X + rect.Width, rect.Y + rect.Height} {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return Locator{}, ErrInvalidLocator
			}
		}
		if rect.Width <= 0 || rect.Height <= 0 {
			return Locator{}, ErrInvalidLocator
		}
	}
	return locator, nil
}

// SQL reads decode system-written values; validation belongs to writes.
func (locator *Locator) Scan(value any) error {
	var raw []byte
	switch value := value.(type) {
	case string:
		raw = []byte(value)
	case []byte:
		raw = value
	default:
		return fmt.Errorf("read locator: unexpected SQL value %T", value)
	}
	var decoded Locator
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*locator = decoded
	return nil
}

func (locator Locator) Value() (driver.Value, error) {
	encoded, err := json.Marshal(locator)
	return string(encoded), err
}
