package web

import (
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net/http"
	"strconv"
)

const (
	defaultCollectionPageSize = 200
	maxCollectionPageSize     = 200
	maxCollectionCursorBytes  = 4096
)

type collectionCursor struct {
	Version int    `json:"v"`
	Kind    string `json:"k"`
	Primary string `json:"p"`
	Tie     string `json:"t,omitempty"`
	Filter  string `json:"f,omitempty"`
}

func collectionPageSize(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultCollectionPageSize, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid limit")
	}
	if n > maxCollectionPageSize {
		n = maxCollectionPageSize
	}
	return n, nil
}

func encodeCollectionCursor(cursor collectionCursor) string {
	cursor.Version = 1
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCollectionCursor(raw, kind, filter string) (collectionCursor, error) {
	if raw == "" {
		return collectionCursor{}, nil
	}
	if len(raw) > maxCollectionCursorBytes {
		return collectionCursor{}, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return collectionCursor{}, errors.New("invalid cursor")
	}
	var cursor collectionCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Version != 1 || cursor.Kind != kind || cursor.Filter != filter {
		return collectionCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}
