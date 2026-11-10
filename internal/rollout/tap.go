// Package rollout implements the gateway-side rollout tap (#608): mirroring
// production LLM traffic into an append-only JSONL sink that fusion-trainer
// consumes as RL data (RFT/GRPO) — the agent-lightning v1.0 proxy-side
// collection pattern, mapped onto the existing :11432 choke point with no
// second proxy.
//
// Isolation contract: the tap NEVER blocks, fails, or slows the proxied
// request. The sink write is asynchronous (bounded queue, drop-on-full with
// a loud warning) and a sink write failure degrades to a log line.
package rollout

import (
    "bufio"
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

// SchemaVersion is the current rollout record schema (fusion-trainer
// docs/rollout-tap.md — validated by `ft datasets from-rollouts`).
const SchemaVersion = 1

// Content modes (PII gate): "redact" drops message content but keeps the
// shape/length (default); "raw" stores verbatim content, explicit per-config
// opt-in only.
const (
    ContentModeRedact = "redact"
    ContentModeRaw    = "raw"
)

// Message is one turn of the mirrored conversation (D1 chat schema shape).
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

// Outcome is backfilled by app-observable producers (cowork flow nodes,
// feedback hooks) directly in the sink file — the tap never writes it.
type Outcome struct {
    Signal string  `json:"signal,omitempty"`
    Score  float64 `json:"score,omitempty"`
    Detail string  `json:"detail,omitempty"`
}

// Record is one rollout (schema v1). Required: schema_version, rollout_id,
// model, messages. Optional: everything else.
type Record struct {
    SchemaVersion int       `json:"schema_version"`
    RolloutID     string    `json:"rollout_id"`
    TenantID      string    `json:"tenant_id,omitempty"`
    Source        string    `json:"source,omitempty"`
    Model         string    `json:"model,omitempty"`
    CreatedAt     string    `json:"created_at,omitempty"`
    Messages      []Message `json:"messages"`
    FinishReason  string    `json:"finish_reason,omitempty"`
    LatencyMS     int64     `json:"latency_ms,omitempty"`
    Prompt        string    `json:"prompt,omitempty"`
}

// NewRolloutID mints "ro-<16 hex>" from crypto/rand, with a time-derived
// fallback that keeps records unique-enough if the entropy source fails.
func NewRolloutID(now time.Time) string {
    b := make([]byte, 8)
    if _, err := rand.Read(b); err != nil {
        return fmt.Sprintf("ro-%x", now.UnixNano())
    }
    return "ro-" + hex.EncodeToString(b)
}

// RedactContent renders the redacted shape of a message: the content is
// dropped, the length survives so prompt-size analytics still work.
func RedactContent(content string) string {
    return fmt.Sprintf("[redacted:%d]", len(content))
}

// Tap decides which requests mirror into the sink and applies the PII gate.
// Decision inputs (config + per-key override), deny beats allow:
//   - tap disabled globally → deny;
//   - key override false → deny (even if the tenant is allowlisted);
//   - key override true → allow (explicit per-key opt-in);
//   - otherwise the tenant allowlist decides; an empty allowlist means
//     every tenant is tapped when the plane is enabled.
type Tap struct {
    mu          sync.RWMutex
    enabled     bool
    contentMode string
    tenants     map[string]bool
    sink        *Sink
}

// TapConfig is the wire-level tap configuration (config.rollout_tap).
type TapConfig struct {
    Enabled     bool
    ContentMode string
    Tenants     []string
    SinkPath    string
    MaxQueue    int
}

// NewTap builds the tap; a nil sink (empty path / construction failure
// logged by NewSink) makes every decision deny — fail-closed.
func NewTap(cfg TapConfig) *Tap {
    t := &Tap{
        enabled:     cfg.Enabled,
        contentMode: cfg.ContentMode,
        tenants:     map[string]bool{},
    }
    if t.contentMode == "" {
        t.contentMode = ContentModeRedact
    }
    for _, tn := range cfg.Tenants {
        t.tenants[tn] = true
    }
    if cfg.Enabled {
        t.sink = NewSink(cfg.SinkPath, cfg.MaxQueue)
    }
    return t
}

// Enabled reports whether the plane is on at all.
func (t *Tap) Enabled() bool {
    if t == nil {
        return false
    }
    return t.enabled
}

// Allows decides for one request: tenant + per-key override (nil = inherit).
func (t *Tap) Allows(tenant string, keyOverride *bool) bool {
    if t == nil || !t.enabled || t.sink == nil {
        return false
    }
    if keyOverride != nil {
        return *keyOverride // explicit per-key decision, deny beats allow
    }
    if len(t.tenants) == 0 {
        return true
    }
    return t.tenants[tenant]
}

// Record enqueues one rollout; never blocks (drop-on-full) and never
// propagates errors to the caller — isolation contract.
func (t *Tap) Record(rec Record) {
    if t == nil || t.sink == nil {
        return
    }
    rec.SchemaVersion = SchemaVersion
    if rec.RolloutID == "" {
        rec.RolloutID = NewRolloutID(time.Now())
    }
    if rec.CreatedAt == "" {
        rec.CreatedAt = time.Now().UTC().Format(time.RFC3339)
    }
    if t.contentMode != ContentModeRaw {
        for i := range rec.Messages {
            rec.Messages[i].Content = RedactContent(rec.Messages[i].Content)
        }
        rec.Prompt = RedactContent(rec.Prompt)
    }
    t.sink.enqueue(rec)
}

// Sink is the append-only JSONL store. Writes run on a background worker
// draining a bounded queue; the hot path only does a non-blocking send.
type Sink struct {
    mu      sync.Mutex
    f       *os.File
    w       *bufio.Writer
    ch      chan Record
    dropped int64 // atomic-ish counter guarded by mu on the log path
}

// NewSink opens (creates) the sink file for append and starts the drain
// worker. Construction failure logs loudly and returns a sink whose enqueue
// is a no-op — the tap plane stays up, recording nothing.
func NewSink(path string, queueSize int) *Sink {
    s := &Sink{}
    if path == "" {
        slog.Warn("rollout sink disabled: no sink path configured (#608)")
        return s
    }
    if queueSize <= 0 {
        queueSize = 1024
    }
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        slog.Error("rollout sink mkdir failed — tap records will be dropped (#608)", "path", path, "error", err)
        return s
    }
    f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
    if err != nil {
        slog.Error("rollout sink open failed — tap records will be dropped (#608)", "path", path, "error", err)
        return s
    }
    s.f = f
    s.w = bufio.NewWriter(f)
    s.ch = make(chan Record, queueSize)
    safego.GoRestart("rollout_sink", s.drain)
    slog.Info("rollout sink open", "path", path, "queue", queueSize)
    return s
}

