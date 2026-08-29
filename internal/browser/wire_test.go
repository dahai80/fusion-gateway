package browser

import (
    "encoding/json"
    "strings"
    "testing"
)

func TestCreateSessionRequestSnakeCase(t *testing.T) {
    url := "https://example.com"
    max := 100
    req := CreateSessionRequest{
        Mode:             WebModeHeaded,
        InitialURL:       &url,
        MaxActions:       &max,
        CredentialDomain: &url,
    }
    b, err := json.Marshal(req)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    for _, key := range []string{`"mode"`, `"initial_url"`, `"max_actions"`, `"credential_domain"`} {
        if !strings.Contains(string(b), key) {
            t.Fatalf("missing snake_case key %s in %s", key, b)
        }
    }
    if strings.Contains(string(b), `"initialUrl"`) {
        t.Fatalf("camelCase leaked: %s", b)
    }
}

func TestCreateSessionRequestOmitEmpty(t *testing.T) {
    req := CreateSessionRequest{Mode: WebModeHeadless}
    b, err := json.Marshal(req)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    for _, key := range []string{`"initial_url"`, `"max_actions"`, `"credential_domain"`} {
        if strings.Contains(string(b), key) {
            t.Fatalf("optional field %s should be omitted for zero value: %s", key, b)
        }
    }
    if !strings.Contains(string(b), `"mode":"headless"`) {
        t.Fatalf("mode not encoded: %s", b)
    }
}

func TestCreateSessionResponseDecode(t *testing.T) {
    raw := `{"session_id":"sess-1","credential_injected":true}`
    var resp CreateSessionResponse
    if err := json.Unmarshal([]byte(raw), &resp); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if resp.SessionID != "sess-1" || !resp.CredentialInjected {
        t.Fatalf("decoded wrong: %+v", resp)
    }
}

func TestFBNodeCapacitySnakeCase(t *testing.T) {
    raw := `{"node_id":"nb-1","max_sessions":4,"live_sessions":2,"max_total_memory_mb":16384,"free_memory_mb":8000,"ram_gb":16}`
    var cap FBNodeCapacity
    if err := json.Unmarshal([]byte(raw), &cap); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if cap.NodeID != "nb-1" || cap.MaxSessions != 4 || cap.LiveSessions != 2 {
        t.Fatalf("decoded wrong: %+v", cap)
    }
    if cap.MaxTotalMemoryMB != 16384 || cap.FreeMemoryMB != 8000 || cap.RamGB != 16 {
        t.Fatalf("decoded wrong: %+v", cap)
    }
}

func TestFBErrorDecode(t *testing.T) {
    raw := `{"code":"quota_exceeded","message":"no slots","retryable":false}`
    var fe FBError
    if err := json.Unmarshal([]byte(raw), &fe); err != nil {
        t.Fatalf("unmarshal: %v", err)
    }
    if fe.Code != ErrCodeQuotaExceeded || fe.Message != "no slots" || fe.IsRetryable() {
        t.Fatalf("decoded wrong: %+v", fe)
    }
}

func TestBrowserActionRequestActionType(t *testing.T) {
    text := "hello"
    req := BrowserActionRequest{
        SessionID:   "sess-1",
        Action:      ActionTypeText,
        PayloadText: &text,
    }
    b, err := json.Marshal(req)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if !strings.Contains(string(b), `"action":"type_text"`) {
        t.Fatalf("action raw value wrong: %s", b)
    }
    if !strings.Contains(string(b), `"payload_text":"hello"`) {
        t.Fatalf("payload_text wrong: %s", b)
    }
}

func TestRequestFrameEnvelope(t *testing.T) {
    frame := RequestFrame{
        Type:      reqTypeExecute,
        Payload:   json.RawMessage(`{"action":"click"}`),
        SessionID: "sess-1",
    }
    b, err := json.Marshal(frame)
    if err != nil {
        t.Fatalf("marshal: %v", err)
    }
    if !strings.Contains(string(b), `"type":"execute"`) {
        t.Fatalf("type wrong: %s", b)
    }
    if !strings.Contains(string(b), `"session_id":"sess-1"`) {
        t.Fatalf("session_id wrong: %s", b)
    }
    // payload should be passed through as raw JSON object, not base64.
    if !strings.Contains(string(b), `"payload":{"action":"click"}`) {
        t.Fatalf("payload not raw: %s", b)
    }
}

func TestErrorCodesStable(t *testing.T) {
    // Wire contract: these exact strings are emitted by fusion-browser
    // ErrorModel.swift. A drift would break the gateway's branching.
    want := map[string]string{
        ErrCodeNodeStale:       "node_stale",
        ErrCodeQuotaExceeded:   "quota_exceeded",
        ErrCodeSessionNotFound: "session_not_found",
        ErrCodeSessionClosing:  "session_closing",
        ErrCodeTimeout:         "timeout",
        ErrCodeNavigateFailed:  "navigate_failed",
    }
    for got, exp := range want {
        if got != exp {
            t.Fatalf("error code constant drift: const %q != expected %q", got, exp)
        }
    }
}
