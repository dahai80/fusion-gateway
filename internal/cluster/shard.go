package cluster

// ShardScheduler — split batch requests across cluster nodes, merge results
// Called from: internal/server/server.go (handleEmbeddings batch sharding)
// API: ShardEmbedding() — split input, dispatch to nodes, merge responses
// User instruction: "#26" — Task #26 batch task sharding

import (
    "context"
    "fmt"
    "log/slog"
    "sync"
    "sync/atomic"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
)

const defaultShardSize = 32

type shardResult struct {
    data   []adapter.EmbeddingData
    tokens int
    err    error
}

func ShardEmbedding(ctx context.Context, discovery *Discovery, req *adapter.EmbeddingRequest, routingCfg config.RoutingConfig, sharedToken string) (*adapter.EmbeddingResponse, error) {
    inputLen := len(req.Input)
    if inputLen == 0 {
        return nil, fmt.Errorf("empty input")
    }

    healthyNodes := discovery.HealthyNodeList()
    if len(healthyNodes) == 0 {
        return nil, fmt.Errorf("no healthy cluster nodes for sharding")
    }

    shardSize := defaultShardSize
    if inputLen <= shardSize || len(healthyNodes) < 2 {
        node, err := discovery.SelectNode("least-connections")
        if err != nil {
            return nil, err
        }
        provider := NewClusterNodeProvider(node, routingCfg, sharedToken)
        return provider.Embedding(ctx, req)
    }

    shards := splitInput(req.Input, shardSize)
    numShards := len(shards)
    numWorkers := minInt(numShards, len(healthyNodes))

    slog.Info("batch embedding sharding",
        "total_input", inputLen,
        "shard_size", shardSize,
        "shards", numShards,
        "workers", numWorkers,
    )

    results := make([]shardResult, numShards)
    var wg sync.WaitGroup

    shardIdx := atomic.Int64{}
    for w := 0; w < numWorkers; w++ {
        wg.Add(1)
        wid := w
        safego.Go("shard_worker", func() {
            defer wg.Done()
            for {
                idx := int(shardIdx.Add(1) - 1)
                if idx >= numShards {
                    return
                }

                node := healthyNodes[wid%len(healthyNodes)]
                provider := NewClusterNodeProvider(node, routingCfg, sharedToken)

                shardReq := &adapter.EmbeddingRequest{
                    Model: req.Model,
                    Input: shards[idx],
                }

                resp, err := provider.Embedding(ctx, shardReq)
                if err != nil {
                    results[idx] = shardResult{err: fmt.Errorf("shard %d on node %s: %w", idx, node.ID, err)}
                    slog.Warn("shard embedding failed",
                        "shard", idx,
                        "node", node.ID,
                        "error", err,
                    )
                    return
                }

                results[idx] = shardResult{
                    data:   resp.Data,
                    tokens: resp.Usage.PromptTokens,
                }
            }
        })
    }

    wg.Wait()

    var totalTokens int
    var allData []adapter.EmbeddingData
    var firstErr error

    for i, r := range results {
        if r.err != nil {
            if firstErr == nil {
                firstErr = r.err
            }
            continue
        }
        for j := range r.data {
            r.data[j].Index = i*shardSize + j
            if r.data[j].Index >= inputLen {
                r.data[j].Index = inputLen - 1
            }
        }
        allData = append(allData, r.data...)
        totalTokens += r.tokens
    }

    if firstErr != nil && len(allData) == 0 {
        return nil, firstErr
    }

    if len(allData) < inputLen {
        slog.Warn("partial embedding results",
            "expected", inputLen,
            "got", len(allData),
        )
    }

    return &adapter.EmbeddingResponse{
        Object: "list",
        Data:   allData,
        Model:  req.Model,
        Usage: adapter.UsageResponse{
            PromptTokens: totalTokens,
            TotalTokens:  totalTokens,
        },
    }, nil
}

func splitInput(input []string, shardSize int) [][]string {
    var shards [][]string
    for i := 0; i < len(input); i += shardSize {
        end := i + shardSize
        if end > len(input) {
            end = len(input)
        }
        shards = append(shards, input[i:end])
    }
    return shards
}

func minInt(a, b int) int {
    if a < b {
        return a
    }
    return b
}
