package db

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

const maxReaderLocatorBytes = 16 * 1024

type ReaderLocator []byte

func EmptyReaderLocator() ReaderLocator {
	return ReaderLocator(`{}`)
}

func NewReaderLocator(raw []byte) (ReaderLocator, error) {
	return normalizeReaderLocator(raw)
}

func ReaderLocatorFromString(raw string) (ReaderLocator, error) {
	return normalizeReaderLocator([]byte(raw))
}

func (locator ReaderLocator) MarshalJSON() ([]byte, error) {
	if len(locator) == 0 {
		return []byte(`{}`), nil
	}
	return []byte(locator), nil
}

func (locator *ReaderLocator) UnmarshalJSON(raw []byte) error {
	normalized, err := NewReaderLocator(raw)
	if err != nil {
		return err
	}
	*locator = normalized
	return nil
}

func (locator ReaderLocator) String() string {
	if len(locator) == 0 {
		return "{}"
	}
	return string(locator)
}

func normalizeReaderLocator(raw []byte) (ReaderLocator, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxReaderLocatorBytes {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader locator must be a JSON object")
	}

	var value map[string]jsontext.Value
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, errorWithDetail(ErrInvalidReaderInput, "reader locator must be a JSON object")
	}
	canonical, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("normalize reader locator: %w", err)
	}
	return ReaderLocator(canonical), nil
}
