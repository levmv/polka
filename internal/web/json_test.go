package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadJSONTooLarge(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(`{"x":"`+strings.Repeat("a", maxJSONBodyBytes)+`"}`))
	w := httptest.NewRecorder()

	var dst struct {
		X string `json:"x"`
	}
	if readJSON(w, req, &dst) {
		t.Fatal("readJSON succeeded for oversized body")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusRequestEntityTooLarge, w.Body.String())
	}
}

func TestReadJSONStrictShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "valid with whitespace", body: "{\"x\":\"ok\"}  \n", want: http.StatusOK},
		{name: "unknown field", body: "{\"x\":\"ok\",\"typo\":true}", want: http.StatusBadRequest},
		{name: "case mismatched field", body: "{\"X\":\"ok\"}", want: http.StatusBadRequest},
		{name: "duplicate field", body: "{\"x\":\"first\",\"x\":\"second\"}", want: http.StatusBadRequest},
		{name: "invalid UTF-8", body: "{\"x\":\"ok\xff\"}", want: http.StatusBadRequest},
		{name: "trailing value", body: "{\"x\":\"ok\"} {\"x\":\"again\"}", want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(tt.body))
			w := httptest.NewRecorder()
			var dst struct {
				X string `json:"x"`
			}
			ok := readJSON(w, req, &dst)
			if tt.want == http.StatusOK {
				if !ok || dst.X != "ok" {
					t.Fatalf("readJSON = %v, dst = %+v; want success", ok, dst)
				}
				return
			}
			if ok || w.Code != tt.want {
				t.Fatalf("readJSON = %v, status = %d; want false/%d", ok, w.Code, tt.want)
			}
		})
	}
}

func TestSessionCookieSecure(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if sessionCookieSecure(req) {
		t.Fatal("plain HTTP request should not set Secure")
	}

	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	if !sessionCookieSecure(req) {
		t.Fatal("https forwarded request should set Secure")
	}

	req = httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	if !sessionCookieSecure(req) {
		t.Fatal("TLS request should set Secure")
	}
}
