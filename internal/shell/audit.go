package shell

import (
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sync"
    "time"
)

// Audit action classes and outcomes (#275, migration E6 contract vocabulary).
const (
    AuditClassRead     = "read"
    AuditClassWrite    = "write"
    AuditClassApproval = "approval"
    AuditClassAgentRun = "agent_run"
    AuditClassConfig   = "config"
    AuditClassAuth     = "auth"
)

var auditClasses = map[string]struct{}{
    AuditClassRead: {}, AuditClassWrite: {}, AuditClassApproval: {},
    AuditClassAgentRun: {}, AuditClassConfig: {}, AuditClassAuth: {},
}

const (
    AuditOutcomeOk    = "ok"
    AuditOutcomeDenied  = "denied"
    AuditOutcomeError = "error"
)

// AuditEntry is one row of the shell audit trail. The chain is
// hash(prev_hash || canonical fields) per entry; a row whose recomputed hash
// mismatches — or that follows a broken row — surfaces integrity_ok=false
// (rendered as "tampered" by the shell audit page).
type AuditEntry struct {
    Seq         int64     `json:"seq"`
    TS          time.Time `json:"ts"`
    User        string    `json:"user"`
    Role        string    `json:"role"`
    ActionClass string    `json:"action_class"`
    Method      string    `json:"method"`
    Path        string    `json:"path"`
    Outcome     string    `json:"outcome"`
    DurationMS  int64     `json:"duration_ms"`
    PrevHash    string    `json:"prev_hash"`
    Hash        string    `json:"hash"`
}

// AuditView is the GET /api/v1/shell/audit row: the entry plus the verified
// integrity flag.
type AuditView struct {
    AuditEntry
    IntegrityOK bool `json:"integrity_ok"`
}

// maxAuditEntries bounds the on-disk chain (same copy-trim pattern as the
// connector audit log — reslicing would pin the ever-growing backing array).
const maxAuditEntries = 10000

// AuditLog is the hash-chain audit store behind /api/v1/shell/audit. Appends
// are write-through (admin-plane volume is low); the chain root is the last
// entry's hash, so an external edit of ANY historical row breaks every hash
// after it and the read path reports it.
type AuditLog struct {
    mu      sync.RWMutex
    entries []AuditEntry
    path    string // "" = memory only (tests)
}

func NewAuditLog(path string) *AuditLog {
    return &AuditLog{path: path}
}

func (a *AuditLog) Load() error {
    if a.path == "" {
        return nil
    }
    raw, err := os.ReadFile(a.path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read shell audit store: %w", err)
    }
    var entries []AuditEntry
    if err := jsonUnmarshalStrict(raw, &entries); err != nil {
        // A corrupt audit store must not wedge the shell: keep it aside and
        // start a fresh chain — the audit page shows what survived.
        slog.Error("shell audit store unparseable — starting fresh chain", "path", a.path, "error", err)
        return nil
    }
    a.mu.Lock()
    a.entries = entries
    n := len(a.entries)
    a.mu.Unlock()
    slog.Info("shell audit store loaded", "path", a.path, "entries", n)
    return nil
}

func (a *AuditLog) Save() error {
    if a.path == "" {
        return nil
    }
    a.mu.RLock()
    raw, err := jsonMarshalIndent(a.entries)
    a.mu.RUnlock()
    if err != nil {
        return fmt.Errorf("marshal shell audit entries: %w", err)
    }
    if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
        return fmt.Errorf("create shell audit dir: %w", err)
    }
    tmp := a.path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write shell audit temp: %w", err)
    }
    if err := os.Rename(tmp, a.path); err != nil {
        return fmt.Errorf("rename shell audit store: %w", err)
    }
    return nil
}

// Record appends one entry to the chain. Unknown action classes / outcomes
// are coerced to the contract vocabulary (read/error) so the store can never
// hold off-vocabulary rows an attacker injected via the caller.
func (a *AuditLog) Record(user, role, actionClass, method, path, outcome string, durationMS int64) error {
    if _, ok := auditClasses[actionClass]; !ok {
        actionClass = AuditClassRead
    }
    if outcome != AuditOutcomeOk && outcome != AuditOutcomeDenied && outcome != AuditOutcomeError {
        outcome = AuditOutcomeError
    }
    a.mu.Lock()
    prevHash := ""
    seq := int64(0)
    if n := len(a.entries); n > 0 {
        prevHash = a.entries[n-1].Hash
        seq = a.entries[n-1].Seq + 1
    }
    e := AuditEntry{
        Seq:         seq,
        TS:          time.Now().UTC(),
        User:        user,
        Role:        role,
        ActionClass: actionClass,
        Method:      method,
        Path:        path,
        Outcome:     outcome,
        DurationMS:  durationMS,
        PrevHash:    prevHash,
    }
    e.Hash = auditHash(e)
    a.entries = append(a.entries, e)
    if len(a.entries) > maxAuditEntries {
        trimmed := make([]AuditEntry, maxAuditEntries)
        copy(trimmed, a.entries[len(a.entries)-maxAuditEntries:])
        a.entries = trimmed
    }
    a.mu.Unlock()
    if err := a.Save(); err != nil {
        slog.Error("shell audit store save failed", "path", a.path, "error", err)
        return err
    }
    return nil
}

// Entries returns the verified chain, newest first, filtered by user and/or
// action_class (empty filter = all). Verification walks oldest → newest: a
// row is integrity_ok only if its own hash recomputes AND every predecessor
// in the chain was intact (the first tampered row poisons the tail).
func (a *AuditLog) Entries(user, actionClass string) []AuditView {
    a.mu.RLock()
    entries := make([]AuditEntry, len(a.entries))
    copy(entries, a.entries)
    a.mu.RUnlock()

    ok := true
    prev := ""
    expectedSeq := int64(0)
    views := make([]AuditView, 0, len(entries))
    for i := range entries {
        e := entries[i]
        rowOK := ok && e.Hash == auditHash(e) && e.PrevHash == prev && e.Seq == expectedSeq
        if !rowOK {
            ok = false
        }
        expectedSeq = e.Seq + 1
        prev = e.Hash
        if (user == "" || e.User == user) && (actionClass == "" || e.ActionClass == actionClass) {
            views = append(views, AuditView{AuditEntry: e, IntegrityOK: rowOK})
        }
    }
    // Newest first for the audit page.
    for i, j := 0, len(views)-1; i < j; i, j = i+1, j-1 {
        views[i], views[j] = views[j], views[i]
    }
    return views
}

// auditHash chains one entry: sha256 over the canonical field join with the
// previous entry's hash.
func auditHash(e AuditEntry) string {
    canon := fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%s|%d|%s",
        e.Seq, e.TS.Format(time.RFC3339Nano), e.User, e.Role, e.ActionClass,
        e.Method, e.Path, e.Outcome, e.DurationMS, e.PrevHash)
    sum := sha256.Sum256([]byte(canon))
    return hex.EncodeToString(sum[:])
}
