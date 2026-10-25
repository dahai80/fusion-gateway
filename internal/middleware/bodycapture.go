package middleware

import (
    "bytes"
    "context"
    "encoding/json"
    "io"
    "log/slog"
    "net/http"
)

type captureContextKey string

const captureKey captureContextKey = "body_capture"

// boundedBuffer is a capped byte accumulator: writes past the cap are
// counted (returning full length so an io.TeeReader source keeps flowing)
// but not stored, setting truncated.
type boundedBuffer struct {
    buf   bytes.Buffer
    max   int
    trunc bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
    room := b.max - b.buf.Len()
    if room >= len(p) {
        b.buf.Write(p)
        return len(p), nil
    }
    if room > 0 {
        b.buf.Write(p[:room])
    }
    b.trunc = true
    return len(p), nil
}

// bodyCapture accumulates the request and response bytes for one request,
// each bounded by the configured cap (plan §5.1). Stored in the request
// context so the finalize path (withMiddleware defer) can assemble the
// persist.Entry without touching any handler.
type bodyCapture struct {
    maxBytes int

    reqBuf     boundedBuffer
    reqDone    bool

    respBuf    boundedBuffer
}

// withCapture installs a bodyCapture in the context and TeeReads the request
// body into it (plan §4.2 #1): the copy happens before any handler parses
// the body, so capture needs zero handler changes. Reading stops at maxBytes
// — the TeeReader keeps passing bytes through after the cap; only the COPY
// stops (truncation never starves the handler).
func withCapture(r *http.Request, maxBytes int, captureReqBody bool) *http.Request {
    bc := &bodyCapture{maxBytes: maxBytes}
    bc.reqBuf.max = maxBytes
    bc.respBuf.max = maxBytes
    ctx := context.WithValue(r.Context(), captureKey, bc)
    r = r.WithContext(ctx)
    if captureReqBody && r.Body != nil {
        r.Body = io.NopCloser(io.TeeReader(r.Body, &bc.reqBuf))
    }
    return r
}

// GetBodyCapture returns the request's capture buffer (nil when persistence
// capture is off).
func GetBodyCapture(ctx context.Context) *bodyCapture {
    bc, _ := ctx.Value(captureKey).(*bodyCapture)
    return bc
}

// MarkRequestBodyCaptured stops the finalize drain from re-reading the request
// body. Call right after replacing r.Body with a buffered copy (peek+restore
// in TPM estimation, prompt-injection scan, stream solve): the replacement's
// bytes were already tee-captured, and if the handler exits without draining
// the replacement, the finalize io.Copy would append them a second time —
// the duplicated req_body/req_raw rows seen in production.
func MarkRequestBodyCaptured(ctx context.Context) {
    if bc, _ := ctx.Value(captureKey).(*bodyCapture); bc != nil {
        bc.reqDone = true
    }
}

// captureRequest drains the remaining request body to io.Discard. Called at
// finalize: when the handler read the body, this is a no-op on an exhausted
// reader. When the body is UNREAD (auth rejected the request early, admin
// routes), draining must go to Discard — r.Body is the TeeReader installed by
// withCapture, which already tees every byte into reqBuf as a side effect of
// being read. Copying into reqBuf directly made the tee write each byte a
// second time (401 rows carried body+body in req_raw). A replaced r.Body
// (peek+restore sites) is safe too: those read through the tee first, so the
// buffer already holds the full body and the Discard drain adds nothing.
func (bc *bodyCapture) captureRequest(r *http.Request) {
    if bc.reqDone || r.Body == nil {
        return
    }
    bc.reqDone = true
    _, err := io.Copy(io.Discard, r.Body)
    if err != nil && err != io.EOF {
        slog.Debug("request body capture drain stopped", "error", err)
    }
}

// StripAuthFields removes credential-shaped top-level keys from a JSON
// request body before persist (plan §6 capture.strip_auth_fields). Best-effort:
// a non-JSON body passes through untouched (binary/audio uploads).
func StripAuthFields(body []byte) []byte {
    if len(body) == 0 || body[0] != '{' {
        return body
    }
    var m map[string]any
    if err := json.Unmarshal(body, &m); err != nil {
        return body
    }
    changed := false
    for _, k := range []string{"api_key", "apiKey", "authorization", "Authorization", "password"} {
        if _, ok := m[k]; ok {
            delete(m, k)
            changed = true
        }
    }
    if !changed {
        return body
    }
    out, err := json.Marshal(m)
    if err != nil {
        return body
    }
    return out
}

// CaptureResult is the assembled body pair for one finished request.
type CaptureResult struct {
    ReqBody     []byte
    RespBody    []byte
    ReqTruncated  bool
    RespTruncated bool
}

// result copies the bounded buffers out (metadata-only mode passes false for
// a direction and gets nil — the bytes were still copied into the buffer, but
// are dropped at assembly, keeping the config toggle cheap to honor).
func (bc *bodyCapture) result(reqBodyOn, respBodyOn bool) CaptureResult {
    var res CaptureResult
    if reqBodyOn {
        res.ReqBody = append([]byte(nil), bc.reqBuf.buf.Bytes()...)
        res.ReqTruncated = bc.reqBuf.trunc
    }
    if respBodyOn {
        res.RespBody = append([]byte(nil), bc.respBuf.buf.Bytes()...)
        res.RespTruncated = bc.respBuf.trunc
    }
    return res
}

// CaptureResponseWriter wraps the ResponseRecorder to mirror every written
// byte into the capture buffer. Embedding preserves Flusher/Hijack
// passthrough (SSE streaming depends on Flush; /v1/realtime on Hijack) —
// only Write is intercepted.
type CaptureResponseWriter struct {
    *ResponseRecorder
    bc *bodyCapture
}

func (w *CaptureResponseWriter) Write(b []byte) (int, error) {
    w.bc.respBuf.Write(b)
    return w.ResponseWriter.Write(b)
}

// InstallCapture wraps the recorder + request with body capture (persistence
// on). Split out of withMiddleware to keep that function readable.
func InstallCapture(rec *ResponseRecorder, r *http.Request, maxBytes int, captureReqBody bool) (*CaptureResponseWriter, *http.Request) {
    r = withCapture(r, maxBytes, captureReqBody)
    bc := GetBodyCapture(r.Context())
    crw := &CaptureResponseWriter{ResponseRecorder: rec, bc: bc}
    return crw, r
}

// FinalizeCapture drains any unread request body and assembles the result.
// Call exactly once, after the handler returned (all exits).
func FinalizeCapture(r *http.Request, reqBodyOn, respBodyOn bool) CaptureResult {
    bc := GetBodyCapture(r.Context())
    if bc == nil {
        return CaptureResult{}
    }
    bc.captureRequest(r)
    return bc.result(reqBodyOn, respBodyOn)
}
