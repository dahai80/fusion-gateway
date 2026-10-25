package insights

import (
    "context"
    "encoding/json"
    "fmt"
    "time"
)

// SearchQuery is the Explorer request DSL (plan §8, POST — too many filters
// for query params). All fields optional except the enforced time window.
type SearchQuery struct {
    From          string `json:"from"`
    To            string `json:"to"`
    Q             string `json:"q"`               // free text: model/error/request_id substring
    Endpoint      string `json:"endpoint"`
    Backend       string `json:"backend"`
    Model         string `json:"model"`
    ProjectID     string `json:"project_id"`
    APIKeyName    string `json:"api_key_name"`
    StatusCode    int    `json:"status_code"`
    IsStream      *bool  `json:"is_stream"`
    IsSuccess     *bool  `json:"is_success"`
    MinDurationMS float64 `json:"min_duration_ms"`
    Limit         int    `json:"limit"`
    Offset        int    `json:"offset"`
}

// SearchRow is one log row WITHOUT payload columns (plan §8 guard: list
// queries never touch fg_request_payloads).
type SearchRow struct {
    ID          string  `json:"id"`
    TS          string  `json:"ts"`
    RequestID   string  `json:"request_id"`
    APIKeyName  string  `json:"api_key_name,omitempty"`
    ProjectID   string  `json:"project_id,omitempty"`
    Endpoint    string  `json:"endpoint"`
    IsStream    bool    `json:"is_stream"`
    Model       string  `json:"model"`
    ModelMapped string  `json:"model_mapped,omitempty"`
    Backend     string  `json:"backend"`
    RouteReason string  `json:"route_reason,omitempty"`
    StatusCode  int     `json:"status_code"`
    IsSuccess   bool    `json:"is_success"`
    ErrorMessage string `json:"error_message,omitempty"`
    InputTokens  int     `json:"input_tokens"`
    OutputTokens int     `json:"output_tokens"`
    TotalTokens  int     `json:"total_tokens"`
    Cost         float64 `json:"cost"`
    DurationMS   float64 `json:"duration_ms"`
    TTFTMS       float64 `json:"ttft_ms"`
    TPotMS       float64 `json:"tpot_ms"`
    ChunkCount   int     `json:"chunk_count"`
    TokensPerSec float64 `json:"tokens_per_sec"`
    ClientIP     string  `json:"client_ip,omitempty"`
    UserAgent    string  `json:"user_agent,omitempty"`
}

// SearchResponse is the Explorer list + total for pagination.
type SearchResponse struct {
    From   string      `json:"from"`
    To     string      `json:"to"`
    Total  int64       `json:"total"`
    Limit  int         `json:"limit"`
    Offset int         `json:"offset"`
    Rows   []SearchRow `json:"rows"`
}

// Search runs the Explorer query. Parameters are bound (never interpolated);
// q matches substring on model/error_message/request_id.
func (s *Service) Search(ctx context.Context, sq SearchQuery) (*SearchResponse, error) {
    from, to, err := windowBounds(sq.From, sq.To)
    if err != nil {
        return nil, err
    }
    if sq.Limit <= 0 || sq.Limit > 200 {
        sq.Limit = 50
    }
    if sq.Offset < 0 {
        sq.Offset = 0
    }
    if sq.IsStream == nil && sq.IsSuccess == nil && sq.StatusCode == 0 &&
        sq.Endpoint == "" && sq.Backend == "" && sq.Model == "" &&
        sq.ProjectID == "" && sq.APIKeyName == "" && sq.Q == "" && sq.MinDurationMS <= 0 {
        return s.searchAll(ctx, sq, from, to)
    }

    streamVal := 0
    if sq.IsStream != nil && *sq.IsStream {
        streamVal = 1
    }
    successVal := 0
    if sq.IsSuccess != nil && *sq.IsSuccess {
        successVal = 1
    }

    where := `ts >= $1 AND ts < $2
          AND ($3 = '' OR endpoint = $3)
          AND ($4 = '' OR backend = $4)
          AND ($5 = '' OR model = $5)
          AND ($6 = '' OR project_id = $6)
          AND ($7 = '' OR api_key_name = $7)
          AND ($8 = 0 OR status_code = $8)
          AND ($9 = 0 OR is_stream = ($9 = 1))
          AND ($10 = 0 OR is_success = ($10 = 1))
          AND ($11 <= 0 OR duration_ms >= $11)
          AND ($12 = '' OR model ILIKE '%' || $12 || '%'
               OR COALESCE(error_message, '') ILIKE '%' || $12 || '%'
               OR request_id ILIKE '%' || $12 || '%')`
    args := []any{from, to, sq.Endpoint, sq.Backend, sq.Model, sq.ProjectID,
        sq.APIKeyName, sq.StatusCode, streamVal, successVal, sq.MinDurationMS, sq.Q}

    var total int64
    if err := s.pool.QueryRow(ctx,
        "SELECT count(*) FROM fg_request_logs WHERE "+where, args...).Scan(&total); err != nil {
        return nil, err
    }

    rows, err := s.pool.Query(ctx, `
        SELECT id, ts, request_id, COALESCE(api_key_name,''), COALESCE(project_id,''),
               endpoint, is_stream, model, COALESCE(model_mapped,''), backend,
               COALESCE(route_reason,''), status_code, is_success,
               COALESCE(error_message,''), COALESCE(input_tokens,0),
               COALESCE(output_tokens,0), COALESCE(total_tokens,0),
               COALESCE(cost,0), COALESCE(duration_ms,0), COALESCE(ttft_ms,0),
               COALESCE(tpot_ms,0), COALESCE(chunk_count,0),
               COALESCE(tokens_per_sec,0), COALESCE(client_ip,''), COALESCE(user_agent,'')
        FROM fg_request_logs
        WHERE `+where+`
        ORDER BY ts DESC
        LIMIT $13 OFFSET $14`,
        append(args, sq.Limit, sq.Offset)...)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    resp := fromToResp(from, to, total, sq)
    resp.Rows = []SearchRow{}
    for rows.Next() {
        var r SearchRow
        var ts time.Time
        if err := rows.Scan(&r.ID, &ts, &r.RequestID, &r.APIKeyName, &r.ProjectID,
            &r.Endpoint, &r.IsStream, &r.Model, &r.ModelMapped, &r.Backend,
            &r.RouteReason, &r.StatusCode, &r.IsSuccess, &r.ErrorMessage,
            &r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.Cost,
            &r.DurationMS, &r.TTFTMS, &r.TPotMS, &r.ChunkCount, &r.TokensPerSec,
            &r.ClientIP, &r.UserAgent); err != nil {
            return nil, err
        }
        r.TS = ts.Format(time.RFC3339Nano)
        resp.Rows = append(resp.Rows, r)
    }
    return resp, rows.Err()
}

