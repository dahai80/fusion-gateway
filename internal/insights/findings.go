// P3b stage 3 (plan §15.5): fg_optimization_findings read + lifecycle. The
// admin apply endpoint writes the config first (admin owns updateYAMLSection)
// and only then marks the row applied — a failed config write must not leave
// a finding stamped applied.
package insights

import (
    "context"
    "encoding/json"
    "fmt"
    "time"
)

// Finding is one fg_optimization_findings row.
type Finding struct {
    ID            int64           `json:"id"`
    GeneratedAt   time.Time       `json:"generated_at"`
    Rule          string          `json:"rule"`
    Scope         json.RawMessage `json:"scope"`
    Evidence      json.RawMessage `json:"evidence"`
    Action        json.RawMessage `json:"action"`
    Status        string          `json:"status"`
    AppliedAt     *time.Time      `json:"applied_at"`
    AppliedConfig *string         `json:"applied_config"`
}

const findingCols = `id, generated_at, rule, scope, evidence, action,
                     status, applied_at, applied_config`

// scanFinding maps one query row into a Finding.
func scanFinding(scanner interface{ Scan(...any) error }) (*Finding, error) {
    var f Finding
    if err := scanner.Scan(&f.ID, &f.GeneratedAt, &f.Rule, &f.Scope,
        &f.Evidence, &f.Action, &f.Status, &f.AppliedAt, &f.AppliedConfig); err != nil {
        return nil, err
    }
    return &f, nil
}

// Findings lists optimization findings, newest first. status/rule are
// exact-match optional filters; limit defaults to 100, caps at 500.
func (s *Service) Findings(ctx context.Context, status, rule string, limit int) ([]Finding, error) {
    if limit <= 0 {
        limit = 100
    }
    if limit > 500 {
        limit = 500
    }
    where := ""
    args := []any{}
    if status != "" {
        args = append(args, status)
        where += fmt.Sprintf(" status = $%d AND", len(args))
    }
    if rule != "" {
        args = append(args, rule)
        where += fmt.Sprintf(" rule = $%d AND", len(args))
    }
    if where != "" {
        where = " WHERE" + where
        where = where[:len(where)-4]
    }
    args = append(args, limit)
    q := `SELECT ` + findingCols + ` FROM fg_optimization_findings` + where +
        fmt.Sprintf(" ORDER BY generated_at DESC, id DESC LIMIT $%d", len(args))
    rows, err := s.pool.Query(ctx, q, args...)
    if err != nil {
        return nil, fmt.Errorf("findings query: %w", err)
    }
    defer rows.Close()
    out := []Finding{}
    for rows.Next() {
        f, err := scanFinding(rows)
        if err != nil {
            return nil, fmt.Errorf("findings scan: %w", err)
        }
        out = append(out, *f)
    }
    return out, rows.Err()
}

// FindingByID fetches a single finding (apply/dismiss precheck — the admin
// handler needs the rule and scope before touching config).
func (s *Service) FindingByID(ctx context.Context, id int64) (*Finding, error) {
    f, err := scanFinding(s.pool.QueryRow(ctx,
        `SELECT `+findingCols+` FROM fg_optimization_findings WHERE id = $1`, id))
    if err != nil {
        return nil, fmt.Errorf("finding fetch: %w", err)
    }
    return f, nil
}

// ApplyFinding stamps a finding applied with the config mutation that was
// written. Only a 'new' finding can transition — re-applying or dismissing an
// applied row must be a hard error, not a silent overwrite of history.
func (s *Service) ApplyFinding(ctx context.Context, id int64, appliedConfig string) (*Finding, error) {
    f, err := scanFinding(s.pool.QueryRow(ctx,
        `UPDATE fg_optimization_findings
         SET status = 'applied', applied_at = now(), applied_config = $2
         WHERE id = $1 AND status = 'new'
         RETURNING `+findingCols, id, appliedConfig))
    if err != nil {
        return nil, fmt.Errorf("apply finding: %w", err)
    }
    return f, nil
}

// DismissFinding marks a finding not worth acting on. Same 'new'-only
// transition contract as ApplyFinding.
func (s *Service) DismissFinding(ctx context.Context, id int64) (*Finding, error) {
    f, err := scanFinding(s.pool.QueryRow(ctx,
        `UPDATE fg_optimization_findings
         SET status = 'dismissed'
         WHERE id = $1 AND status = 'new'
         RETURNING `+findingCols, id))
    if err != nil {
        return nil, fmt.Errorf("dismiss finding: %w", err)
    }
    return f, nil
}
