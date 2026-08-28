package cluster

// ClusterNodeProvider — wraps a remote cluster node as adapter.Provider
// Called from: internal/cluster/discovery.go (builds providers), internal/server/server.go (routes requests)
// API: Chat/StreamChat/Embedding/ListModels/HealthCheck — same as adapter.Provider interface
// User instruction: "#23" — Task #23 cluster node config & registration discovery

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

type ClusterNodeProvider struct {
    node             *Node
    httpClient       *http.Client
    apiKey           string
    routeHeader      string
    routeHeaderValue string
}

func NewClusterNodeProvider(node *Node, routingCfg config.RoutingConfig, sharedToken string) *ClusterNodeProvider {
    // H4 (audit P1): route the cluster inference path through TransportForBackend
    // so it inherits the MaxConnsPerHost FD cap (default 16). The prior bare
    // &http.Client{Timeout} inherited http.DefaultTransport (MaxConnsPerHost=0 =
    // unlimited) — 100 concurrent requests to one node opened 100 connections,
    // and a 5-gateway fan-out exhausted the node's FD table (macOS ulimit -n 256).
    return &ClusterNodeProvider{
        node:             node,
        httpClient: &http.Client{
            Timeout:   120 * time.Second,
            Transport: httpx.TransportForBackend(config.BackendConfig{BaseURL: node.Address}),
        },
        apiKey:           sharedToken,
        routeHeader:      routingCfg.Negotiation.RouteHeader,
        routeHeaderValue: routingCfg.Negotiation.RouteHeaderValue,
    }
}

func (p *ClusterNodeProvider) Name() string {
    return fmt.Sprintf("cluster-%s", p.node.ID)
}

func (p *ClusterNodeProvider) NodeID() string {
    return p.node.ID
}

func (p *ClusterNodeProvider) HealthCheck(ctx context.Context) error {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.node.Address+"/health", nil)
    if err != nil {
        return fmt.Errorf("create health check request: %w", err)
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("health check failed: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("health check returned status %d", resp.StatusCode)
    }
    return nil
}

func (p *ClusterNodeProvider) Chat(ctx context.Context, req *adapter.ChatRequest) (*adapter.ChatResponse, error) {
    p.node.IncrInFlight()
    defer p.node.DecrInFlight()

    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal chat request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.node.Address+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create chat request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("chat request to node %s failed: %w", p.node.ID, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        respBody := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("chat to node %s status %d: %s", p.node.ID, resp.StatusCode, string(respBody))
    }

    var chatResp adapter.ChatResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode chat response from node %s: %w", p.node.ID, err)
    }

    return &chatResp, nil
}

