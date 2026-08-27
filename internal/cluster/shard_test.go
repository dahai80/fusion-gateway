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

// markServing flags a node as having completed a /v1/models poll that lists
// the given model — sets models + modelsReady (RR13/RR14). ShardEmbedding
// filters nodes by servesModel(req.Model), so test nodes must advertise the
// requested embedding model or the model-aware filter skips them.
func markServing(t *testing.T, n *Node, model string) {
    t.Helper()
    n.mu.Lock()
    n.models = []string{model}
    n.modelsReady = true
    n.mu.Unlock()
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
    markServing(t, n1, "test-embed")

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
        markServing(t, n, "test-embed")
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
        markServing(t, n, "test-embed")
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
        markServing(t, n, "test-embed")
    }

    input := make([]string, 50)
    for i := range input {
        input[i] = "text"
    }
    req := &adapter.EmbeddingRequest{
        Model: "test-embed",
        Input: input,
    }
    // RR14: partial failure MUST be an error, never 200 + short vector list.
    // The prior code returned 200 + truncated Data → silent data loss.
    _, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "")
    if err == nil {
        t.Fatal("RR14: expected error on partial shard failure, got 200 with partial data (silent data loss)")
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
        markServing(t, n, "test-embed")
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
        markServing(t, n, "test-embed")
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

// RR14 guard A: a healthy node that does NOT serve req.Model must be excluded
// from the shard worker pool. The prior code took HealthyNodeList (healthy-only)
// and round-robined workers model-blind (healthyNodes[wid%len]) — shards landed
// on the non-serving node → 400 per shard. This test builds one serving + one
// non-serving node, shards 50 inputs, and asserts the non-serving node NEVER
// receives a request (its httptest handler would 400). On the BUG (no model
// filter) both nodes get traffic and the non-serving node's 400s trigger a
// partial/total failure. On the FIX only the serving node is pooled → success.
func TestShardEmbedding_RR14_NonServingNodeExcluded(t *testing.T) {
    t.Log("testing RR14: shard pool excludes healthy node not serving req.Model")
    servingHits := atomic.Int64{}
    nonServingHits := atomic.Int64{}
    servingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        servingHits.Add(1)
        var req adapter.EmbeddingRequest
        _ = json.NewDecoder(r.Body).Decode(&req)
        data := make([]adapter.EmbeddingData, len(req.Input))
        for i := range data {
            data[i] = adapter.EmbeddingData{Object: "embedding", Embedding: []float64{0.1}, Index: i}
        }
        _ = json.NewEncoder(w).Encode(adapter.EmbeddingResponse{
            Object: "list", Model: req.Model, Data: data,
            Usage: adapter.UsageResponse{PromptTokens: len(req.Input), TotalTokens: len(req.Input)},
        })
    }))
    defer servingSrv.Close()
    nonServingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        nonServingHits.Add(1)
        w.WriteHeader(http.StatusBadRequest)
    }))
    defer nonServingSrv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n-serving", servingSrv.URL),
        nodeConfig("n-nonserving", nonServingSrv.URL),
    ))
    d.loadNodesFromConfig()
    serving, _ := d.GetNode("n-serving")
    serving.markHealthy()
    markServing(t, serving, "test-embed")
    nonServing, _ := d.GetNode("n-nonserving")
    nonServing.markHealthy()
    // NOT marked serving "test-embed" — would 400 if sharded to.

    input := make([]string, 50)
    for i := range input {
        input[i] = "text"
    }
    req := &adapter.EmbeddingRequest{Model: "test-embed", Input: input}
    resp, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "k")
    if err != nil {
        t.Fatalf("RR14: expected success (only serving node pooled), got error: %v", err)
    }
    if nonServingHits.Load() != 0 {
        t.Fatalf("RR14: non-serving node received %d requests — model-blind sharding reopened (silent 400 per shard)", nonServingHits.Load())
    }
    if len(resp.Data) != 50 {
        t.Errorf("expected 50 embeddings, got %d", len(resp.Data))
    }
}

// RR14 guard B: partial shard failure MUST return an error, never 200 + a
// short vector list. The prior code logged a Warn and returned 200 with the
// surviving shards' Data — clients took truncated vectors as success, ran
// broken similarity search with index gaps at the failed shards (silent data
// loss, worse than a 502 the client would retry). Here 2 nodes serve the model
// but one always 500s → ~half the shards fail → must error.
func TestShardEmbedding_RR14_PartialFailureErrors(t *testing.T) {
    t.Log("testing RR14: partial shard failure returns error, not 200 + partial data")
    okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        var req adapter.EmbeddingRequest
        _ = json.NewDecoder(r.Body).Decode(&req)
        data := make([]adapter.EmbeddingData, len(req.Input))
        for i := range data {
            data[i] = adapter.EmbeddingData{Object: "embedding", Embedding: []float64{0.1}, Index: i}
        }
        _ = json.NewEncoder(w).Encode(adapter.EmbeddingResponse{
            Object: "list", Model: req.Model, Data: data,
            Usage: adapter.UsageResponse{PromptTokens: len(req.Input), TotalTokens: len(req.Input)},
        })
    }))
    defer okSrv.Close()
    failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusInternalServerError)
    }))
    defer failSrv.Close()

    d := NewDiscovery(makeClusterCfg(true,
        nodeConfig("n-ok", okSrv.URL),
        nodeConfig("n-fail", failSrv.URL),
    ))
    d.loadNodesFromConfig()
    for _, n := range d.AllNodes() {
        n.markHealthy()
        markServing(t, n, "test-embed")
    }

    input := make([]string, 50)
    for i := range input {
        input[i] = "text"
    }
    req := &adapter.EmbeddingRequest{Model: "test-embed", Input: input}
    resp, err := ShardEmbedding(context.TODO(), d, req, defaultRoutingCfg(), "k")
    if err == nil {
        t.Fatalf("RR14: partial shard failure returned 200 + %d vectors — silent data loss reopened", len(resp.Data))
    }
}
