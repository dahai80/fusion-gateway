package browser

import (
    "encoding/binary"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net"
    "time"
)

// Frame cap mirrors fusion-browser Framing.swift maxFrameBytes (8 MiB). A
// frame claiming > cap is a hard error — the caller drops the connection and
// clears the buffer (matches the Swift backpressure drop). Capping here
// prevents an attacker (or a buggy node) from OOM-ing the gateway by sending a
// 4-byte length prefix that claims gigabytes then dribbling the body.
const (
    defaultFrameMaxBytes = 8 * 1024 * 1024
    defaultFrameTimeout  = 30 * time.Second
)

var (
    // ErrFrameOversize is returned by ReadFrame when the declared frame length
    // exceeds the configured cap. The caller MUST close the connection — a
    // peer that lies about frame size is not worth keeping.
    ErrFrameOversize = errors.New("browser: frame oversize")
    // ErrFrameTimeout is returned by ReadFrame when the body does not arrive
    // in full within the frame timeout after the length prefix is read. B-4:
    // a slow-drip partial frame would otherwise hold the buffer forever.
    ErrFrameTimeout = errors.New("browser: frame timeout")
    // ErrFrameEmpty is returned by ReadFrame when a frame body is empty (zero
    // length prefix). fusion-browser never sends these; treat as malformed.
    ErrFrameEmpty = errors.New("browser: frame empty")
)

// WriteFrame encodes obj as JSON, writes a 4-byte big-endian uint32 length
// prefix followed by the body. Byte-exact mirror of FBFrame.encode in
// fusion-browser Protocol.swift: [u32 BE len][JSON payload].
func WriteFrame(w io.Writer, obj any) error {
    body, err := json.Marshal(obj)
    if err != nil {
        return fmt.Errorf("browser: marshal frame body: %w", err)
    }
    var prefix [4]byte
    binary.BigEndian.PutUint32(prefix[:], uint32(len(body)))
    if _, err := w.Write(prefix[:]); err != nil {
        return fmt.Errorf("browser: write frame prefix: %w", err)
    }
    if _, err := w.Write(body); err != nil {
        return fmt.Errorf("browser: write frame body: %w", err)
    }
    return nil
}

// ReadFrame reads one length-prefixed frame from r and returns the raw body
// bytes (without the prefix). The caller unmarshals per the response type.
// maxBytes bounds the declared length (oversize → ErrFrameOversize); timeout
// bounds the time to read the full body after the prefix is consumed
// (partial-frame slow-drip → ErrFrameTimeout). The returned body is a fresh
// copy so the caller may hold it past the next read.
func ReadFrame(r io.Reader, maxBytes int, timeout time.Duration) ([]byte, error) {
    if maxBytes <= 0 {
        maxBytes = defaultFrameMaxBytes
    }
    if timeout <= 0 {
        timeout = defaultFrameTimeout
    }

    var prefix [4]byte
    if _, err := io.ReadFull(r, prefix[:]); err != nil {
        return nil, fmt.Errorf("browser: read frame prefix: %w", err)
    }
    n := binary.BigEndian.Uint32(prefix[:])
    if n == 0 {
        return nil, ErrFrameEmpty
    }
    if int64(n) > int64(maxBytes) {
        slog.Warn("browser frame oversize", "declared", n, "cap", maxBytes)
        return nil, fmt.Errorf("%w: declared %d > cap %d", ErrFrameOversize, n, maxBytes)
    }

    // Bound the body read so a silent peer cannot hold the goroutine forever
    // (B-4 partial-frame hold). A plain io.ReadFull on a UDS blocks until the
    // declared bytes arrive or the conn closes; set a read deadline on the
    // net.Conn (UDS) so a partial-frame slow-drip is dropped at the timeout.
    body := make([]byte, n)
    if err := readFullBounded(r, body, timeout); err != nil {
        return nil, err
    }
    return body, nil
}

// readFullBounded is io.ReadFull with a hard timeout on the whole body. The
// timeout is enforced via SetReadDeadline when the reader is a net.Conn (the
// UDS production path). For a non-Conn reader (tests using bytes.Buffer),
// io.ReadFull returns immediately so the deadline is a no-op and the timeout
// never fires — correct, and no goroutine is spawned.
func readFullBounded(r io.Reader, buf []byte, timeout time.Duration) error {
    conn, ok := r.(net.Conn)
    if ok {
        if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
            slog.Debug("browser frame: SetReadDeadline unsupported, falling back to blocking read",
                "error", err)
        } else {
            defer conn.SetReadDeadline(time.Time{})
        }
    }
    if _, err := io.ReadFull(r, buf); err != nil {
        // A timeout surfaces as a net.Error with Timeout()==true from the
        // deadline path; translate it to ErrFrameTimeout so the caller closes
        // the conn (matches the Swift timeout drop).
        var ne net.Error
        if ok && errors.As(err, &ne) && ne.Timeout() {
            slog.Warn("browser frame partial timeout",
                "expected", len(buf), "timeout", timeout.String())
            return fmt.Errorf("%w: %d bytes in %s", ErrFrameTimeout, len(buf), timeout.String())
        }
        return fmt.Errorf("browser: read frame body: %w", err)
    }
    return nil
}
