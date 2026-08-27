package web

import (
	"errors"
	"net/http"
)

func parseLimitedMultipartForm(w http.ResponseWriter, r *http.Request, maxBodyBytes, maxMemoryBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseMultipartForm(maxMemoryBytes); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "Multipart body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "File too large or bad form", http.StatusBadRequest)
		}
		return false
	}
	return true
}
