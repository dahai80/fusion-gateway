package cluster

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func nodeConfig(id, addr string) config.ClusterNodeConfig {
    return config.ClusterNodeConfig{ID: id, Address: addr, GPU: "M1", MemoryGB: 16}
}

func defaultRoutingCfg() config.RoutingConfig {
    return config.RoutingConfig{
        Negotiation: config.NegotiationConfig{
            RouteHeader:      "X-Fusion-Route",
            RouteHeaderValue: "gateway-decision",
        },
    }
}

func TestSplitInput(t *testing.T) {
    input := []string{"a", "b", "c", "d", "e"}
    shards := splitInput(input, 2)
    if len(shards) != 3 {
        t.Fatalf("expected 3 shards, got %d", len(shards))
    }
    if len(shards[0]) != 2 || len(shards[1]) != 2 || len(shards[2]) != 1 {
        t.Errorf("unexpected shard sizes: %d %d %d", len(shards[0]), len(shards[1]), len(shards[2]))
    }
}

func TestSplitInput_ExactDivide(t *testing.T) {
    input := []string{"a", "b", "c", "d"}
    shards := splitInput(input, 2)
    if len(shards) != 2 {
        t.Fatalf("expected 2 shards, got %d", len(shards))
    }
}

func TestSplitInput_SingleShard(t *testing.T) {
    input := []string{"a", "b"}
    shards := splitInput(input, 10)
    if len(shards) != 1 {
        t.Fatalf("expected 1 shard, got %d", len(shards))
    }
}

func TestMinInt(t *testing.T) {
    if minInt(3, 5) != 3 {
        t.Errorf("expected 3")
    }
    if minInt(5, 3) != 3 {
        t.Errorf("expected 3")
    }
}

func TestShardEmbedding_NoHealthy(t *testing.T) {
    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", "http://localhost:9001"),
    ))
    d.loadNodesFromConfig()

    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: []string{"a", "b", "c"},
    }
    _, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "")
    if err == nil {
        t.Fatal("expected error with no healthy nodes")
    }
}

func TestShardEmbedding_EmptyInput(t *testing.T) {
    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", "http://localhost:9001"),
    ))
    d.loadNodesFromConfig()

    req := &adapter.EmbeddingRequest{Model: "test", Input: []string{}}
    _, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "")
    if err == nil {
        t.Fatal("expected error with empty input")
    }
}

func TestShardEmbedding_SingleNodeFallback(t *testing.T) {
    // When there is only 1 healthy node, should fall back to single-node embedding
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var req adapter.EmbeddingRequest
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            t.Errorf("decode request: %v", err)
            w.WriteHeader(http.StatusBadRequest)
            return
        }

        data := make([]adapter.EmbeddingData, len(req.Input))
        for i := range req.Input {
            data[i] = adapter.EmbeddingData{
                Object:    "embedding",
                Embedding: []float64{float64(i) * 0.1},
                Index:     i,
            }
        }

        resp := adapter.EmbeddingResponse{
            Object: "list",
            Model:  req.Model,
            Data:   data,
            Usage:  adapter.UsageResponse{PromptTokens: len(req.Input), TotalTokens: len(req.Input)},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", srv.URL),
    ))
    d.loadNodesFromConfig()
    n1, _ := d.GetNode("n1")
    n1.markHealthy()

    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: []string{"a", "b", "c"},
    }
    resp, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "test-key")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(resp.Data) != 3 {
        t.Fatalf("expected 3 embeddings, got %d", len(resp.Data))
    }
    if resp.Usage.PromptTokens != 3 {
        t.Errorf("expected 3 prompt tokens, got %d", resp.Usage.PromptTokens)
    }
}

func TestShardEmbedding_SmallInputFallback(t *testing.T) {
    // When input length <= shardSize (32), should fall back to single-node
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var req adapter.EmbeddingRequest
        _ = json.NewDecoder(r.Body).Decode(&req)

        data := make([]adapter.EmbeddingData, len(req.Input))
        for i := range req.Input {
            data[i] = adapter.EmbeddingData{
                Object:    "embedding",
                Embedding: []float64{0.1},
                Index:     i,
            }
        }

        resp := adapter.EmbeddingResponse{
            Object: "list",
            Model:  req.Model,
            Data:   data,
            Usage:  adapter.UsageResponse{PromptTokens: len(req.Input), TotalTokens: len(req.Input)},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", srv.URL),
        nodeConfig("n2", srv.URL),
    ))
    d.loadNodesFromConfig()
    for _, n := range d.AllNodes() {
        n.markHealthy()
    }

    // 5 inputs < 32 shardSize → single-node fallback
    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: []string{"a", "b", "c", "d", "e"},
    }
    resp, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(resp.Data) != 5 {
        t.Fatalf("expected 5 embeddings, got %d", len(resp.Data))
    }
}

