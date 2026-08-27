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

    // RR14: filter healthy nodes by whether they serve req.Model. The prior
    // code took HealthyNodeList (healthy-only) and round-robined workers
    // model-blind — shards landed on nodes that never loaded the embedding
    // model → 400 per shard. Empty model stays model-agnostic (legacy).
    healthyNodes := discovery.HealthyNodeList()
    if req.Model != "" {
        serving := make([]*Node, 0, len(healthyNodes))
        for _, n := range healthyNodes {
            if n.servesModel(req.Model) {
                serving = append(serving, n)
            }
        }
        healthyNodes = serving
    }
    if len(healthyNodes) == 0 {
        return nil, fmt.Errorf("no healthy cluster nodes serving model %q for sharding", req.Model)
    }

    shardSize := defaultShardSize
    if inputLen <= shardSize || len(healthyNodes) < 2 {
        // RR14: single-node path must also be model-aware — SelectNodeByModel
        // skips nodes not serving req.Model. Empty model falls back to the
        // model-agnostic SelectNode inside SelectNodeByModel.
        node, err := discovery.SelectNodeByModel("least-connections", req.Model, 0)
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

    // RR14: partial failure is a data-plane error, not a 200. The prior code
    // returned 200 + a short vector list when some shards failed (M<N) —
    // clients took the truncated Data as success, ran broken similarity
    // search with index gaps at the failed shards. Now ANY shard failure is
    // an error: all-failed surfaces firstErr directly; partial surfaces a
    // structured error naming the failed shards so the client retries.
    if firstErr != nil {
        var failedShards []int
        for i, r := range results {
            if r.err != nil {
                failedShards = append(failedShards, i)
            }
        }
        if len(failedShards) == numShards {
            return nil, firstErr
        }
        slog.Error("shard embedding partial failure — returning error, not partial data",
            "shards", numShards,
            "failed_shards", failedShards,
            "first_error", firstErr,
        )
        return nil, fmt.Errorf("embedding sharding partial failure: %d/%d shards failed (first: %w)", len(failedShards), numShards, firstErr)
    }

    if len(allData) < inputLen {
        slog.Error("shard embedding result count mismatch — returning error",
            "expected", inputLen,
            "got", len(allData),
        )
        return nil, fmt.Errorf("embedding sharding data loss: expected %d vectors, got %d", inputLen, len(allData))
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
