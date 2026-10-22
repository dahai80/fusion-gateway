package shell

import (
    "errors"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sort"
    "sync"
)

// Shell role model (#275, migration E6 / E5 identity contract): built-in
// roles are fixed with derived write classes; custom roles grant write
// classes from the audit action-class vocabulary. The write-class vocabulary
// IS the audit action-class vocabulary — one list serves both surfaces.
var builtinShellRoles = []RoleDef{
    {Name: "admin", Builtin: true, WriteClasses: allAuditClasses(), Description: "full access (built-in)"},
    {Name: "user", Builtin: true, WriteClasses: []string{AuditClassRead, AuditClassWrite, AuditClassAgentRun}, Description: "standard operator (built-in)"},
    {Name: "auditor", Builtin: true, WriteClasses: []string{AuditClassRead, AuditClassApproval}, Description: "read + approvals (built-in)"},
}

func allAuditClasses() []string {
    return []string{AuditClassRead, AuditClassWrite, AuditClassApproval, AuditClassAgentRun, AuditClassConfig, AuditClassAuth}
}

// RoleDef is one role row in GET /api/v1/shell/roles.
type RoleDef struct {
    Name        string   `json:"name"`
    Builtin     bool     `json:"builtin"`
    WriteClasses []string `json:"write_classes"`
    Description string   `json:"description,omitempty"`
}

// RoleView is RoleDef plus the live user count from the user store.
type RoleView struct {
    RoleDef
    UserCount int `json:"user_count"`
}

// CustomRole is the persisted custom-role shape (store.data_dir/
// shell_roles.json). Built-ins never persist — they are code.
type CustomRole struct {
    Name         string   `json:"name"`
    Description  string   `json:"description,omitempty"`
    WriteClasses []string `json:"write_classes"`
}

// RoleStore holds built-in + custom shell roles.
type RoleStore struct {
    mu     sync.RWMutex
    custom map[string]*CustomRole
    path   string // "" = memory only (tests)
}

// Sentinel errors mapped to HTTP statuses by the role handlers: conflict
// conditions (409) vs validation rejects (400).
var (
    ErrRoleExists  = errors.New("role already exists")
    ErrRoleBuiltin = errors.New("role is built-in")
    ErrRoleInUse   = errors.New("role still assigned to users")
)

func NewRoleStore(path string) *RoleStore {
    return &RoleStore{custom: make(map[string]*CustomRole), path: path}
}

func (s *RoleStore) Load() error {
    if s.path == "" {
        return nil
    }
    raw, err := os.ReadFile(s.path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read shell roles store: %w", err)
    }
    var roles []CustomRole
    if err := jsonUnmarshalStrict(raw, &roles); err != nil {
        return fmt.Errorf("parse shell roles store %s: %w", s.path, err)
    }
    s.mu.Lock()
    for i := range roles {
        if roles[i].Name == "" {
            continue
        }
        s.custom[roles[i].Name] = &roles[i]
    }
    n := len(s.custom)
    s.mu.Unlock()
    slog.Info("shell role store loaded", "path", s.path, "custom_roles", n)
    return nil
}

func (s *RoleStore) Save() error {
    if s.path == "" {
        return nil
    }
    s.mu.RLock()
    roles := make([]CustomRole, 0, len(s.custom))
    for _, r := range s.custom {
        roles = append(roles, *r)
    }
    s.mu.RUnlock()
    sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
    if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
        return fmt.Errorf("create shell roles dir: %w", err)
    }
    raw, err := jsonMarshalIndent(roles)
    if err != nil {
        return fmt.Errorf("marshal shell roles: %w", err)
    }
    tmp := s.path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write shell roles temp: %w", err)
    }
    if err := os.Rename(tmp, s.path); err != nil {
        return fmt.Errorf("rename shell roles store: %w", err)
    }
    return nil
}

// List returns built-in + custom roles with live user counts.
// userCount reports how many enabled accounts hold the role.
func (s *RoleStore) List(userCount func(role string) int) []RoleView {
    s.mu.RLock()
    customs := make([]CustomRole, 0, len(s.custom))
    for _, r := range s.custom {
        customs = append(customs, *r)
    }
    s.mu.RUnlock()
    views := make([]RoleView, 0, len(builtinShellRoles)+len(customs))
    for _, b := range builtinShellRoles {
        views = append(views, RoleView{RoleDef: b, UserCount: userCount(b.Name)})
    }
    sort.Slice(customs, func(i, j int) bool { return customs[i].Name < customs[j].Name })
    for _, c := range customs {
        views = append(views, RoleView{
            RoleDef:   RoleDef{Name: c.Name, WriteClasses: c.WriteClasses, Description: c.Description},
            UserCount: userCount(c.Name),
        })
    }
    return views
}

// Create adds a custom role. Built-in collision is a 409; write classes must
// come from the vocabulary; the role count is bounded (E5: ≤64 per tenant).
func (s *RoleStore) Create(name, description string, writeClasses []string) error {
    if name == "" {
        return fmt.Errorf("role name is required")
    }
    for _, b := range builtinShellRoles {
        if b.Name == name {
            return fmt.Errorf("%w: %q cannot be redefined", ErrRoleBuiltin, name)
        }
    }
    seen := make(map[string]struct{}, len(writeClasses))
    classes := make([]string, 0, len(writeClasses))
    for _, c := range writeClasses {
        if _, ok := auditClasses[c]; !ok {
            return fmt.Errorf("write class %q is not in the vocabulary %v", c, allAuditClasses())
        }
        if _, dup := seen[c]; dup {
            continue
        }
        seen[c] = struct{}{}
        classes = append(classes, c)
    }
    if len(classes) == 0 {
        return fmt.Errorf("custom role must grant at least one write class")
    }
    s.mu.Lock()
    if _, exists := s.custom[name]; exists {
        s.mu.Unlock()
        return fmt.Errorf("%w: %q", ErrRoleExists, name)
    }
    if len(s.custom) >= 64 {
        s.mu.Unlock()
        return fmt.Errorf("custom role limit reached (64)")
    }
    s.custom[name] = &CustomRole{Name: name, Description: description, WriteClasses: classes}
    s.mu.Unlock()
    if err := s.Save(); err != nil {
        return err
    }
    slog.Info("shell custom role created", "role", name, "write_classes", classes)
    return nil
}

