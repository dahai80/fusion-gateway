// P3b stage 3 (plan §14.2/§15.5): optimization rules engine. Hourly pass
// over fg_request_logs that evaluates the six optimization rules and persists
// actionable findings into fg_optimization_findings. Pure evaluation functions
// live here so unit tests cover the thresholds without a database; the engine
// only fetches aggregates and inserts drafts. A finding with the same
// (rule, scope) inside 24h is skipped, so restarts and overlapping ticks never
// duplicate rows. Only MIGRATE_TO_LOCAL is machine-applicable (via the admin
// apply endpoint, which writes models.supply); the rest are advisory.
package insights

import (
    "context"
    "encoding/json"
    "log/slog"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
)

// rulesInterval: every rule window is at least 24h of data, so an hourly pass
// is well oversampled — keeps the finding fresh without hammering the DB.
const rulesInterval = time.Hour

// Rule names (fg_optimization_findings.rule values, plan §14.2).
const (
    RuleMigrateToLocal = "MIGRATE_TO_LOCAL"
    RuleKeepCloud      = "KEEP_CLOUD"
    RuleMigrateToCloud = "MIGRATE_TO_CLOUD"
    RuleTrimTokens     = "TRIM_TOKENS"
    RuleDedupPrompt    = "DEDUP_PROMPT"
    RulePreloadModel   = "PRELOAD_MODEL"
)

// RulesEngine is the background optimization worker. Shares the Service read
// pool — same class of bounded aggregates as the detector.
type RulesEngine struct {
    svc      *Service
    recorder EventRecorder
    worker   *lifecycle.Worker
}

// StartRulesEngine launches the hourly rules pass. Stop before closing the
// pool (same shutdown contract as the detector).
func (s *Service) StartRulesEngine(parent context.Context, rec EventRecorder) *RulesEngine {
    re := &RulesEngine{svc: s, recorder: rec}
    re.worker = lifecycle.Start(parent, "insights-rules-engine", re.loop)
    slog.Info("optimization rules engine started", "interval", rulesInterval.String())
    return re
}

// Stop joins the rules worker.
func (re *RulesEngine) Stop() {
    if re.worker != nil {
        re.worker.Stop()
    }
}

func (re *RulesEngine) loop(ctx context.Context) {
    // Immediate first pass: the 24h dedupe makes a restart pass a no-op.
    if err := re.runOnce(ctx); err != nil {
        slog.Warn("rules engine pass failed", "error", err)
    }
    ticker := time.NewTicker(rulesInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            if err := re.runOnce(ctx); err != nil {
                slog.Warn("rules engine pass failed", "error", err)
            }
        }
    }
}

// findingDraft is a rule firing awaiting persistence.
type findingDraft struct {
    Rule     string
    Scope    map[string]any
    Evidence map[string]any
    Action   map[string]any
}

// runOnce evaluates every rule and persists the fired drafts.
func (re *RulesEngine) runOnce(ctx context.Context) error {
    var fired []findingDraft

    if rows, err := re.fetchModelSplit(ctx); err != nil {
        slog.Warn("rules engine: model split fetch failed", "error", err)
    } else {
        fired = append(fired, evalMigrateToLocal(rows)...)
        fired = append(fired, evalKeepCloud(rows)...)
        fired = append(fired, evalTrimTokens(rows)...)
    }
    if rows, err := re.fetchLocalHourErrors(ctx); err != nil {
        slog.Warn("rules engine: local hour fetch failed", "error", err)
    } else {
        fired = append(fired, evalMigrateToCloud(rows)...)
    }
    if rows, err := re.fetchPromptDupes(ctx); err != nil {
        slog.Warn("rules engine: prompt dupe fetch failed", "error", err)
    } else {
        fired = append(fired, evalDedupPrompt(rows)...)
    }
    if rows, avgMem, err := re.fetchLocalModelDays(ctx); err != nil {
        slog.Warn("rules engine: local model fetch failed", "error", err)
    } else {
        fired = append(fired, evalPreloadModel(rows, avgMem)...)
    }

    for _, f := range fired {
        if re.deduped(ctx, f) {
            continue
        }
        if err := re.insertFinding(ctx, f); err != nil {
            slog.Warn("rules engine insert failed", "rule", f.Rule, "error", err)
            continue
        }
        slog.Info("optimization finding generated", "rule", f.Rule, "scope", f.Scope)
        if re.recorder != nil {
            re.recorder.RecordEvent("info", "finding_generated", map[string]any{
                "rule": f.Rule, "scope": f.Scope,
            })
        }
    }
    return nil
}

