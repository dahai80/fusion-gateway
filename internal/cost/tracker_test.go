package cost

import (
    "encoding/json"
    "math"
    "os"
    "path/filepath"
    "testing"
)

func TestRecord_AddsEntry(t *testing.T) {
    t.Parallel()
    tr := NewTracker(100)
    tr.Record("key1", "cloud", "gpt-4", 1000, 500)

    tr.mu.RLock()
    defer tr.mu.RUnlock()

    if len(tr.records) != 1 {
        t.Fatalf("expected 1 record, got %d", len(tr.records))
    }
    rec := tr.records[0]
    if rec.KeyName != "key1" {
        t.Errorf("expected key1, got %s", rec.KeyName)
    }
    if rec.Backend != "cloud" {
        t.Errorf("expected cloud, got %s", rec.Backend)
    }
    if rec.Model != "gpt-4" {
        t.Errorf("expected gpt-4, got %s", rec.Model)
    }
    if rec.TotalTokens != 1500 {
        t.Errorf("expected 1500 total tokens, got %d", rec.TotalTokens)
    }
    expectedCost := 1000.0/1_000_000*30 + 500.0/1_000_000*60
    if math.Abs(rec.CostUSD-expectedCost) > 1e-9 {
        t.Errorf("expected cost %.10f, got %.10f", expectedCost, rec.CostUSD)
    }
    if math.Abs(tr.totalCost-expectedCost) > 1e-9 {
        t.Errorf("expected totalCost %.10f, got %.10f", expectedCost, tr.totalCost)
    }
}

func TestSummary_Aggregates(t *testing.T) {
    t.Parallel()
    tr := NewTracker(100)
    tr.Record("key1", "cloud", "gpt-4", 1000, 0)
    tr.Record("key2", "local", "gpt-4o-mini", 0, 1000)
    tr.Record("key1", "cloud", "gpt-4", 500, 0)

    s := tr.Summary()

    if s.TotalRequests != 3 {
        t.Errorf("expected 3 requests, got %d", s.TotalRequests)
    }
    if s.TotalTokens != 2500 {
        t.Errorf("expected 2500 total tokens, got %d", s.TotalTokens)
    }

    key1Cost := 1000.0/1_000_000*30 + 500.0/1_000_000*30
    key2Cost := 1000.0/1_000_000*0.60
    totalExpected := key1Cost + key2Cost

    if math.Abs(s.TotalCostUSD-totalExpected) > 1e-9 {
        t.Errorf("expected total cost %.10f, got %.10f", totalExpected, s.TotalCostUSD)
    }

    if math.Abs(s.ByKey["key1"]-key1Cost) > 1e-9 {
        t.Errorf("expected key1 cost %.10f, got %.10f", key1Cost, s.ByKey["key1"])
    }
    if math.Abs(s.ByBackend["cloud"]-key1Cost) > 1e-9 {
        t.Errorf("expected cloud cost %.10f, got %.10f", key1Cost, s.ByBackend["cloud"])
    }
}

func TestSummaryByKey_Filters(t *testing.T) {
    t.Parallel()
    tr := NewTracker(100)
    tr.Record("key1", "cloud", "gpt-4", 1000, 0)
    tr.Record("key2", "local", "gpt-4o-mini", 0, 1000)
    tr.Record("key1", "local", "gpt-4o-mini", 500, 500)

    s := tr.SummaryByKey("key1")

    if s.TotalRequests != 2 {
        t.Errorf("expected 2 requests for key1, got %d", s.TotalRequests)
    }
    if s.TotalTokens != 2000 {
        t.Errorf("expected 2000 total tokens for key1, got %d", s.TotalTokens)
    }

    // key1 record 1: gpt-4 1000 prompt → 1000/1M*30 = 0.03
    // key1 record 2: gpt-4o-mini 500 prompt + 500 completion → 500/1M*0.15 + 500/1M*0.60 = 0.000375
    key1Cost := 0.03 + 0.000375
    if math.Abs(s.TotalCostUSD-key1Cost) > 1e-9 {
        t.Errorf("expected key1 total cost %.10f, got %.10f", key1Cost, s.TotalCostUSD)
    }

    if _, ok := s.ByKey["key2"]; ok {
        t.Error("expected no key2 in ByKey for key1 summary")
    }
}

func TestMaxRecords_Eviction(t *testing.T) {
    tr := NewTracker(2)
    tr.Record("k1", "b1", "gpt-4", 100, 0)
    tr.Record("k2", "b2", "gpt-4", 200, 0)
    tr.Record("k3", "b3", "gpt-4", 300, 0)

    tr.mu.RLock()
    defer tr.mu.RUnlock()

    if len(tr.records) != 2 {
        t.Fatalf("expected 2 records after eviction, got %d", len(tr.records))
    }
    if tr.records[0].KeyName != "k2" {
        t.Errorf("expected first record key k2, got %s", tr.records[0].KeyName)
    }
    if tr.records[1].KeyName != "k3" {
        t.Errorf("expected second record key k3, got %s", tr.records[1].KeyName)
    }
}

func TestNewTracker_DefaultMaxRecords(t *testing.T) {
    t.Parallel()
    tr := NewTracker(0)
    if tr.maxRecords != 10000 {
        t.Errorf("expected default maxRecords 10000, got %d", tr.maxRecords)
    }
    tr2 := NewTracker(-5)
    if tr2.maxRecords != 10000 {
        t.Errorf("expected default maxRecords 10000 for negative input, got %d", tr2.maxRecords)
    }
}

func TestExportJSON_WritesValidFile(t *testing.T) {
    tr := NewTracker(100)
    tr.Record("key1", "cloud", "gpt-4", 1000, 500)
    tr.Record("key2", "local", "gpt-4o-mini", 200, 100)

    dir := t.TempDir()
    path := filepath.Join(dir, "cost.json")

    if err := tr.ExportJSON(path); err != nil {
        t.Fatalf("ExportJSON failed: %v", err)
    }

    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("failed to read exported file: %v", err)
    }

    var records []UsageRecord
    if err := json.Unmarshal(data, &records); err != nil {
        t.Fatalf("failed to unmarshal JSON: %v", err)
    }
    if len(records) != 2 {
        t.Errorf("expected 2 records in JSON, got %d", len(records))
    }
    if records[0].KeyName != "key1" {
        t.Errorf("expected first record key key1, got %s", records[0].KeyName)
    }
    if records[1].Model != "gpt-4o-mini" {
        t.Errorf("expected second record model gpt-4o-mini, got %s", records[1].Model)
    }
}