// enqueue performs the non-blocking send; a full queue drops the record
// with a warning rather than adding latency to the proxied request.
func (s *Sink) enqueue(rec Record) {
    if s.ch == nil {
        return
    }
    select {
    case s.ch <- rec:
    default:
        s.mu.Lock()
        s.dropped++
        n := s.dropped
        s.mu.Unlock()
        // Log at most every 100th drop so a saturated sink cannot flood
        // the log either.
        if n%100 == 1 {
            slog.Warn("rollout sink queue full — records dropped", "dropped_total", n)
        }
    }
}

// drain is the sink worker: one JSON object per line, flushed per record.
// A write failure logs and drops the record — never panics, never exits
// (GoRestart covers the residual panic case anyway).
func (s *Sink) drain() {
    for rec := range s.ch {
        raw, err := json.Marshal(rec)
        if err != nil {
            slog.Warn("rollout record marshal failed — dropped", "error", err)
            continue
        }
        s.mu.Lock()
        if s.w == nil {
            s.mu.Unlock()
            continue
        }
        if _, err := s.w.Write(append(raw, '\n')); err != nil {
            s.dropped++
            slog.Warn("rollout sink write failed — record dropped", "error", err, "dropped_total", s.dropped)
        } else if err := s.w.Flush(); err != nil {
            slog.Warn("rollout sink flush failed", "error", err)
        }
        s.mu.Unlock()
    }
}
