package middleware

import (
    "bufio"
    "context"
    "log/slog"
    "net"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type reqLogContextKey string

const RequestLogKey reqLogContextKey = "request_log"

type ResponseRecorder struct {
    http.ResponseWriter
    StatusCode int
    Size       int
    // written is true once WriteHeader or Write has been called (R2 audit fix:
    // lets the panic-recover path tell whether the handler already wrote a
    // response before panicking, so it doesn't double-write a 500 on top).
    written bool
}

func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
    return &ResponseRecorder{ResponseWriter: w, StatusCode: http.StatusOK}
}

// Written reports whether the response has started (header or body written).
func (r *ResponseRecorder) Written() bool {
    return r.written
}

func (r *ResponseRecorder) WriteHeader(code int) {
    r.StatusCode = code
    r.written = true
    r.ResponseWriter.WriteHeader(code)
}

func (r *ResponseRecorder) Write(b []byte) (int, error) {
    r.written = true
    size, err := r.ResponseWriter.Write(b)
    r.Size += size
    return size, err
}

func (r *ResponseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
    if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
        return hj.Hijack()
    }
    return nil, nil, http.ErrNotSupported
}

func (r *ResponseRecorder) Flush() {
    if fl, ok := r.ResponseWriter.(http.Flusher); ok {
        fl.Flush()
    }
}

func InitRequestLog(r *http.Request) *store.RequestLog {
    // RequestID note: this runs in withMiddleware BEFORE the RequestID
    // middleware injects the id into the (derived) context, so the ctx read
    // below is always empty on the first pass. The middleware also mirrors
    // the resolved id onto the inbound request header (R12), which IS visible
    // here — read the header first, ctx as fallback for direct callers.
    reqID := r.Header.Get("X-Request-ID")
    if reqID == "" {
        reqID, _ = r.Context().Value(RequestIDKey).(string)
    }
    entry := &store.RequestLog{
        RequestID:   reqID,
        RequestType: r.Method + " " + r.URL.Path,
        IsStream:    r.URL.Query().Get("stream") == "true",
        IsSuccess:   true,
    }
    if v := r.Header.Get("X-Fusion-Project-Id"); v != "" {
        entry.ProjectID = v
    }
    if v := r.Header.Get("X-Fusion-Chat-Id"); v != "" {
        entry.ChatID = v
    }
    if v := r.Header.Get("X-Space-Id"); v != "" {
        entry.SpaceID = v
    }
    return entry
}

func WithRequestLogContext(r *http.Request, entry *store.RequestLog) *http.Request {
    ctx := context.WithValue(r.Context(), RequestLogKey, entry)
    return r.WithContext(ctx)
}

func GetRequestLog(ctx context.Context) *store.RequestLog {
    entry, _ := ctx.Value(RequestLogKey).(*store.RequestLog)
    return entry
}

// StampAPIKeyName records the authenticated key's display name on the request
// log entry at the moment auth resolves it. Called from the auth middleware
// because the Principal it builds lives only in the derived request context —
// the outer withMiddleware defer reads the ORIGINAL request's context and can
// never see it (Go context propagation is inward-only), which is why every
// authenticated request was logged as "anonymous".
func StampAPIKeyName(ctx context.Context, keyName string) {
    if keyName == "" {
        return
    }
    if entry := GetRequestLog(ctx); entry != nil {
        entry.APIKeyName = keyName
    }
}

func FinalizeAndAppendLog(entry *store.RequestLog, st store.Store, start time.Time, keyName string) {
    entry.Timestamp = start
    entry.Latency = time.Since(start).Seconds()
    entry.IsSuccess = entry.StatusCode >= 200 && entry.StatusCode < 400
    // Key-name precedence: the entry value (stamped by the auth middleware
    // into the entry itself, see StampAPIKeyName) wins over the ctx-derived
    // keyName. The ctx read at the outermost withMiddleware defer cannot see
    // the Principal the auth middleware injected into the derived request
    // context (Go context propagation is inward-only), so it always reports
    // "anonymous" for authenticated requests — the entry stamp is the only
    // reliable source. Keep the fallback for paths that never run auth.
    if entry.APIKeyName == "" {
        entry.APIKeyName = keyName
    }

    if st != nil {
        if err := st.AppendLog(entry); err != nil {
            slog.Error("failed to append request log", "error", err, "request_type", entry.RequestType)
        }
    }
}