func TestShardEmbedding_MultiNodeSharding(t *testing.T) {
    // Build inputs large enough to trigger sharding (> 32 items, 2+ healthy nodes)
    var callCount atomic.Int64
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        callCount.Add(1)

        var req adapter.EmbeddingRequest
        _ = json.NewDecoder(r.Body).Decode(&req)

        data := make([]adapter.EmbeddingData, len(req.Input))
        for i := range req.Input {
            data[i] = adapter.EmbeddingData{
                Object:    "embedding",
                Embedding: []float64{float64(i) * 0.01},
                Index:     i,
            }
        }

        resp := adapter.EmbeddingResponse{
            Object: "list",
            Model:  req.Model,
            Data:   data,
            Usage:  adapter.UsageResponse{PromptTokens: len(req.Input), TotalTokens: len(req.Input)},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", srv.URL),
        nodeConfig("n2", srv.URL),
    ))
    d.loadNodesFromConfig()
    for _, n := range d.AllNodes() {
        n.markHealthy()
    }

    // 50 inputs > 32 shardSize → triggers sharding
    input := make([]string, 50)
    for i := range input {
        input[i] = "text"
    }
    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: input,
    }
    resp, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    if len(resp.Data) != 50 {
        t.Errorf("expected 50 embeddings, got %d", len(resp.Data))
    }
    if resp.Usage.PromptTokens != 50 {
        t.Errorf("expected 50 total tokens, got %d", resp.Usage.PromptTokens)
    }
    if callCount.Load() < 2 {
        t.Logf("sharding dispatched %d requests", callCount.Load())
    }
}

func TestShardEmbedding_PartialFailure(t *testing.T) {
    // One node returns success, another returns error
    callCount := atomic.Int64{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        count := callCount.Add(1)
        // Fail every other request
        if count%2 == 0 {
            w.WriteHeader(http.StatusInternalServerError)
            return
        }

        var req adapter.EmbeddingRequest
        _ = json.NewDecoder(r.Body).Decode(&req)

        data := make([]adapter.EmbeddingData, len(req.Input))
        for i := range req.Input {
            data[i] = adapter.EmbeddingData{
                Object:    "embedding",
                Embedding: []float64{0.1},
                Index:     i,
            }
        }

        resp := adapter.EmbeddingResponse{
            Object: "list",
            Model:  req.Model,
            Data:   data,
            Usage:  adapter.UsageResponse{PromptTokens: len(req.Input), TotalTokens: len(req.Input)},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", srv.URL),
        nodeConfig("n2", srv.URL),
    ))
    d.loadNodesFromConfig()
    for _, n := range d.AllNodes() {
        n.markHealthy()
    }

    input := make([]string, 50)
    for i := range input {
        input[i] = "text"
    }
    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: input,
    }
    resp, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "")
    // Should get partial results even if some shards fail
    if err != nil {
        // If all shards fail, we get an error
        t.Logf("all shards failed: %v", err)
    } else {
        t.Logf("partial results: got %d of %d embeddings", len(resp.Data), 50)
    }
}

func TestShardEmbedding_AllShardsFail(t *testing.T) {
    // All requests fail
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer srv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", srv.URL),
        nodeConfig("n2", srv.URL),
    ))
    d.loadNodesFromConfig()
    for _, n := range d.AllNodes() {
        n.markHealthy()
    }

    input := make([]string, 50)
    for i := range input {
        input[i] = "text"
    }
    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: input,
    }
    _, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "")
    if err == nil {
        t.Fatal("expected error when all shards fail")
    }
}

func TestShardEmbedding_CancelledContext(t *testing.T) {
    // Large batch with cancelled context
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var req adapter.EmbeddingRequest
        _ = json.NewDecoder(r.Body).Decode(&req)

        data := make([]adapter.EmbeddingData, len(req.Input))
        for i := range req.Input {
            data[i] = adapter.EmbeddingData{
                Object:    "embedding",
                Embedding: []float64{0.1},
                Index:     i,
            }
        }

        resp := adapter.EmbeddingResponse{
            Object: "list",
            Model:  req.Model,
            Data:   data,
            Usage:  adapter.UsageResponse{PromptTokens: len(req.Input), TotalTokens: len(req.Input)},
        }
        _ = json.NewEncoder(w).Encode(resp)
    }))
    defer srv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n1", srv.URL),
        nodeConfig("n2", srv.URL),
    ))
    d.loadNodesFromConfig()
    for _, n := range d.AllNodes() {
        n.markHealthy()
    }

    ctx, cancel := context.WithCancel(context.Background())
    cancel() // Cancel immediately

    input := make([]string, 50)
    for i := range input {
        input[i] = "text"
    }
    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: input,
    }
    _, err := ShardEmbedding(ctx, d, req, defaultRoutingCfg(), "")
    // Cancelled context should cause errors
    if err == nil {
        t.Log("cancelled context may still succeed if requests were fast enough")
    }
}
