package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseLimitedMultipartFormTooLarge(t *testing.T) {
	req := limitedMultipartRequest(t, "book", "book.epub", []byte(strings.Repeat("x", 128)))
	w := httptest.NewRecorder()

	if parseLimitedMultipartForm(w, req, req.ContentLength-1, 64) {
		t.Fatal("parseLimitedMultipartForm succeeded for oversized body")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
}

func TestParseLimitedMultipartFormBadForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	w := httptest.NewRecorder()

	if parseLimitedMultipartForm(w, req, 1024, 64) {
		t.Fatal("parseLimitedMultipartForm succeeded for bad form")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func limitedMultipartRequest(t *testing.T, field, filename string, content []byte) *http.Request {
	t.Helper()

	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/test", &b)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}