// searchAll is the no-filter fast path (common Explorer open: just browse the
// window) — avoids the OR-heavy WHERE for the most frequent query shape.
func (s *Service) searchAll(ctx context.Context, sq SearchQuery, from, to time.Time) (*SearchResponse, error) {
    var total int64
    if err := s.pool.QueryRow(ctx,
        "SELECT count(*) FROM fg_request_logs WHERE ts >= $1 AND ts < $2", from, to).Scan(&total); err != nil {
        return nil, err
    }
    rows, err := s.pool.Query(ctx, `
        SELECT id, ts, request_id, COALESCE(api_key_name,''), COALESCE(project_id,''),
               endpoint, is_stream, model, COALESCE(model_mapped,''), backend,
               COALESCE(route_reason,''), status_code, is_success,
               COALESCE(error_message,''), COALESCE(input_tokens,0),
               COALESCE(output_tokens,0), COALESCE(total_tokens,0),
               COALESCE(cost,0), COALESCE(duration_ms,0), COALESCE(ttft_ms,0),
               COALESCE(tpot_ms,0), COALESCE(chunk_count,0),
               COALESCE(tokens_per_sec,0), COALESCE(client_ip,''), COALESCE(user_agent,'')
        FROM fg_request_logs
        WHERE ts >= $1 AND ts < $2
        ORDER BY ts DESC
        LIMIT $3 OFFSET $4`, from, to, sq.Limit, sq.Offset)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    resp := fromToResp(from, to, total, sq)
    resp.Rows = []SearchRow{}
    for rows.Next() {
        var r SearchRow
        var ts time.Time
        if err := rows.Scan(&r.ID, &ts, &r.RequestID, &r.APIKeyName, &r.ProjectID,
            &r.Endpoint, &r.IsStream, &r.Model, &r.ModelMapped, &r.Backend,
            &r.RouteReason, &r.StatusCode, &r.IsSuccess, &r.ErrorMessage,
            &r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.Cost,
            &r.DurationMS, &r.TTFTMS, &r.TPotMS, &r.ChunkCount, &r.TokensPerSec,
            &r.ClientIP, &r.UserAgent); err != nil {
            return nil, err
        }
        r.TS = ts.Format(time.RFC3339Nano)
        resp.Rows = append(resp.Rows, r)
    }
    return resp, rows.Err()
}

func fromToResp(from, to time.Time, total int64, sq SearchQuery) *SearchResponse {
    return &SearchResponse{
        From:   from.Format(time.RFC3339),
        To:     to.Format(time.RFC3339),
        Total:  total,
        Limit:  sq.Limit,
        Offset: sq.Offset,
    }
}

// Payload is the /admin/api/logs/{id}/payload response: captured bodies with
// their storage split (jsonb vs raw) reconciled for the UI. Exactly one row
// is fetched — never in list queries (plan §8 guard).
type Payload struct {
    ID           string          `json:"id"`
    TS           string          `json:"ts"`
    ReqBody      json.RawMessage `json:"req_body,omitempty"`
    RespBody     json.RawMessage `json:"resp_body,omitempty"`
    ReqRaw       string          `json:"req_raw,omitempty"`
    RespRaw      string          `json:"resp_raw,omitempty"`
    ReqTruncated bool            `json:"req_truncated"`
    RespTruncated bool           `json:"resp_truncated"`
    RespRawMime  string          `json:"resp_raw_mime,omitempty"`
}

// Payload fetches a single request's captured bodies by row id.
func (s *Service) Payload(ctx context.Context, id string) (*Payload, error) {
    var p Payload
    var ts time.Time
    err := s.pool.QueryRow(ctx, `
        SELECT p.id, p.ts,
               p.req_body, p.resp_body,
               COALESCE(p.req_raw, ''), COALESCE(p.resp_raw, ''),
               p.req_truncated, p.resp_truncated, COALESCE(p.resp_raw_mime, '')
        FROM fg_request_payloads p
        WHERE p.id = $1`, id).Scan(
        &p.ID, &ts, &p.ReqBody, &p.RespBody, &p.ReqRaw, &p.RespRaw,
        &p.ReqTruncated, &p.RespTruncated, &p.RespRawMime)
    if err != nil {
        return nil, fmt.Errorf("payload %s: %w", id, err)
    }
    p.TS = ts.Format(time.RFC3339Nano)
    return &p, nil
}
