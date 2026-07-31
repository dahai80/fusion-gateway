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
    return &ClusterNodeProvider{
        node:             node,
        httpClient:       &http.Client{Timeout: 120 * time.Second},
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
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("chat to node %s status %d: %s", p.node.ID, resp.StatusCode, string(respBody))
    }

    var chatResp adapter.ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
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
        respBody, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        return nil, fmt.Errorf("stream chat to node %s status %d: %s", p.node.ID, resp.StatusCode, string(respBody))
    }

    // L6 fix: larger buffer to reduce backpressure risk
    ch := make(chan adapter.StreamChunk, 256)

    safego.Go("cluster_node_stream", func() {
        defer close(ch)
        defer p.node.DecrInFlight()
        defer resp.Body.Close()

        decoder := json.NewDecoder(resp.Body)
        for {
            var chunk adapter.StreamChunk
            if err := decoder.Decode(&chunk); err != nil {
                if err != io.EOF {
                    slog.Error("cluster node sse decode error", "node_id", p.node.ID, "error", err)
                }
                return
            }
            select {
            case ch <- chunk:
            default:
                slog.Warn("cluster node sse backpressure, draining", "node_id", p.node.ID)
                select {
                case ch <- chunk:
                default:
                    slog.Error("cluster node sse backpressure exceeded, stream truncated", "node_id", p.node.ID)
                    return
                }
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
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("embedding to node %s status %d: %s", p.node.ID, resp.StatusCode, string(respBody))
    }

    var embResp adapter.EmbeddingResponse
    if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
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
        respBody, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("rerank to node %s status %d: %s", p.node.ID, resp.StatusCode, string(respBody))
    }

    var rerankResp adapter.RerankResponse
    if err := json.NewDecoder(resp.Body).Decode(&rerankResp); err != nil {
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
    if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
        return nil, fmt.Errorf("decode models from node %s: %w", p.node.ID, err)
    }

    return listResp.Data, nil
}
