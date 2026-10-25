package middleware

import (
    "bytes"
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func TestBoundedBuffer_Truncates(t *testing.T) {
    b := &boundedBuffer{max: 8}
    n, err := b.Write([]byte("12345678"))
    if err != nil || n != 8 || b.trunc {
        t.Fatalf("in-cap write: n=%d err=%v trunc=%v", n, err, b.trunc)
    }
    n, err = b.Write([]byte("abcdef"))
    if err != nil || n != 6 {
        t.Fatalf("over-cap write must claim full length, got n=%d err=%v", n, err)
    }
    if !b.trunc {
        t.Fatal("trunc flag must be set on over-cap write")
    }
    if got := b.buf.String(); got != "12345678" {
        t.Fatalf("buffer = %q, want capped at 8 bytes", got)
    }
}

func TestCapture_RequestTeeAndFinalize(t *testing.T) {
    body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"api_key":"sk-secret"}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = withCapture(req, 512, true)

    // handler reads the body normally
    read, err := io.ReadAll(req.Body)
    if err != nil || string(read) != body {
        t.Fatalf("handler read broken: len=%d err=%v", len(read), err)
    }

    res := FinalizeCapture(req, true, true)
    if string(res.ReqBody) != body {
        t.Fatalf("captured request body mismatch")
    }
    if res.ReqTruncated {
        t.Fatal("small body must not be truncated")
    }
}

func TestCapture_RequestCapTruncates(t *testing.T) {
    big := strings.Repeat("x", 2048)
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(big))
    req = withCapture(req, 256, true)
    io.Copy(io.Discard, req.Body)

    res := FinalizeCapture(req, true, true)
    if len(res.ReqBody) != 256 {
        t.Fatalf("captured %d bytes, want 256 cap", len(res.ReqBody))
    }
    if !res.ReqTruncated {
        t.Fatal("truncated flag must be set")
    }
}

func TestCapture_ResponseMirror(t *testing.T) {
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}"))
    req = withCapture(req, 256, false)
    rec := NewResponseRecorder(httptest.NewRecorder())
    crw := &CaptureResponseWriter{ResponseRecorder: rec, bc: GetBodyCapture(req.Context())}

    crw.Write([]byte(`{"choices":[{"message":{"role":"assistant"}}]}`))
    io.Copy(io.Discard, req.Body)
    res := FinalizeCapture(req, false, true)
    if len(res.RespBody) == 0 || !strings.Contains(string(res.RespBody), "assistant") {
        t.Fatalf("response mirror broken: %q", res.RespBody)
    }
}

func TestCapture_MetadataOnlyMode(t *testing.T) {
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"a":1}`))
    req = withCapture(req, 256, true)
    io.Copy(io.Discard, req.Body)
    // both directions disabled at assembly -> nil bodies
    res := FinalizeCapture(req, false, false)
    if res.ReqBody != nil || res.RespBody != nil {
        t.Fatalf("metadata-only mode must yield nil bodies, got %+v", res)
    }
}

func TestCapture_PeekRestoreNoDoubleCapture(t *testing.T) {
    body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = withCapture(req, 512, true)

    // peek+restore pattern (TPM estimator / prompt-injection scan): read the
    // full body through the tee, replace r.Body with a buffered copy, mark
    // captured — then the handler exits WITHOUT draining the replacement.
    read, err := io.ReadAll(req.Body)
    if err != nil || string(read) != body {
        t.Fatalf("peek read broken: len=%d err=%v", len(read), err)
    }
    req.Body = io.NopCloser(strings.NewReader(string(read)))
    MarkRequestBodyCaptured(req.Context())

    res := FinalizeCapture(req, true, true)
    if string(res.ReqBody) != body {
        t.Fatalf("double capture: %q", res.ReqBody)
    }
}

func TestCapture_PeekRestoreWithoutMarkStillDoubles(t *testing.T) {
    // Documents the pre-fix behavior: without the mark, the finalize drain
    // re-appends the replaced body. Guards against removing the marker calls.
    body := `{"a":1}`
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
    req = withCapture(req, 512, true)
    read, _ := io.ReadAll(req.Body)
    req.Body = io.NopCloser(strings.NewReader(string(read)))

    res := FinalizeCapture(req, true, true)
    if string(res.ReqBody) != body+body {
        t.Fatalf("expected doubled body without mark, got %q", res.ReqBody)
    }
}

func TestStripAuthFields(t *testing.T) {
    in := []byte(`{"model":"m","api_key":"sk-secret","password":"p","nested":{"authorization":"keep-me"}}`)
    out := StripAuthFields(in)
    s := string(out)
    if strings.Contains(s, "sk-secret") {
        t.Fatalf("api_key not stripped: %s", s)
    }
    if strings.Contains(s, "\"password\"") {
        t.Fatalf("password not stripped: %s", s)
    }
    if !strings.Contains(s, "\"model\":\"m\"") {
        t.Fatalf("legitimate fields must survive: %s", s)
    }
    // non-JSON passes through untouched
    raw := []byte{0x00, 0x01, 0x02}
    if !bytes.Equal(StripAuthFields(raw), raw) {
        t.Fatal("binary body must pass through untouched")
    }
}
