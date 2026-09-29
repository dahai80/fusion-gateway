package server

import (
    "bytes"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/safego"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// batchWorker drains pending batches created via POST /v1/batches (#173).
// Restores the execution path deleted in 32a1217 (internal/batch package):
// the M1 audit had disabled batch creation with a permanent 501 because no
// worker consumed submissions. Each BatchRequest is executed by looping the
// request back through the server's own mux, so batch traffic goes through
// the same middleware chain (auth, rate limit, routing, cost tracking) as
// direct client calls — no second execution path to maintain.
//
// Concurrency: one worker goroutine (safego.GoRestart) polls the store at
// batch.PollInterval; per-request failures increment Batch.Failed and are
// recorded as BatchResult.Error — the batch still completes. Cancelled
// batches are skipped. The loop exits when the shutdown context is done.

const (
    batchPollInterval = 5 * time.Second
    batchMaxParallel  = 4
)

// startBatchWorker launches the background batch processor. No-op when the
// mux is not wired yet (Start) or batching is disabled in config.
func (s *Server) startBatchWorker() {
    if s.mux == nil {
        return
    }
    if !s.cfg.Config.Batch.Enabled {
        return
    }
    safego.GoRestart("batch_worker", func() {
        ticker := time.NewTicker(batchPollInterval)
        defer ticker.Stop()
        for {
            select {
            case <-s.shutdownCtx.Done():
                return
            case <-ticker.C:
                s.processPendingBatches()
            }
        }
    })
    slog.Info("batch worker started", "poll_interval", batchPollInterval)
}

// processPendingBatches picks up pending batches and executes each one.
func (s *Server) processPendingBatches() {
    batches, err := s.store.ListBatches()
    if err != nil {
        slog.Error("batch worker: list batches failed", "error", err)
        return
    }
    for _, b := range batches {
        if b.Status != store.BatchStatusPending {
            continue
        }
        select {
        case <-s.shutdownCtx.Done():
            return
        default:
        }
        s.processBatch(b)
    }
}

// processBatch runs a single batch to completion: marks it running, executes
// every request through the internal mux, then updates counters and status.
func (s *Server) processBatch(b *store.Batch) {
    b.Status = store.BatchStatusRunning
    if err := s.store.UpdateBatch(b); err != nil {
        slog.Error("batch worker: mark running failed", "batch_id", b.ID, "error", err)
        return
    }
    slog.Info("batch worker: processing batch", "batch_id", b.ID, "requests", len(b.Requests))

    sem := make(chan struct{}, batchMaxParallel)
    done := make(chan *store.BatchResult, len(b.Requests))
    for i := range b.Requests {
        req := b.Requests[i]
        sem <- struct{}{}
        safego.Go("batch_request", func() {
            defer func() { <-sem }()
            done <- s.executeBatchRequest(req)
        })
    }
    for range b.Requests {
        res := <-done
        b.Results = append(b.Results, *res)
        if res.Error != "" {
            b.Failed++
        } else {
            b.Completed++
        }
    }

    now := time.Now()
    b.CompletedAt = &now
    b.Status = store.BatchStatusCompleted
    if err := s.store.UpdateBatch(b); err != nil {
        slog.Error("batch worker: finalize failed", "batch_id", b.ID, "error", err)
        return
    }
    slog.Info("batch worker: batch completed",
        "batch_id", b.ID,
        "completed", b.Completed,
        "failed", b.Failed,
        "total", b.Total,
    )
}

// executeBatchRequest loops one BatchRequest back through the server's own
// mux so it traverses the identical middleware chain as external traffic.
// The request is redirected to localhost so the Host header matches a local
// route (never the original external target); auth uses the configured
// master key so the worker can call /v1/* endpoints even when anonymous
// access is disabled.
func (s *Server) executeBatchRequest(br store.BatchRequest) *store.BatchResult {
    result := &store.BatchResult{CustomID: br.CustomID}

    if br.Method == "" {
        br.Method = http.MethodPost
    }
    var body io.Reader
    if len(br.Body) > 0 {
        body = bytes.NewReader(br.Body)
    }
    req, err := http.NewRequest(br.Method, "http://localhost"+br.URL, body)
    if err != nil {
        result.Error = fmt.Sprintf("invalid batch request: %v", err)
        return result
    }
    req.Header.Set("Content-Type", "application/json")
    if mk := s.masterKey(); mk != "" {
        req.Header.Set("Authorization", "Bearer "+mk)
    }

    rec := httptest.NewRecorder()
    s.mux.ServeHTTP(rec, req)

    result.Response = &store.BatchResponse{
        StatusCode: rec.Code,
        Body:       rec.Body.Bytes(),
    }
    if rec.Code >= http.StatusBadRequest {
        result.Error = fmt.Sprintf("upstream status %d", rec.Code)
    }
    return result
}

// masterKey returns the effective master key for internal batch calls.
func (s *Server) masterKey() string {
    return s.cfg.Config.Auth.MasterKey
}
