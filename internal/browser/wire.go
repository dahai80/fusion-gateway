package browser

import "encoding/json"

// Wire contract for the gateway<->fusion-browser UDS plane. The Go types in
// this file are the byte-exact mirror of fusion-browser/Sources/FusionBrowser/
// Protocol.swift, Config.swift, ErrorModel.swift. fusion-browser's JSONEncoder
// uses .convertToSnakeCase, so the WIRE keys are snake_case (node_id,
// max_sessions, live_sessions, max_total_memory_mb, free_memory_mb, ram_gb,
// session_id, credential_injected, etc.). Go struct tags name the snake_case
// wire key directly — no converter — so a frame round-trips byte-for-byte.

// Request / response envelope type discriminants. Must match the Swift enum
// raw values (Protocol.swift ReqType/RespType).
const (
    reqTypeCreateSession = "create_session"
    reqTypeExecute       = "execute"
    reqTypeClose         = "close"
    reqTypeMetrics       = "metrics"
    reqTypeCapacity      = "capacity"

    respTypeCreateSession = "create_session"
    respTypeState         = "state"
    respTypeClosed        = "closed"
    respTypeMetrics       = "metrics"
    respTypeCapacity      = "capacity"
    respTypeError         = "error"
)

// RequestFrame is the top-level request envelope {type, payload, sessionId}.
// payload is held as json.RawMessage so the proxy can forward execute/close
// bodies VERBATIM (no re-encode round-trip — schema-drift safe). Only
// create_session + capacity are fully decoded by the caller.
type RequestFrame struct {
    Type      string          `json:"type"`
    Payload   json.RawMessage `json:"payload,omitempty"`
    SessionID string          `json:"session_id,omitempty"`
}

// ResponseFrame is the top-level response envelope {type, payload, sessionId}.
// payload is json.RawMessage for the same verbatim-forward reason; the caller
// decodes it into the typed struct matching Type when it needs the fields
// (create_session → CreateSessionResponse, capacity → FBNodeCapacity, error →
// FBError) or relays it raw otherwise.
type ResponseFrame struct {
    Type      string          `json:"type"`
    Payload   json.RawMessage `json:"payload,omitempty"`
    SessionID string          `json:"session_id,omitempty"`
}

// WebMode mirrors Protocol.swift WebMode: headless|headed.
type WebMode string

const (
    WebModeHeadless WebMode = "headless"
    WebModeHeaded   WebMode = "headed"
)

// ActionType mirrors Protocol.swift ActionType (snake_case raw values).
type ActionType string

const (
    ActionClick      ActionType = "click"
    ActionTypeText   ActionType = "type_text"
    ActionScroll     ActionType = "scroll"
    ActionNavigate   ActionType = "navigate"
    ActionScreenshot ActionType = "screenshot"
    ActionEvaluate   ActionType = "evaluate"
    ActionClose      ActionType = "close"
)

// CreateSessionRequest mirrors Protocol.swift CreateSessionRequest. Optional
// fields are pointers so an omitted field encodes to absence, not a zero
// value — matches Swift's nil default.
type CreateSessionRequest struct {
    Mode             WebMode `json:"mode"`
    InitialURL       *string `json:"initial_url,omitempty"`
    MaxActions       *int    `json:"max_actions,omitempty"`
    TaskTimeoutMs    *int    `json:"task_timeout_ms,omitempty"`
    CredentialDomain *string `json:"credential_domain,omitempty"`
}

// CreateSessionResponse mirrors Protocol.swift CreateSessionResponse.
type CreateSessionResponse struct {
    SessionID          string `json:"session_id"`
    CredentialInjected bool   `json:"credential_injected"`
}

// BrowserActionRequest mirrors Protocol.swift BrowserActionRequest. The proxy
// forwards this verbatim to the pinned node; the gateway decodes it only to
// validate the action type on ingress.
type BrowserActionRequest struct {
    SessionID    string     `json:"session_id"`
    Action       ActionType `json:"action"`
    TargetNodeID *string    `json:"target_node_id,omitempty"`
    PayloadText  *string    `json:"payload_text,omitempty"`
    ScrollDeltaY *float64   `json:"scroll_delta_y,omitempty"`
    TraceID      *string    `json:"trace_id,omitempty"`
}

// BrowserStateResponse mirrors Protocol.swift BrowserStateResponse. The proxy
// relays this VERBATIM (payload untouched) — the gateway does not interpret
// the AXTree/screenshot fields. Decode is provided for tests that assert the
// wire shape; production proxy uses raw passthrough.
type BrowserStateResponse struct {
    SessionID                  string             `json:"session_id"`
    URL                        string             `json:"url"`
    Title                      string             `json:"title"`
    AxTreeMarkdown             string             `json:"ax_tree_markdown"`
    InteractiveNodes           []json.RawMessage  `json:"interactive_nodes"`
    ScreenshotJpeg             []byte             `json:"screenshot_jpeg,omitempty"`
    ScreenshotPng              []byte             `json:"screenshot_png,omitempty"`
    HasSecurityInjectionBlocked bool              `json:"has_security_injection_blocked"`
    ExecutionTimeMs            int                `json:"execution_time_ms"`
    SecurityAudit              *json.RawMessage   `json:"security_audit,omitempty"`
    SessionRecovered           bool               `json:"session_recovered"`
    Error                      *FBError           `json:"error,omitempty"`
    TraceID                    *string            `json:"trace_id,omitempty"`
    EvaluateResult             *string            `json:"evaluate_result,omitempty"`
}

// MetricsResponse mirrors Protocol.swift MetricsResponse — opaque passthrough.
// counters and latency are [name,value] pairs the gateway does not interpret.
type MetricsResponse struct {
    Counters []json.RawMessage `json:"counters"`
    Latency  []json.RawMessage `json:"latency"`
}

// FBNodeCapacity mirrors Config.swift FBNodeCapacity — the placement signal.
// Wire keys are snake_case (node_id, max_sessions, live_sessions,
// max_total_memory_mb, free_memory_mb, ram_gb). This is the one response the
// registry fully decodes on every capacity poll.
type FBNodeCapacity struct {
    NodeID           string `json:"node_id"`
    MaxSessions      int    `json:"max_sessions"`
    LiveSessions     int    `json:"live_sessions"`
    MaxTotalMemoryMB int    `json:"max_total_memory_mb"`
    FreeMemoryMB     int    `json:"free_memory_mb"`
    RamGB            int    `json:"ram_gb"`
}

// FBError mirrors ErrorModel.swift FBError {code, message, retryable}. Relayed
// verbatim from the node — the gateway never masks a node's own error code
// (RC1 lesson: masked errors hide root cause).
type FBError struct {
    Code      string `json:"code"`
    Message   string `json:"message"`
    Retryable bool   `json:"retryable"`
}

// Known node error codes (ErrorModel.swift static instances). The gateway uses
// these to branch its own behavior (e.g. quota_exceeded → 503 not retryable)
// but relays the node's actual code/message regardless.
const (
    ErrCodeNodeStale       = "node_stale"
    ErrCodeQuotaExceeded   = "quota_exceeded"
    ErrCodeSessionNotFound = "session_not_found"
    ErrCodeSessionClosing  = "session_closing"
    ErrCodeTimeout         = "timeout"
    ErrCodeNavigateFailed  = "navigate_failed"
)

// IsRetryable reports whether a node error code is retryable per ErrorModel.
// Used by the proxy to decide whether to surface a retry hint to the caller.
func (e FBError) IsRetryable() bool { return e.Retryable }