// deduped reports whether an equivalent (rule, scope) finding was already
// generated in the last 24h. Fail open — a broken check must not block a
// finding, worst case is a duplicate row.
func (re *RulesEngine) deduped(ctx context.Context, f findingDraft) bool {
    scope, err := json.Marshal(f.Scope)
    if err != nil {
        return false
    }
    var exists bool
    err = re.svc.pool.QueryRow(ctx,
        `SELECT EXISTS(SELECT 1 FROM fg_optimization_findings
          WHERE rule = $1 AND scope = $2::jsonb
            AND generated_at > now() - interval '24 hours')`,
        f.Rule, scope).Scan(&exists)
    if err != nil {
        slog.Warn("rules engine dedupe check failed", "rule", f.Rule, "error", err)
        return false
    }
    return exists
}

// insertFinding persists one draft. A nil action lands as NULL (advisory
// finding, not machine-applicable).
func (re *RulesEngine) insertFinding(ctx context.Context, f findingDraft) error {
    scope, err := json.Marshal(f.Scope)
    if err != nil {
        return err
    }
    evidence, err := json.Marshal(f.Evidence)
    if err != nil {
        return err
    }
    var action any
    if f.Action != nil {
        if action, err = json.Marshal(f.Action); err != nil {
            return err
        }
    }
    _, err = re.svc.pool.Exec(ctx,
        `INSERT INTO fg_optimization_findings
             (generated_at, rule, scope, evidence, action)
         VALUES (now(), $1, $2, $3, $4)`,
        f.Rule, scope, evidence, action)
    return err
}

// modelSplitRow is one model's 7-day local/cloud traffic split with TTFT P95
// per side and the token-waste tally.
type modelSplitRow struct {
    Model    string
    Total    int
    CloudN   int
    LocalN   int
    LocalErr int
    CloudP95 float64
    LocalP95 float64
    LocalGen int
    TrimN    int
}

// evalMigrateToLocal (plan §14.2): a model routed >10% to the cloud whose
// local TTFT P95 is at least 30% faster with a healthy local error rate
// should prefer fusion-mlx via models.supply. Guards: >= 50 total requests,
// >= 10 local generation samples (P95 is noise below that), local error rate
// <= 2% (a failing local backend must not win the comparison — that is a
// KEEP_CLOUD finding instead).
func evalMigrateToLocal(rows []modelSplitRow) []findingDraft {
    var out []findingDraft
    for _, r := range rows {
        if r.Total < 50 || r.Model == "" {
            continue
        }
        cloudShare := float64(r.CloudN) / float64(r.Total)
        if cloudShare <= 0.10 || r.LocalGen < 10 || r.LocalP95 <= 0 || r.CloudP95 <= 0 {
            continue
        }
        if r.LocalN > 0 && float64(r.LocalErr)/float64(r.LocalN) > 0.02 {
            continue
        }
        if r.LocalP95 >= 0.7*r.CloudP95 {
            continue
        }
        out = append(out, findingDraft{
            Rule:  RuleMigrateToLocal,
            Scope: map[string]any{"model": r.Model},
            Evidence: map[string]any{
                "cloud_share":   cloudShare,
                "cloud_p95_ms":  r.CloudP95,
                "local_p95_ms":  r.LocalP95,
                "cloud_requests": r.CloudN,
                "total_requests": r.Total,
                "window_days":   7,
            },
            Action: map[string]any{
                "type":   "models_supply",
                "model":  r.Model,
                "prefer": "fusion-mlx",
            },
        })
    }
    return out
}

// evalKeepCloud (plan §14.2): a model whose local attempts error out >2% of
// the time (>= 50 local requests) should stay on the cloud — the inverse
// guard of MIGRATE_TO_LOCAL.
func evalKeepCloud(rows []modelSplitRow) []findingDraft {
    var out []findingDraft
    for _, r := range rows {
        if r.Model == "" || r.LocalN < 50 {
            continue
        }
        errRate := float64(r.LocalErr) / float64(r.LocalN)
        if errRate <= 0.02 {
            continue
        }
        out = append(out, findingDraft{
            Rule:  RuleKeepCloud,
            Scope: map[string]any{"model": r.Model},
            Evidence: map[string]any{
                "local_error_rate": errRate,
                "local_errors":     r.LocalErr,
                "local_requests":   r.LocalN,
                "window_days":      7,
            },
        })
    }
    return out
}