func (p *ClusterNodeProvider) StreamChat(ctx context.Context, req *adapter.ChatRequest) (<-chan adapter.StreamChunk, error) {
    p.node.IncrInFlight()

    body, err := json.Marshal(req)
    if err != nil {
        p.node.DecrInFlight()
        return nil, fmt.Errorf("marshal stream chat request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.node.Address+"/v1/chat/completions", bytes.NewReader(body))
    if err != nil {
        p.node.DecrInFlight()
        return nil, fmt.Errorf("create stream chat request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        p.node.DecrInFlight()
        return nil, fmt.Errorf("stream chat to node %s failed: %w", p.node.ID, err)
    }

    if resp.StatusCode != http.StatusOK {
        p.node.DecrInFlight()
        respBody := httpx.ReadErrorBody(resp)
        resp.Body.Close()
        return nil, fmt.Errorf("stream chat to node %s status %d: %s", p.node.ID, resp.StatusCode, httpx.TruncateForError(respBody))
    }

    // L6 fix: larger buffer to reduce backpressure risk
    ch := make(chan adapter.StreamChunk, 256)

    safego.Go("cluster_node_stream", func() {
        defer close(ch)
        defer p.node.DecrInFlight()
        defer resp.Body.Close()

        // B8: ctx-watcher closes resp.Body on client cancel, which unblocks the
        // decoder.Decode read below. Without this, a slow/quiet upstream keeps
        // Decode blocked in body.Read for minutes after the client is gone — the
        // transport ctx cancel does eventually propagate, but only on the next
        // read attempt, which may not come. Closing the body forces an immediate
        // read error and a clean exit.
        stopBodyWatch := make(chan struct{})
        defer close(stopBodyWatch)
        safego.Go("cluster_node_stream_cancel_watch", func() {
            select {
            case <-ctx.Done():
                slog.Debug("cluster node stream canceled by client, closing body", "node_id", p.node.ID, "error", ctx.Err())
                resp.Body.Close()
            case <-stopBodyWatch:
            }
        })

        decoder := json.NewDecoder(resp.Body)
        for {
            var chunk adapter.StreamChunk
            if err := decoder.Decode(&chunk); err != nil {
                if err != io.EOF {
                    // Distinguish client cancel (ctx.Err set) from a genuine
                    // upstream decode error: cancel is silent, decode errors
                    // are logged (B8: truncation/abort is observable, not the
                    // prior silent default-drop).
                    if ctx.Err() != nil {
                        slog.Debug("cluster node stream decode ended on cancel", "node_id", p.node.ID, "error", err)
                    } else {
                        slog.Error("cluster node sse decode error", "node_id", p.node.ID, "error", err)
                    }
                }
                return
            }
            // B8: ctx-aware send. The prior nested `default: return` fired
            // whenever the 256-buffer filled, silently dropping tail data with
            // no log. Cancel now exits cleanly; a full buffer is a real
            // backpressure condition that is logged (observable), not swallowed.
            select {
            case ch <- chunk:
            case <-ctx.Done():
                slog.Debug("cluster node stream send canceled by client", "node_id", p.node.ID, "error", ctx.Err())
                return
            default:
                slog.Warn("cluster node sse backpressure exceeded, stream truncated", "node_id", p.node.ID)
                return
            }
        }
    })

    return ch, nil
}

func (p *ClusterNodeProvider) Embedding(ctx context.Context, req *adapter.EmbeddingRequest) (*adapter.EmbeddingResponse, error) {
    p.node.IncrInFlight()
    defer p.node.DecrInFlight()

    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal embedding request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.node.Address+"/v1/embeddings", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create embedding request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("embedding to node %s failed: %w", p.node.ID, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        respBody := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("embedding to node %s status %d: %s", p.node.ID, resp.StatusCode, string(respBody))
    }

    var embResp adapter.EmbeddingResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&embResp); err != nil {
        return nil, fmt.Errorf("decode embedding response from node %s: %w", p.node.ID, err)
    }

    return &embResp, nil
}

func (p *ClusterNodeProvider) Rerank(ctx context.Context, req *adapter.RerankRequest) (*adapter.RerankResponse, error) {
    p.node.IncrInFlight()
    defer p.node.DecrInFlight()

    body, err := json.Marshal(req)
    if err != nil {
        return nil, fmt.Errorf("marshal rerank request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.node.Address+"/v1/rerank", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("create rerank request: %w", err)
    }

    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }
    httpReq.Header.Set(p.routeHeader, p.routeHeaderValue)

    resp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("rerank to node %s failed: %w", p.node.ID, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        // RR10 (audit P0): bounded error body via ReadErrorBody (1 MiB cap).
        respBody := httpx.ReadErrorBody(resp)
        return nil, fmt.Errorf("rerank to node %s status %d: %s", p.node.ID, resp.StatusCode, string(respBody))
    }

    var rerankResp adapter.RerankResponse
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&rerankResp); err != nil {
        return nil, fmt.Errorf("decode rerank response from node %s: %w", p.node.ID, err)
    }

    return &rerankResp, nil
}

func (p *ClusterNodeProvider) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.node.Address+"/v1/models", nil)
    if err != nil {
        return nil, fmt.Errorf("create list models request: %w", err)
    }

    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.httpClient.Do(req)
    if err != nil {
        return nil, fmt.Errorf("list models from node %s failed: %w", p.node.ID, err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("list models from node %s status %d", p.node.ID, resp.StatusCode)
    }

    var listResp struct {
        Data []adapter.ModelInfo `json:"data"`
    }
    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    if err := json.NewDecoder(httpx.LimitResponseReader(resp.Body)).Decode(&listResp); err != nil {
        return nil, fmt.Errorf("decode models from node %s: %w", p.node.ID, err)
    }

    return listResp.Data, nil
}
