package shell

import (
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sync"
)

// UserAccount is one shell login account (PRD R-1 ③ RBAC). PasswordHash is
// bcrypt only — plaintext never persists. Permissions hold module ids
// ("*" = all); roles hold the role vocabulary ("admin" grants everything).
type UserAccount struct {
    Username           string   `json:"username"`
    DisplayName        string   `json:"display_name"`
    PasswordHash       string   `json:"password_hash"`
    Roles              []string `json:"roles"`
    Permissions        []string `json:"permissions"`
    Disabled           bool     `json:"disabled"`
    MustChangePassword bool     `json:"must_change_password"`
}

// UserStore is the durable account store behind /api/v1/shell/users. Once a
// store file exists it is the source of truth; config users only seed an
// empty store (config edits after that require the API, not config.yaml).
type UserStore struct {
    mu    sync.RWMutex
    users map[string]*UserAccount
    path  string // "" = memory only (tests)
}

func NewUserStore(path string) *UserStore {
    return &UserStore{users: make(map[string]*UserAccount), path: path}
}

// Load reads the store file if present. A missing file is not an error —
// the caller then seeds from config.
func (s *UserStore) Load() error {
    if s.path == "" {
        return nil
    }
    raw, err := os.ReadFile(s.path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read shell users store: %w", err)
    }
    if err := s.hydrate(raw); err != nil {
        return fmt.Errorf("parse shell users store %s: %w", s.path, err)
    }
    slog.Info("shell user store loaded", "path", s.path, "users", s.Count())
    return nil
}

func (s *UserStore) hydrate(raw []byte) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    var accounts []UserAccount
    if err := jsonUnmarshalStrict(raw, &accounts); err != nil {
        return err
    }
    users := make(map[string]*UserAccount, len(accounts))
    for i := range accounts {
        a := &accounts[i]
        if a.Username == "" {
            return fmt.Errorf("store entry %d has empty username", i)
        }
        users[a.Username] = a
    }
    s.users = users
    return nil
}

// SeedFromConfig populates an EMPTY store from config users (bcrypt-hashing
// plaintext passwords). An already-populated store is left untouched.
func (s *UserStore) SeedFromConfig(accounts []UserAccount) error {
    s.mu.Lock()
    if len(s.users) > 0 {
        s.mu.Unlock()
        return nil
    }
    for i := range accounts {
        s.users[accounts[i].Username] = &accounts[i]
    }
    seeded := len(s.users)
    s.mu.Unlock()
    if seeded > 0 {
        slog.Info("shell user store seeded from config", "users", seeded)
    }
    return s.Save()
}

// Save persists the store atomically (temp file + rename, 0600 — the file
// carries bcrypt hashes but is still credential material).
func (s *UserStore) Save() error {
    if s.path == "" {
        return nil
    }
    s.mu.RLock()
    accounts := make([]UserAccount, 0, len(s.users))
    for _, a := range s.users {
        accounts = append(accounts, *a)
    }
    s.mu.RUnlock()
    if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
        return fmt.Errorf("create shell store dir: %w", err)
    }
    raw, err := jsonMarshalIndent(accounts)
    if err != nil {
        return fmt.Errorf("marshal shell users: %w", err)
    }
    tmp := s.path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write shell users temp: %w", err)
    }
    if err := os.Rename(tmp, s.path); err != nil {
        return fmt.Errorf("rename shell users store: %w", err)
    }
    return nil
}

// Get returns a copy of one account.
func (s *UserStore) Get(username string) (UserAccount, bool) {
    s.mu.RLock()
    defer s.mu.RUnlock()
    a, ok := s.users[username]
    if !ok {
        return UserAccount{}, false
    }
    return *a, true
}

// List returns copies of all accounts sorted by username (stable admin rows).
func (s *UserStore) List() []UserAccount {
    s.mu.RLock()
    defer s.mu.RUnlock()
    out := make([]UserAccount, 0, len(s.users))
    for _, a := range s.users {
        out = append(out, *a)
    }
    sortAccounts(out)
    return out
}

// Count returns the number of accounts.
func (s *UserStore) Count() int {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return len(s.users)
}

// EnabledAdminCount counts non-disabled accounts holding the admin role.
func (s *UserStore) EnabledAdminCount() int {
    s.mu.RLock()
    defer s.mu.RUnlock()
    n := 0
    for _, a := range s.users {
        if !a.Disabled && hasRole(a.Roles, "admin") {
            n++
        }
    }
    return n
}

// Create inserts a new account (duplicate username rejected).
func (s *UserStore) Create(a UserAccount) error {
    s.mu.Lock()
    if _, ok := s.users[a.Username]; ok {
        s.mu.Unlock()
        return fmt.Errorf("user %q already exists", a.Username)
    }
    s.users[a.Username] = &a
    s.mu.Unlock()
    return s.Save()
}

// Update mutates one account under the store lock. mutate must not retain
// the pointer beyond the call. Last-enabled-admin guard is the caller's
// policy (handlers check before calling).
func (s *UserStore) Update(username string, mutate func(*UserAccount)) error {
    s.mu.Lock()
    a, ok := s.users[username]
    if !ok {
        s.mu.Unlock()
        return fmt.Errorf("user %q not found", username)
    }
    mutate(a)
    s.mu.Unlock()
    return s.Save()
}

// Delete removes one account.
func (s *UserStore) Delete(username string) error {
    s.mu.Lock()
    if _, ok := s.users[username]; !ok {
        s.mu.Unlock()
        return fmt.Errorf("user %q not found", username)
    }
    delete(s.users, username)
    s.mu.Unlock()
    return s.Save()
}
