package browser

import (
    "bytes"
    "encoding/binary"
    "errors"
    "io"
    "strings"
    "testing"
    "time"
)

func TestWriteFrameRoundTrip(t *testing.T) {
    var buf bytes.Buffer
    type payload struct {
        Hello string `json:"hello"`
        N     int    `json:"n"`
    }
    if err := WriteFrame(&buf, payload{Hello: "world", N: 42}); err != nil {
        t.Fatalf("WriteFrame: %v", err)
    }
    body, err := ReadFrame(&buf, defaultFrameMaxBytes, time.Second)
    if err != nil {
        t.Fatalf("ReadFrame: %v", err)
    }
    if !strings.Contains(string(body), `"hello":"world"`) {
        t.Fatalf("body lost field: %s", body)
    }
    if !strings.Contains(string(body), `"n":42`) {
        t.Fatalf("body lost field: %s", body)
    }
}

func TestWriteFramePrefixIsBigEndianUint32(t *testing.T) {
    var buf bytes.Buffer
    if err := WriteFrame(&buf, map[string]any{"a": 1}); err != nil {
        t.Fatalf("WriteFrame: %v", err)
    }
    var prefix [4]byte
    if _, err := io.ReadFull(&buf, prefix[:]); err != nil {
        t.Fatalf("read prefix: %v", err)
    }
    declared := binary.BigEndian.Uint32(prefix[:])
    rest := buf.Bytes()
    if int(declared) != len(rest) {
        t.Fatalf("prefix %d != body len %d", declared, len(rest))
    }
}

func TestReadFrameOversize(t *testing.T) {
    var buf bytes.Buffer
    var prefix [4]byte
    binary.BigEndian.PutUint32(prefix[:], uint32(defaultFrameMaxBytes+1))
    buf.Write(prefix[:])
    buf.Write([]byte("x"))
    _, err := ReadFrame(&buf, defaultFrameMaxBytes, time.Second)
    if !errors.Is(err, ErrFrameOversize) {
        t.Fatalf("expected ErrFrameOversize, got %v", err)
    }
}

func TestReadFrameEmpty(t *testing.T) {
    var buf bytes.Buffer
    var prefix [4]byte // all zero = declared length 0
    buf.Write(prefix[:])
    _, err := ReadFrame(&buf, defaultFrameMaxBytes, time.Second)
    if !errors.Is(err, ErrFrameEmpty) {
        t.Fatalf("expected ErrFrameEmpty, got %v", err)
    }
}

func TestReadFramePrefixEOF(t *testing.T) {
    // Only 2 bytes available — prefix read fails with io.ErrUnexpectedEOF.
    var buf bytes.Buffer
    buf.Write([]byte{0x00, 0x01})
    _, err := ReadFrame(&buf, defaultFrameMaxBytes, time.Second)
    if err == nil {
        t.Fatal("expected error on short prefix, got nil")
    }
    if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
        t.Fatalf("expected EOF-class error, got %v", err)
    }
}

func TestReadFrameTruncatedBody(t *testing.T) {
    var buf bytes.Buffer
    var prefix [4]byte
    binary.BigEndian.PutUint32(prefix[:], 10)
    buf.Write(prefix[:])
    buf.Write([]byte("only5")) // declared 10, sent 5
    _, err := ReadFrame(&buf, defaultFrameMaxBytes, time.Second)
    if err == nil {
        t.Fatal("expected error on truncated body, got nil")
    }
}
