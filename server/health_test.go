package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GO CONCEPT: httptest. NewRecorder is a fake ResponseWriter that records
// what a handler writes, and NewRequest builds a request without a network.
// That makes handler tests fast and simple: no server needed.

func TestHealth_OK(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	Health("1.2.3").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Health must never be cached, or a monitor could see a stale "ok".
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if body.Status != "ok" || body.Version != "1.2.3" {
		t.Errorf("body = %+v", body)
	}
}

func TestHealth_HEADHasNoBody(t *testing.T) {
	rec := httptest.NewRecorder()
	Health("1").ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD response should have no body, got %q", rec.Body.String())
	}
}

func TestHealth_OtherMethodsNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	Health("1").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET, HEAD" {
		t.Errorf("Allow = %q, want \"GET, HEAD\"", allow)
	}
}
