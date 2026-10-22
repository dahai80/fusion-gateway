package shell

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sort"
    "sync"
)

// ConnectorDoc is one config-first third-party connector document (#275,
// migration E6): the shell Settings page edits the whole JSON document; the
// gateway stores it, validates it, and resolves its credential_ref against
// the adapter vault. Package validation/enable (plugin semantics) delegates
// to fusion-plugins-ecosystem#30 — the gateway's contract is store +
// validate + credential resolution.
type ConnectorDoc struct {
    ID            string          `json:"id"`
    Kind          string          `json:"kind"`
    Operations    []string       `json:"operations"`
    CredentialRef string          `json:"credential_ref,omitempty"` // adapter vault platform id
    Config        json.RawMessage `json:"config,omitempty"`
    Enabled       bool            `json:"enabled"`
}

// ConnectorView is the GET /api/v1/shell/connectors row: the document plus
// the server-side validation problems (never a silent drop).
type ConnectorView struct {
    ConnectorDoc
    Problems []string `json:"problems"`
}

// ConnectorStore persists connector documents (store.data_dir/
// shell_connectors.json). Credentials never live here — credential_ref
// points at the vault.
type ConnectorStore struct {
    mu    sync.RWMutex
    docs  map[string]*ConnectorDoc
    path  string // "" = memory only (tests)
    vault *AdapterVault
}

func NewConnectorStore(path string, vault *AdapterVault) *ConnectorStore {
    return &ConnectorStore{
        docs:  make(map[string]*ConnectorDoc),
        path:  path,
        vault: vault,
    }
}

func (s *ConnectorStore) Load() error {
    if s.path == "" {
        return nil
    }
    raw, err := os.ReadFile(s.path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read shell connectors store: %w", err)
    }
    var docs []ConnectorDoc
    if err := jsonUnmarshalStrict(raw, &docs); err != nil {
        return fmt.Errorf("parse shell connectors store %s: %w", s.path, err)
    }
    s.mu.Lock()
    for i := range docs {
        if docs[i].ID == "" {
            slog.Warn("shell connector store entry skipped (empty id)", "index", i)
            continue
        }
        s.docs[docs[i].ID] = &docs[i]
    }
    n := len(s.docs)
    s.mu.Unlock()
    slog.Info("shell connector store loaded", "path", s.path, "connectors", n)
    return nil
}

func (s *ConnectorStore) Save() error {
    if s.path == "" {
        return nil
    }
    s.mu.RLock()
    docs := make([]ConnectorDoc, 0, len(s.docs))
    for _, d := range s.docs {
        docs = append(docs, *d)
    }
    s.mu.RUnlock()
    sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
    if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
        return fmt.Errorf("create shell connectors dir: %w", err)
    }
    raw, err := jsonMarshalIndent(docs)
    if err != nil {
        return fmt.Errorf("marshal shell connectors: %w", err)
    }
    tmp := s.path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write shell connectors temp: %w", err)
    }
    if err := os.Rename(tmp, s.path); err != nil {
        return fmt.Errorf("rename shell connectors store: %w", err)
    }
    return nil
}

// Put upserts one connector document and returns its validation problems.
// Shape problems are stored-with-problems (the GUI shows them inline), only
// an empty id is rejected outright — the document is the operator's draft
// pad, not a gate.
func (s *ConnectorStore) Put(doc ConnectorDoc) ([]string, error) {
    if doc.ID == "" {
        return nil, fmt.Errorf("connector id is required")
    }
    s.mu.Lock()
    s.docs[doc.ID] = &doc
    s.mu.Unlock()
    problems := s.Validate(doc.ID)
    if err := s.Save(); err != nil {
        slog.Error("shell connector store save failed", "path", s.path, "error", err)
        return problems, err
    }
    slog.Info("shell connector saved", "id", doc.ID, "kind", doc.Kind, "problems", len(problems))
    return problems, nil
}

// Validate recomputes one document's problems against the current state
// (shape + vault resolvability of credential_ref).
func (s *ConnectorStore) Validate(id string) []string {
    s.mu.RLock()
    doc, exists := s.docs[id]
    s.mu.RUnlock()
    if !exists {
        return []string{"connector not found"}
    }
    var problems []string
    if doc.Kind == "" {
        problems = append(problems, "kind is required")
    }
    if len(doc.Operations) == 0 {
        problems = append(problems, "operations must list at least one operation")
    }
    if doc.CredentialRef != "" && s.vault != nil {
        if _, ok := s.vault.Resolve(doc.CredentialRef); !ok {
            problems = append(problems, fmt.Sprintf("credential_ref %q does not resolve in the adapter vault", doc.CredentialRef))
        }
    }
    return problems
}

// List returns all documents with their current problems, sorted by id.
func (s *ConnectorStore) List() []ConnectorView {
    s.mu.RLock()
    docs := make([]ConnectorDoc, 0, len(s.docs))
    for _, d := range s.docs {
        docs = append(docs, *d)
    }
    s.mu.RUnlock()
    sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
    views := make([]ConnectorView, 0, len(docs))
    for _, d := range docs {
        views = append(views, ConnectorView{ConnectorDoc: d, Problems: s.Validate(d.ID)})
    }
    return views
}

// Get returns one document by id.
func (s *ConnectorStore) Get(id string) (ConnectorDoc, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    d, ok := s.docs[id]
    if !ok {
        return ConnectorDoc{}, false
    }
    return *d, true
}

// Test runs the connector's self-check (#275): document valid + enabled +
// credential_ref resolvable. An `operation` narrows the check to that
// operation name (must be in the document's operations list). Executing a
// real third-party call is the plugin package's job (fusion-plugins-
// ecosystem#30); the gateway reports resolvability honestly.
func (s *ConnectorStore) Test(id, operation string) (bool, string, []string) {
    doc, ok := s.Get(id)
    if !ok {
        return false, "connector not found", nil
    }
    problems := s.Validate(id)
    if !doc.Enabled {
        problems = append(problems, "connector disabled")
    }
    if operation != "" {
        found := false
        for _, op := range doc.Operations {
            if op == operation {
                found = true
                break
            }
        }
        if !found {
            problems = append(problems, fmt.Sprintf("operation %q not in connector operations", operation))
        }
    }
    if len(problems) > 0 {
        return false, problems[0], problems
    }
    return true, "", nil
}
