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
}

func NewResponseRecorder(w http.ResponseWriter) *ResponseRecorder {
    return &ResponseRecorder{ResponseWriter: w, StatusCode: http.StatusOK}
}

func (r *ResponseRecorder) WriteHeader(code int) {
    r.StatusCode = code
    r.ResponseWriter.WriteHeader(code)
}

func (r *ResponseRecorder) Write(b []byte) (int, error) {
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
    reqID, _ := r.Context().Value(RequestIDKey).(string)
    entry := &store.RequestLog{
        RequestID:   reqID,
        RequestType: r.Method + " " + r.URL.Path,
        IsStream:    r.URL.Query().Get("stream") == "true",
        IsSuccess:   true,
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

func FinalizeAndAppendLog(entry *store.RequestLog, st store.Store, start time.Time, keyName string) {
    entry.Timestamp = start
    entry.Latency = time.Since(start).Seconds()
    entry.IsSuccess = entry.StatusCode >= 200 && entry.StatusCode < 400
    entry.APIKeyName = keyName

    if st != nil {
        if err := st.AppendLog(entry); err != nil {
            slog.Error("failed to append request log", "error", err, "request_type", entry.RequestType)
        }
    }
}