// Delete removes a custom role. Built-ins are fixed; a role still assigned
// to users is rejected with the reason (the operator must unassign first).
func (s *RoleStore) Delete(name string, assignedCount func(role string) int) error {
    for _, b := range builtinShellRoles {
        if b.Name == name {
            return fmt.Errorf("%w: %q cannot be deleted", ErrRoleBuiltin, name)
        }
    }
    s.mu.Lock()
    if _, exists := s.custom[name]; !exists {
        s.mu.Unlock()
        return fmt.Errorf("role %q not found", name)
    }
    s.mu.Unlock()
    if n := assignedCount(name); n > 0 {
        return fmt.Errorf("%w: %q is still assigned to %d user(s); unassign them first", ErrRoleInUse, name, n)
    }
    s.mu.Lock()
    delete(s.custom, name)
    s.mu.Unlock()
    if err := s.Save(); err != nil {
        return err
    }
    slog.Info("shell custom role deleted", "role", name)
    return nil
}

// Exists reports whether a role name resolves (built-in or custom) — the
// fail-closed check for user role assignment against custom roles.
func (s *RoleStore) Exists(name string) bool {
    for _, b := range builtinShellRoles {
        if b.Name == name {
            return true
        }
    }
    s.mu.RLock()
    defer s.mu.RUnlock()
    _, ok := s.custom[name]
    return ok
}

// MenuItem is one sidebar entry in the role ↔ menu matrix.
type MenuItem struct {
    ID    string   `json:"id"`
    Label string   `json:"label"`
    Href  string   `json:"href"`
    Roles []string `json:"roles"`
}

// MenuPermissions is the operator-editable role ↔ menu matrix (store.data_dir/
// shell_menu_permissions.json). Defaults are derived per read from the
// manifest registry (every module menu visible to admin + user); overrides
// replace the defaults row-for-row once set.
type MenuPermissions struct {
    mu        sync.RWMutex
    overrides []MenuItem
    path      string // "" = memory only (tests)
}

func NewMenuPermissions(path string) *MenuPermissions {
    return &MenuPermissions{path: path}
}

func (m *MenuPermissions) Load() error {
    if m.path == "" {
        return nil
    }
    raw, err := os.ReadFile(m.path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read shell menu permissions store: %w", err)
    }
    var items []MenuItem
    if err := jsonUnmarshalStrict(raw, &items); err != nil {
        return fmt.Errorf("parse shell menu permissions store %s: %w", m.path, err)
    }
    m.mu.Lock()
    m.overrides = items
    m.mu.Unlock()
    slog.Info("shell menu permissions loaded", "path", m.path, "items", len(items))
    return nil
}

func (m *MenuPermissions) Save() error {
    if m.path == "" {
        return nil
    }
    m.mu.RLock()
    raw, err := jsonMarshalIndent(m.overrides)
    m.mu.RUnlock()
    if err != nil {
        return fmt.Errorf("marshal shell menu permissions: %w", err)
    }
    if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
        return fmt.Errorf("create shell menu permissions dir: %w", err)
    }
    tmp := m.path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write shell menu permissions temp: %w", err)
    }
    if err := os.Rename(tmp, m.path); err != nil {
        return fmt.Errorf("rename shell menu permissions store: %w", err)
    }
    return nil
}

// Get returns {defaults, overrides}. defaults derive from the live manifest
// list (admin + user see every module menu); overrides are whatever the
// operator saved.
func (m *MenuPermissions) Get(defaults []MenuItem) (def, over []MenuItem) {
    m.mu.RLock()
    over = make([]MenuItem, len(m.overrides))
    copy(over, m.overrides)
    m.mu.RUnlock()
    return defaults, over
}

// Put replaces the override matrix. Every item needs id + label; roles must
// resolve against the role store (built-in or custom) — an unknown role
// would silently hide menus forever.
func (m *MenuPermissions) Put(items []MenuItem, roleExists func(name string) bool) error {
    if len(items) > 256 {
        return fmt.Errorf("menu matrix exceeds 256 items")
    }
    seen := make(map[string]struct{}, len(items))
    for _, it := range items {
        if it.ID == "" || it.Label == "" {
            return fmt.Errorf("menu item requires id and label")
        }
        if _, dup := seen[it.ID]; dup {
            return fmt.Errorf("duplicate menu id %q", it.ID)
        }
        seen[it.ID] = struct{}{}
        if len(it.Roles) == 0 {
            return fmt.Errorf("menu %q must list at least one role", it.ID)
        }
        for _, r := range it.Roles {
            if !roleExists(r) {
                return fmt.Errorf("menu %q references unknown role %q", it.ID, r)
            }
        }
    }
    m.mu.Lock()
    m.overrides = items
    m.mu.Unlock()
    if err := m.Save(); err != nil {
        return err
    }
    slog.Info("shell menu permissions saved", "items", len(items))
    return nil
}