// evalTrimTokens (plan §14.2): >5% of a model's requests carry input tokens
// > 50x output tokens (>= 100 requests) — oversized prompts (stale context
// dumps, untrimmed history) inflating both cost and the P3 token budget.
func evalTrimTokens(rows []modelSplitRow) []findingDraft {
    var out []findingDraft
    for _, r := range rows {
        if r.Model == "" || r.Total < 100 {
            continue
        }
        trimShare := float64(r.TrimN) / float64(r.Total)
        if trimShare <= 0.05 {
            continue
        }
        out = append(out, findingDraft{
            Rule:  RuleTrimTokens,
            Scope: map[string]any{"model": r.Model},
            Evidence: map[string]any{
                "oversized_share":  trimShare,
                "oversized_count":  r.TrimN,
                "total_requests":   r.Total,
                "window_days":      7,
            },
        })
    }
    return out
}

// hourErrRow is one local-backend hour-of-day error tally over 7 days.
type hourErrRow struct {
    Hour  int
    Total int
    Errs  int
}

// evalMigrateToCloud (plan §14.2): a specific local hour-of-day erroring >5%
// (>= 20 requests) — a recurring bad window (nightly batch jobs colliding
// with model loads). Advisory: time-based routing is not a config the
// gateway can express today.
func evalMigrateToCloud(rows []hourErrRow) []findingDraft {
    var out []findingDraft
    for _, r := range rows {
        if r.Total < 20 {
            continue
        }
        errRate := float64(r.Errs) / float64(r.Total)
        if errRate <= 0.05 {
            continue
        }
        out = append(out, findingDraft{
            Rule:  RuleMigrateToCloud,
            Scope: map[string]any{"backend": "local", "hour": r.Hour},
            Evidence: map[string]any{
                "error_rate":   errRate,
                "errors":       r.Errs,
                "requests":     r.Total,
                "hour_of_day":  r.Hour,
                "window_days":  7,
            },
        })
    }
    return out
}

// promptDupeRow is one exact-duplicate prompt hash over 24h of chat traffic.
type promptDupeRow struct {
    Hash  string
    Count int
    Model string
}

// evalDedupPrompt (plan §14.2): the same request body sent more than 3 times
// in 24h — cacheable either by the exact-match cache or the semantic cache.
func evalDedupPrompt(rows []promptDupeRow) []findingDraft {
    var out []findingDraft
    for _, r := range rows {
        if r.Count <= 3 {
            continue
        }
        out = append(out, findingDraft{
            Rule:  RuleDedupPrompt,
            Scope: map[string]any{"prompt_hash": r.Hash},
            Evidence: map[string]any{
                "repeat_count": r.Count,
                "model":        r.Model,
                "window_hours": 24,
            },
            Action: map[string]any{
                "type": "semantic_cache",
                "hint": "enable cache.semantic or cache.tiered for this traffic",
            },
        })
    }
    return out
}

// localModelRow is one local model's 7-day distinct-day request footprint.
type localModelRow struct {
    Model string
    Days  int
    N     int
}

// evalPreloadModel (plan §14.2): >= 3 distinct local models in active use
// over 7 days while average system memory ratio stays under 85% — enough
// headroom to keep the hot set resident instead of paying repeated cold
// loads. Advisory: model preloading lives in fusion-mlx, not gateway config.
func evalPreloadModel(rows []localModelRow, avgMemRatio float64) []findingDraft {
    if len(rows) < 3 || avgMemRatio <= 0 || avgMemRatio >= 0.85 {
        return nil
    }
    models := make([]string, 0, len(rows))
    for _, r := range rows {
        models = append(models, r.Model)
    }
    return []findingDraft{{
        Rule:  RulePreloadModel,
        Scope: map[string]any{"backend": "local"},
        Evidence: map[string]any{
            "distinct_models":  len(rows),
            "models":           models,
            "avg_sys_mem_ratio": avgMemRatio,
            "window_days":      7,
        },
    }}
}

