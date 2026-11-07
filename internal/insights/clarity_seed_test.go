package insights

import (
    "context"
    "time"
)

// seedRequestLog inserts one fg_request_logs row with the given
// error-shaped fields (tokens zero).
func seedRequestLog(ctx context.Context, svc *Service, ts time.Time, tag, endpoint string, status int, errMsg, model string) error {
    _, err := svc.pool.Exec(ctx, `
        INSERT INTO fg_request_logs
            (id, ts, request_id, scenario, endpoint, request_type, is_stream,
             model, backend, backend_type, status_code, is_success, error_message, contains_pii)
        VALUES (gen_random_uuid(), $1, $2, $3, $4, 'chat', false,
                $5, 'test-backend', 'local', $6, $7, NULLIF($8, ''), false)`,
        ts, "req-"+tag, tag, endpoint, model, status, status < 400, errMsg)
    return err
}

// seedRequestLogTokens inserts one fg_request_logs row with token counts
// for the spend-by-key aggregation.
func seedRequestLogTokens(ctx context.Context, svc *Service, ts time.Time, tag, endpoint string, status int, model, keyName string, in, out int64) error {
    _, err := svc.pool.Exec(ctx, `
        INSERT INTO fg_request_logs
            (id, ts, request_id, scenario, endpoint, request_type, is_stream,
             model, backend, backend_type, status_code, is_success,
             input_tokens, output_tokens, total_tokens, api_key_name, contains_pii)
        VALUES (gen_random_uuid(), $1, $2, $3, $4, 'chat', false,
                $5, 'test-backend', 'local', $6, $7, $8, $9, $8::int + $9::int, NULLIF($10, ''), false)`,
        ts, "req-"+tag, tag, endpoint, model, status, status < 400, in, out, keyName)
    return err
}

// seedRequestLogFull inserts one fg_request_logs row with explicit model,
// backend, key, and token counts (summary aggregation tests).
func seedRequestLogFull(ctx context.Context, svc *Service, ts time.Time, tag, endpoint string, status int, model, backend, keyName string, in, out int64) error {
    _, err := svc.pool.Exec(ctx, `
        INSERT INTO fg_request_logs
            (id, ts, request_id, scenario, endpoint, request_type, is_stream,
             model, backend, backend_type, status_code, is_success,
             input_tokens, output_tokens, total_tokens, api_key_name, contains_pii)
        VALUES (gen_random_uuid(), $1, $2, $3, $4, 'chat', false,
                $5, $6, 'local', $7, $8, $9, $10, $9::int + $10::int, NULLIF($11, ''), false)`,
        ts, "req-"+tag, tag, endpoint, model, backend, status, status < 400, in, out, keyName)
    return err
}
