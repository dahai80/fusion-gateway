package server

import (
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/cluster"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    var req adapter.EmbeddingRequest
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    inputLen := len(req.Input)

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    // Route through router engine for embedding request type
    routeReq := &router.RouteRequest{
        Model: req.Model,
        Type:  router.RequestTypeEmbedding,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    slog.Info("embedding route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_count", inputLen,
    )

    // Try cluster sharding for large batch + cluster route
    if decision.Backend == router.ClusterBackend && inputLen > 32 && s.clusterDiscovery != nil {
        if d, ok := s.clusterDiscovery.(*cluster.Discovery); ok {
            resp, err := cluster.ShardEmbedding(ctx, d, &req, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            if err == nil {
                w.Header().Set("Content-Type", "application/json")
                w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
                _ = json.NewEncoder(w).Encode(resp)
                return
            }
            slog.Warn("cluster embedding sharding failed, falling back to single node",
                "error", err,
                "input_count", inputLen,
            )
        }
    }

    // Resolve provider based on routing decision
    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, err := s.pool.GetByBackend("fusion-mlx")
        if err != nil {
            http.Error(w, `{"error":{"message":"Local embedding backend not available"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p

    case router.ClusterBackend:
        if s.clusterDiscovery != nil && decision.NodeID != "" {
            if node, ok := s.clusterDiscovery.GetNode(decision.NodeID); ok {
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            }
        }
        if provider == nil {
            p := s.resolveCloudProvider(decision, nil, w)
            if p == nil {
                return
            }
            provider = p
        }

    default:
        p := s.resolveCloudProvider(decision, nil, w)
        if p == nil {
            return
        }
        provider = p
    }

    embStart := time.Now()
    resp, err := provider.Embedding(ctx, &req)
    if err != nil {
        // RR4: local slot full = expected diversion. Re-route the embedding to
        // cloud (model-mapped) instead of 502; no breaker failure record.
        if errors.Is(err, adapter.ErrLocalSlotFull) && decision.Backend == router.LocalBackend {
            slog.Info("RR4 local slot full, diverting embedding to cloud",
                "model", req.Model, "reason", "max_concurrent reached")
            cloudDecision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "rr4_slot_full_redirect"}
            cloudProvider := s.resolveCloudProvider(cloudDecision, nil, nil)
            if cloudProvider != nil {
                cloudReq := req
                if mapped := s.applyCloudModelMapping(cloudReq.Model, cloudProvider.Name()); mapped != cloudReq.Model {
                    cloudReq.Model = mapped
                }
                cloudResp, cloudErr := cloudProvider.Embedding(ctx, &cloudReq)
                if cloudErr == nil {
                    embDuration := time.Since(embStart)
                    if s.latencyTracker != nil {
                        s.latencyTracker.Record(cloudProvider.Name(), embDuration)
                    }
                    // Cost tracking (#159: record + charge quota in one sink)
                    s.recordAndCharge(ctx, "cloud", cloudReq.Model, inputLen, 0)
                    w.Header().Set("Content-Type", "application/json")
                    w.Header().Set("X-Route-Decision", "cloud:rr4_slot_full_redirect")
                    _ = json.NewEncoder(w).Encode(cloudResp)
                    return
                }
                slog.Error("RR4 embedding cloud diversion failed", "error", cloudErr)
            }
        }
        slog.Error("embedding failed", "provider", provider.Name(), "error", err)
        s.recordOutcome(decision.Backend, decision.NodeID, false)
        http.Error(w, `{"error":{"message":"Embedding failed"}}`, http.StatusBadGateway)
        return
    }

    embDuration := time.Since(embStart)
    s.recordOutcome(decision.Backend, decision.NodeID, true)

    // Latency + cost tracking for embedding
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), embDuration)
    }
    // Cost tracking (#159: record + charge quota in one sink)
    s.recordAndCharge(ctx, string(decision.Backend), req.Model, inputLen, 0)

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRerank(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    var req adapter.RerankRequest
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    routeReq := &router.RouteRequest{
        Model: req.Model,
        Type:  router.RequestTypeRerank,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    slog.Info("rerank route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
    )

    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, err := s.pool.GetByBackend("fusion-mlx")
        if err != nil {
            http.Error(w, `{"error":{"message":"Local rerank backend not available"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p

    case router.ClusterBackend:
        if s.clusterDiscovery != nil && decision.NodeID != "" {
            if node, ok := s.clusterDiscovery.GetNode(decision.NodeID); ok {
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            }
        }
        if provider == nil {
            p := s.resolveCloudProvider(decision, nil, w)
            if p == nil {
                return
            }
            provider = p
        }

    default:
        p := s.resolveCloudProvider(decision, nil, w)
        if p == nil {
            return
        }
        provider = p
    }

    start := time.Now()
    resp, err := provider.Rerank(ctx, &req)
    if err != nil {
        // RR4: local slot full = expected diversion. Re-route the rerank to
        // cloud (model-mapped) instead of 502; no breaker failure record.
        if errors.Is(err, adapter.ErrLocalSlotFull) && decision.Backend == router.LocalBackend {
            slog.Info("RR4 local slot full, diverting rerank to cloud",
                "model", req.Model, "reason", "max_concurrent reached")
            cloudDecision := &router.RouteDecision{Backend: router.CloudBackend, Reason: "rr4_slot_full_redirect"}
            cloudProvider := s.resolveCloudProvider(cloudDecision, nil, nil)
            if cloudProvider != nil {
                cloudReq := req
                if mapped := s.applyCloudModelMapping(cloudReq.Model, cloudProvider.Name()); mapped != cloudReq.Model {
                    cloudReq.Model = mapped
                }
                cloudResp, cloudErr := cloudProvider.Rerank(ctx, &cloudReq)
                if cloudErr == nil {
                    duration := time.Since(start)
                    observability.RecordRequest("cloud", cloudReq.Model, "success")
                    observability.RecordDuration("cloud", cloudReq.Model, duration.Seconds())
                    if s.latencyTracker != nil {
                        s.latencyTracker.Record(cloudProvider.Name(), duration)
                    }
                    // Cost tracking (#159: record + charge quota in one sink)
                    s.recordAndCharge(ctx, "cloud", cloudReq.Model, len(cloudReq.Documents), 0)
                    w.Header().Set("Content-Type", "application/json")
                    w.Header().Set("X-Route-Decision", "cloud:rr4_slot_full_redirect")
                    _ = json.NewEncoder(w).Encode(cloudResp)
                    return
                }
                slog.Error("RR4 rerank cloud diversion failed", "error", cloudErr)
            }
        }
        slog.Error("rerank failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.recordOutcome(decision.Backend, decision.NodeID, false)
        http.Error(w, `{"error":{"message":"Rerank failed"}}`, http.StatusBadGateway)
        return
    }

    duration := time.Since(start)
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration.Seconds())
    s.recordOutcome(decision.Backend, decision.NodeID, true)

    // Latency + cost tracking for rerank
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), duration)
    }
    // Cost tracking (#159: record + charge quota in one sink)
    s.recordAndCharge(ctx, string(decision.Backend), req.Model, len(req.Documents), 0)

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    _ = json.NewEncoder(w).Encode(resp)
}