// fetchModelSplit pulls per-model 7-day local/cloud split with TTFT P95 per
// side (generation rows only, ttft_ms > 0) and the oversized-prompt tally.
func (re *RulesEngine) fetchModelSplit(ctx context.Context) ([]modelSplitRow, error) {
    rows, err := re.svc.pool.Query(ctx,
        `SELECT COALESCE(model, ''), count(*),
                count(*) FILTER (WHERE backend_type = 'cloud'),
                count(*) FILTER (WHERE backend_type = 'local'),
                count(*) FILTER (WHERE backend_type = 'local' AND NOT is_success),
                COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms)
                    FILTER (WHERE backend_type = 'cloud' AND ttft_ms > 0), 0),
                COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY ttft_ms)
                    FILTER (WHERE backend_type = 'local' AND ttft_ms > 0), 0),
                count(*) FILTER (WHERE backend_type = 'local' AND ttft_ms > 0),
                count(*) FILTER (WHERE input_tokens > 0 AND output_tokens > 0
                    AND input_tokens > 50 * output_tokens)
         FROM fg_request_logs
         WHERE ts >= now() - interval '7 days'
         GROUP BY COALESCE(model, '')`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []modelSplitRow{}
    for rows.Next() {
        var r modelSplitRow
        if err := rows.Scan(&r.Model, &r.Total, &r.CloudN, &r.LocalN,
            &r.LocalErr, &r.CloudP95, &r.LocalP95, &r.LocalGen, &r.TrimN); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// fetchLocalHourErrors pulls local-backend error tallies grouped by
// hour-of-day over 7 days.
func (re *RulesEngine) fetchLocalHourErrors(ctx context.Context) ([]hourErrRow, error) {
    rows, err := re.svc.pool.Query(ctx,
        `SELECT extract(hour FROM ts)::int, count(*),
                count(*) FILTER (WHERE NOT is_success)
         FROM fg_request_logs
         WHERE ts >= now() - interval '7 days' AND backend_type = 'local'
         GROUP BY 1`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []hourErrRow{}
    for rows.Next() {
        var r hourErrRow
        if err := rows.Scan(&r.Hour, &r.Total, &r.Errs); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// fetchPromptDupes pulls exact-duplicate request bodies (md5 of req_body)
// sent more than 3 times in the last 24h on chat endpoints. Hashed in SQL —
// the bodies never leave the DB. fg_request_payloads is partitioned by ts
// like fg_request_logs, so the join is on (id, ts).
func (re *RulesEngine) fetchPromptDupes(ctx context.Context) ([]promptDupeRow, error) {
    rows, err := re.svc.pool.Query(ctx,
        `SELECT md5(p.req_body::text), count(*), min(COALESCE(l.model, ''))
         FROM fg_request_payloads p
         JOIN fg_request_logs l ON l.id = p.id AND l.ts = p.ts
         WHERE p.ts >= now() - interval '24 hours'
           AND p.req_body IS NOT NULL
           AND l.endpoint IN ('/v1/chat/completions', '/v1/completions', '/v1/messages')
         GROUP BY 1 HAVING count(*) > 3
         ORDER BY 2 DESC LIMIT 50`)
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    out := []promptDupeRow{}
    for rows.Next() {
        var r promptDupeRow
        if err := rows.Scan(&r.Hash, &r.Count, &r.Model); err != nil {
            return nil, err
        }
        out = append(out, r)
    }
    return out, rows.Err()
}

// fetchLocalModelDays pulls the local model footprint plus the 7-day average
// system memory ratio from fg_hw_samples (the PRELOAD_MODEL memory guard).
func (re *RulesEngine) fetchLocalModelDays(ctx context.Context) ([]localModelRow, float64, error) {
    rows, err := re.svc.pool.Query(ctx,
        `SELECT COALESCE(model, ''),
                count(DISTINCT date_trunc('day', ts)), count(*)
         FROM fg_request_logs
         WHERE ts >= now() - interval '7 days' AND backend_type = 'local'
         GROUP BY 1`)
    if err != nil {
        return nil, 0, err
    }
    defer rows.Close()
    out := []localModelRow{}
    for rows.Next() {
        var r localModelRow
        if err := rows.Scan(&r.Model, &r.Days, &r.N); err != nil {
            return nil, 0, err
        }
        out = append(out, r)
    }
    if err := rows.Err(); err != nil {
        return nil, 0, err
    }
    var avgMem float64
    err = re.svc.pool.QueryRow(ctx,
        `SELECT COALESCE(avg(sys_mem_ratio), 0)
         FROM fg_hw_samples
         WHERE ts >= now() - interval '7 days' AND collection_error = false`).Scan(&avgMem)
    if err != nil {
        return nil, 0, err
    }
    return out, avgMem, nil
}
