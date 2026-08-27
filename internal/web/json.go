package web

import (
	"encoding/json/v2"
	"errors"
	"log"
	"net/http"

	"github.com/levmv/polka/internal/storage"
)

const maxJSONBodyBytes = 1 << 20

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return readLimitedJSON(w, r, dst, maxJSONBodyBytes)
}

func readLimitedJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := json.UnmarshalRead(r.Body, dst, json.RejectUnknownMembers(true)); err != nil {
		writeJSONDecodeError(w, err)
		return false
	}
	return true
}

func writeJSONDecodeError(w http.ResponseWriter, err error) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		http.Error(w, "JSON body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "Invalid JSON", http.StatusBadRequest)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, v)
}

func serverError(w http.ResponseWriter, err error) {
	// Both a missing books root and an empty root over an existing catalog (the
	// classic dropped-mount signal from RequireWritableRoot) mean the library is
	// unreachable, not a server fault: surface them as 503, not 500.
	if errors.Is(err, storage.ErrLayoutMissing) || errors.Is(err, storage.ErrRootEmpty) {
		log.Printf("storage unavailable: %v", err)
		http.Error(w, "Library storage is unavailable", http.StatusServiceUnavailable)
		return
	}
	log.Printf("internal server error: %v", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}
