package middleware

import (
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestRequestID_Existing(t *testing.T) {
    slog.Info("test RequestID_Existing")
    var capturedID string
    handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        capturedID, _ = r.Context().Value(RequestIDKey).(string)
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    req.Header.Set("X-Request-ID", "existing-id")
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if capturedID != "existing-id" {
        t.Fatalf("expected existing-id, got %s", capturedID)
    }
    if rec.Header().Get("X-Request-ID") != "existing-id" {
        t.Errorf("response header not set")
    }
}

func TestRequestID_Generated(t *testing.T) {
    slog.Info("test RequestID_Generated")
    var capturedID string
    handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        capturedID, _ = r.Context().Value(RequestIDKey).(string)
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if capturedID == "" {
        t.Fatal("expected generated request ID")
    }
    if rec.Header().Get("X-Request-ID") != capturedID {
        t.Errorf("response header mismatch")
    }
}

func TestConfigSnapshot(t *testing.T) {
    slog.Info("test ConfigSnapshot")
    snap := &config.ConfigSnapshot{Version: 42}
    var capturedVersion uint64
    handler := ConfigSnapshot(snap)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        s := config.SnapshotFromContext(r.Context())
        if s != nil {
            capturedVersion = s.Version
        }
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if capturedVersion != 42 {
        t.Fatalf("expected version 42, got %d", capturedVersion)
    }
    if rec.Header().Get("X-Config-Version") != "42" {
        t.Errorf("config version header not set")
    }
}

func TestCORS_Wildcard(t *testing.T) {
    slog.Info("test CORS_Wildcard")
    cfg := &config.CORSConfig{
        AllowedOrigins: []string{"*"},
        AllowedMethods: []string{"GET", "POST"},
        AllowedHeaders: []string{"Content-Type"},
    }
    handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
        t.Errorf("expected * origin, got %s", rec.Header().Get("Access-Control-Allow-Origin"))
    }
    if rec.Header().Get("Access-Control-Allow-Methods") != "GET, POST" {
        t.Errorf("unexpected methods header: %s", rec.Header().Get("Access-Control-Allow-Methods"))
    }
    if rec.Header().Get("Access-Control-Allow-Headers") != "Content-Type" {
        t.Errorf("unexpected headers header: %s", rec.Header().Get("Access-Control-Allow-Headers"))
    }
}

func TestCORS_SpecificOrigin(t *testing.T) {
    slog.Info("test CORS_SpecificOrigin")
    cfg := &config.CORSConfig{
        AllowedOrigins: []string{"https://example.com"},
    }
    handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
        t.Errorf("expected https://example.com, got %s", rec.Header().Get("Access-Control-Allow-Origin"))
    }
}

func TestCORS_Preflight(t *testing.T) {
    slog.Info("test CORS_Preflight")
    cfg := &config.CORSConfig{
        AllowedOrigins: []string{"*"},
        AllowedMethods: []string{"GET", "POST"},
        AllowedHeaders: []string{"Content-Type"},
    }
    var nextCalled bool
    handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        nextCalled = true
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodOptions, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if nextCalled {
        t.Error("next handler should not be called for OPTIONS")
    }
    if rec.Code != http.StatusNoContent {
        t.Fatalf("expected 204, got %d", rec.Code)
    }
}

func TestCORS_EmptyConfig(t *testing.T) {
    slog.Info("test CORS_EmptyConfig")
    cfg := &config.CORSConfig{}
    handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    req := httptest.NewRequest(http.MethodGet, "/test", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d", rec.Code)
    }
    if rec.Header().Get("Access-Control-Allow-Origin") != "" {
        t.Error("should not set origin header with empty config")
    }
}
