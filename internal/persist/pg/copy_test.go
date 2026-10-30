package pg

import (
    "bytes"
    "encoding/json"
    "testing"
    "unicode/utf8"

    "github.com/google/uuid"

    "github.com/fusion-gateway/fusion-gateway/internal/persist"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func TestSplitBody(t *testing.T) {
    // valid JSON -> jsonb column
    j, r := splitBody([]byte(`{"model":"m"}`))
    if j == nil || r != nil {
        t.Fatalf("valid JSON must go to jsonb: j=%v r=%v", j, r)
    }
    // SSE frames -> raw text column
    sse := []byte("data: {\"id\":\"x\"}\n\n")
    j, r = splitBody(sse)
    if j != nil || r != string(sse) {
        t.Fatalf("SSE must go to raw: j=%v r=%v", j, r)
    }
    // truncated mid-token JSON -> raw
    j, r = splitBody([]byte(`{"messages":[{"role":"user","content":"abc`))
    if j != nil || r == nil {
        t.Fatalf("truncated JSON must go to raw: j=%v r=%v", j, r)
    }
    // empty -> both NULL
    j, r = splitBody(nil)
    if j != nil || r != nil {
        t.Fatalf("empty body must be NULL/NULL: j=%v r=%v", j, r)
    }
}

// #407: json.Valid passes unpaired surrogate escapes that PostgreSQL jsonb
// rejects (SQLSTATE 22P05) — splitBody must repair them before COPY.
func TestSplitBodySurrogateRepair(t *testing.T) {
    cases := []struct {
        name string
        body string
    }{
        {"unpaired high surrogate", `{"content":"\ud800 bad"}`},
        {"unpaired low surrogate", `{"content":"bad \udc00"}`},
        {"valid surrogate pair escapes", `{"content":"\ud83d\ude00 ok"}`},
        {"surrogate escape elsewhere", `{"nested":{"deep":["\udfff"]},"n":1}`},
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            j, r := splitBody([]byte(tc.body))
            if j == nil || r != nil {
                t.Fatalf("repairable JSON must stay on the jsonb path: j=%v r=%v", j, r)
            }
            cleaned, ok := j.([]byte)
            if !ok {
                t.Fatalf("jsonb value must be []byte, got %T", j)
            }
            // Re-check the repair product the way PG would see it: no
            // surrogate \u escape may survive, and the bytes must be valid
            // JSON (marshal output always is).
            if hasSurrogateEscape(cleaned) {
                t.Fatalf("repaired JSON still contains a surrogate escape: %s", cleaned)
            }
            if !json.Valid(cleaned) {
                t.Fatalf("repaired JSON is not valid: %s", cleaned)
            }
        })
    }
}

func TestSplitBodySurrogatePairDecodes(t *testing.T) {
    j, r := splitBody([]byte(`{"content":"\ud83d\ude00"}`))
    if j == nil || r != nil {
        t.Fatalf("valid pair must stay on the jsonb path: j=%v r=%v", j, r)
    }
    cleaned := j.([]byte)
    var v map[string]any
    if err := json.Unmarshal(cleaned, &v); err != nil {
        t.Fatalf("repaired JSON must unmarshal: %v", err)
    }
    if v["content"] != "\U0001F600" {
        t.Fatalf("valid pair must decode to the emoji rune, got %q", v["content"])
    }
}

func TestSanitizeJSONFastPathKeepsBytes(t *testing.T) {
    // No surrogate escape: bytes must pass through untouched (no re-marshal).
    body := []byte(`{"model":"m","tokens":[1,2,3]}`)
    cleaned, ok := sanitizeJSON(body)
    if !ok || &cleaned[0] != &body[0] {
        t.Fatalf("surrogate-free JSON must return the original slice: ok=%v", ok)
    }
}

func TestSanitizeJSONKeepsBigNumbersExact(t *testing.T) {
    // A surrogate elsewhere must not cost number precision: UseNumber keeps
    // the literal text.
    body := []byte(`{"a":"\ud800","big":12345678901234567890,"f":0.10000000000000001}`)
    cleaned, ok := sanitizeJSON(body)
    if !ok {
        t.Fatal("sanitize must succeed")
    }
    dec := json.NewDecoder(bytes.NewReader(cleaned))
    dec.UseNumber()
    var v map[string]any
    if err := dec.Decode(&v); err != nil {
        t.Fatalf("repaired JSON must unmarshal: %v", err)
    }
    if n, _ := v["big"].(json.Number); n.String() != "12345678901234567890" {
        t.Fatalf("big number must stay byte-exact, got %v", v["big"])
    }
}

// 2026-10-30: PostgreSQL text/jsonb reject invalid UTF-8 byte sequences with
// SQLSTATE 22021 — splitBody must repair them (U+FFFD replacement) so the row
// survives instead of failing the whole payloads CopyFrom batch.
func TestSplitBodyInvalidUTF8Repaired(t *testing.T) {
    // raw text path: SSE frame with a stray 0xa4 byte
    raw := []byte("data: {\"delta\":\"caf\xe9\xa4\"}\n\n")
    j, r := splitBody(raw)
    if j != nil || r == nil {
        t.Fatalf("invalid-UTF8 raw must stay on the raw path: j=%v r=%v", j, r)
    }
    s, ok := r.(string)
    if !ok {
        t.Fatalf("raw value must be string, got %T", r)
    }
    if !utf8.ValidString(s) || bytes.ContainsRune([]byte(s), 0xa4) {
        t.Fatalf("raw value must be valid UTF-8 with the bad byte replaced, got %q", s)
    }
    if !bytes.HasPrefix([]byte(s), []byte("data: {\"delta\":\"caf")) {
        t.Fatalf("repair must preserve surrounding content, got %q", s)
    }

    // json path: otherwise-valid JSON with a stray 0xa4 byte inside a string
    jsonBody := []byte("{\"content\":\"ok\xa4done\"}")
    j, r = splitBody(jsonBody)
    if j == nil || r != nil {
        t.Fatalf("repaired JSON must land on the jsonb path: j=%v r=%v", j, r)
    }
    cleaned, ok := j.([]byte)
    if !ok {
        t.Fatalf("jsonb value must be []byte, got %T", j)
    }
    if !utf8.Valid(cleaned) {
        t.Fatalf("repaired JSON must be valid UTF-8, got %q", cleaned)
    }
    if !bytes.Contains(cleaned, []byte("ok")) || !bytes.Contains(cleaned, []byte("done")) {
        t.Fatalf("repair must keep the surrounding string content, got %q", cleaned)
    }
}

func TestResolveRowID(t *testing.T) {
    e := &persist.Entry{Log: store.RequestLog{ID: "log-42"}}
    e.Log.RequestID = "0f0e0d0c-1a2b-3c4d-5e6f-7a8b9c0d1e2f"
    got := resolveRowID(e)
    if uuid.UUID(got).String() != e.Log.RequestID {
        t.Fatalf("non-UUID ID must fall back to RequestID, got %s", uuid.UUID(got).String())
    }
    e.Log.RequestID = "also-not-uuid"
    if resolveRowID(e) == ([16]byte{}) {
        t.Fatal("both non-UUID must mint a fresh uuid, not zero")
    }
}
