package cluster

// H4 guard tests: the 3 cluster-path http.Clients must route through
// adapter.TransportForBackend so they inherit the MaxConnsPerHost FD cap
// (default 16). A bare &http.Client{} inherits http.DefaultTransport
// (MaxConnsPerHost=0 = unlimited) — the FD-exhaustion vector H4 closes. Revert
// any Transport: TransportForBackend(...) → its MaxConnsPerHost assertion fails.

import (
    "net/http"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// assertCappedTransport fails if the client's transport is not an
// *http.Transport with a non-zero MaxConnsPerHost (the RR11/H4 FD cap).
func assertCappedTransport(t *testing.T, name string, c *http.Client) {
    t.Helper()
    if c == nil {
        t.Fatalf("H4: %s: nil http.Client", name)
    }
    tpt, ok := c.Transport.(*http.Transport)
    if !ok {
        t.Fatalf("H4: %s: Transport not *http.Transport (got %T) — bare client slipped through, no FD cap", name, c.Transport)
    }
    if tpt.MaxConnsPerHost <= 0 {
        t.Fatalf("H4: %s: MaxConnsPerHost=%d — FD cap missing (0/unlimited is the H4 bug)", name, tpt.MaxConnsPerHost)
    }
}

// TestH4_ClusterNodeProvider_CappedTransport: the inference-path client (Chat/
// StreamChat/Embedding/Rerank all use p.httpClient) must be FD-capped.
func TestH4_ClusterNodeProvider_CappedTransport(t *testing.T) {
    node := &Node{ID: "n1", Address: "http://127.0.0.1:11434"}
    p := NewClusterNodeProvider(node, config.RoutingConfig{}, "tok")
    assertCappedTransport(t, "cluster_node_provider", p.httpClient)
}

// TestH4_MasterClient_CappedTransport: the master sync/poll client must be
// FD-capped.
func TestH4_MasterClient_CappedTransport(t *testing.T) {
    mc := NewMasterClient(config.ClusterMasterConfig{Address: "http://127.0.0.1:11435"})
    assertCappedTransport(t, "master_client", mc.client)
}

// TestH4_Discovery_CappedTransport: the discovery polling client fans out to
// every node on a fixed tick; it must be FD-capped. NewDiscovery does not launch
// goroutines (only Start does), so calling it here is leak-free.
func TestH4_Discovery_CappedTransport(t *testing.T) {
    d := NewDiscovery(config.ClusterConfig{})
    assertCappedTransport(t, "discovery", d.client)
}
