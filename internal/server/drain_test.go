package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
)

// #449: once shutdown begins (SetDraining from the SIGTERM handler), the
// health probes must tell the truth so start.sh / supervisor / operator
// curls can distinguish a serving gateway from one that is draining and
// about to release the port.

func TestHealthzReturns503WhenDraining(t *testing.T) {
    s := newTestServer()
    s.SetDraining()

    req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
    rec := httptest.NewRecorder()
    s.handleHealthz(rec, req)

    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("expected 503 while draining, got %d", rec.Code)
    }
    var body map[string]interface{}
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("invalid json: %v", err)
    }
    if body["status"] != "draining" {
        t.Errorf("expected status draining, got %v", body["status"])
    }
}

func TestHealthzOKWhenNotDraining(t *testing.T) {
    s := newTestServer()

    req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
    rec := httptest.NewRecorder()
    s.handleHealthz(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("expected 200 when serving, got %d", rec.Code)
    }
    if rec.Body.String() != "ok" {
        t.Errorf("expected ok body, got %s", rec.Body.String())
    }
}

func TestReadyzReturns503WhenDraining(t *testing.T) {
    s := newTestServer()
    s.SetDraining()

    req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
    rec := httptest.NewRecorder()
    s.handleReadyz(rec, req)

    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("expected 503 while draining, got %d", rec.Code)
    }
    var body map[string]interface{}
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("invalid json: %v", err)
    }
    if body["status"] != "draining" {
        t.Errorf("expected status draining, got %v", body["status"])
    }
}

func TestLivezStays200WhenDraining(t *testing.T) {
    s := newTestServer()
    s.SetDraining()

    req := httptest.NewRequest(http.MethodGet, "/livez", nil)
    rec := httptest.NewRecorder()
    s.handleLivez(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("livez must stay 200 while draining (process is alive), got %d", rec.Code)
    }
    if rec.Body.String() != "alive" {
        t.Errorf("expected alive body, got %s", rec.Body.String())
    }
}

func TestSetDrainingIdempotent(t *testing.T) {
    s := newTestServer()
    s.SetDraining()
    s.SetDraining()
    if !s.IsDraining() {
        t.Fatal("expected IsDraining true after SetDraining")
    }
}
