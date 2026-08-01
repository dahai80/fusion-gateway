package ui

import (
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestHandler_ReturnsIndex(t *testing.T) {
    h := Handler()
    req := httptest.NewRequest(http.MethodGet, "/", nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 for /, got %d", rec.Code)
    }
}

func TestHandler_SPAFallback(t *testing.T) {
    h := Handler()
    req := httptest.NewRequest(http.MethodGet, "/nonexistent-page", nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 for SPA fallback, got %d", rec.Code)
    }
}

func TestHandler_AssetsRoute(t *testing.T) {
    h := Handler()
    req := httptest.NewRequest(http.MethodGet, "/assets/nonexistent.js", nil)
    rec := httptest.NewRecorder()
    h.ServeHTTP(rec, req)
}
