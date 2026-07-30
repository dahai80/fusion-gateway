package cluster

import (
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
    _, err := ShardEmbedding(nil, d, req, defaultRoutingCfg())
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
    _, err := ShardEmbedding(nil, d, req, defaultRoutingCfg())
    if err == nil {
        t.Fatal("expected error with empty input")
    }
}
