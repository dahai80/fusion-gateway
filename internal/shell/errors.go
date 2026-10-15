package shell

import (
    "encoding/json"
    "net/http"
)

// Envelope is the standardized error body for /shell/* routes (PRD §7.2):
// the shell host renders a non-blocking toast on retryable errors instead of
// hanging on a bare 502. Backend-down NEVER leaks a raw proxy error.
type Envelope struct {
    Code      string `json:"code"`
    Message   string `json:"message"`
    ModuleID  string `json:"module_id,omitempty"`
    Retryable bool   `json:"retryable"`
}

// WriteEnvelope emits a JSON error envelope with the given HTTP status.
func WriteEnvelope(w http.ResponseWriter, status int, code, message, moduleID string, retryable bool) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(Envelope{
        Code:      code,
        Message:   message,
        ModuleID:  moduleID,
        Retryable: retryable,
    })
}
