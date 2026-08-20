package router

import (
    "container/list"
    "context"
    "crypto/sha256"
    "encoding/hex"
    "log/slog"
    "regexp"
    "strconv"
    "strings"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// codePattern is a compiled regex + weight scored by the heuristic classifier.
type codePattern struct {
    re     *regexp.Regexp
    weight float64
    name   string
}

// HeuristicClassifier is the in-process sub-ms intent classifier that replaces
// the sync LLM RouterLightClassifier on the code path. It scores a request by
// model name + text patterns + tools presence, caches the result by a sha256
// key, and returns IntentCode when the score meets MinConfidence. Safe for
// concurrent use.
type HeuristicClassifier struct {
    cfg      config.HeuristicClassifierConfig
    cache    *heuristicCache
    patterns []codePattern
}

// NewHeuristicClassifier compiles the default code patterns and prepares the
// cache (nil when CacheSize==0 or disabled). Returns a classifier that returns
// IntentUnknown when disabled.
func NewHeuristicClassifier(cfg config.HeuristicClassifierConfig) *HeuristicClassifier {
    patterns := defaultCodePatterns()
    var cache *heuristicCache
    if cfg.CacheSize > 0 {
        cache = newHeuristicCache(cfg.CacheSize, cfg.CacheTTL)
    }
    c := &HeuristicClassifier{cfg: cfg, cache: cache, patterns: patterns}
    slog.Info("heuristic classifier initialized",
        "enabled", cfg.Enabled,
        "code_adapter", cfg.CodeAdapter,
        "min_confidence", cfg.MinConfidence,
        "cache_size", cfg.CacheSize,
        "patterns", len(patterns),
    )
    return c
}

// defaultCodePatterns returns the compiled regex set the classifier scores
// against the request text. Weights are tuned so a single strong signal (e.g.
// a fenced code block) or several weak signals (keywords + file extension)
// together cross MinConfidence. No config-driven patterns (Rule 2 simplicity).
func defaultCodePatterns() []codePattern {
    defs := []struct {
        expr   string
        weight float64
        name   string
    }{
        {"(?s)```[a-zA-Z0-9]*\\n.*?```", 0.4, "fenced_code_block"},
        {"\\b(def|func|class|import|package|func\\(|fn |public |private |void |return )\\b", 0.2, "code_keyword"},
        {"\\.(go|py|rs|ts|js|java|cpp|c|rb|kt|swift|jsx|tsx)\\b", 0.2, "file_extension"},
        {"\\b(implement|refactor|debug|fix bug|compile|unit test|add a test|write a function|api endpoint)\\b", 0.3, "code_action_verb"},
        {"\\b(stack trace|traceback|null pointer|segfault|syntax error|undefined variable|runtime error)\\b", 0.3, "error_term"},
    }
    out := make([]codePattern, 0, len(defs))
    for _, d := range defs {
        re, err := regexp.Compile(d.expr)
        if err != nil {
            slog.Warn("heuristic: failed to compile pattern, skipping", "name", d.name, "expr", d.expr, "error", err)
            continue
        }
        out = append(out, codePattern{re: re, weight: d.weight, name: d.name})
    }
    return out
}

// Classify scores the request and returns IntentCode when the score meets
// MinConfidence, else IntentUnknown. Cached by sha256(model:toolsFlag:scanText).
func (c *HeuristicClassifier) Classify(ctx context.Context, req *RouteRequest) (*IntentResult, error) {
    if !c.cfg.Enabled {
        return &IntentResult{Intent: IntentUnknown, Confidence: 0}, nil
    }

    key := c.cacheKey(req)
    if c.cache != nil {
        if hit := c.cache.get(key); hit != nil {
            return hit, nil
        }
    }

    s := c.score(req)
    res := &IntentResult{
        Intent:     IntentUnknown,
        Confidence: s,
        Params:     map[string]string{"score": strconv.FormatFloat(s, 'f', 2, 64)},
    }
    if s >= c.cfg.MinConfidence {
        res.Intent = IntentCode
        if c.cfg.CodeAdapter != "" {
            res.Params["code_adapter"] = c.cfg.CodeAdapter
        }
    }

    if c.cache != nil {
        c.cache.set(key, res)
    }
    return res, nil
}

// score computes the code-intent score for a request. Signals: model name
// contains "code" (+0.5), each matching pattern adds its weight, non-nil tools
// (+0.2). The score is clamped to 1.0. Designed so a fenced block alone (0.4)
// plus tools (0.2) or a code model (0.5) crosses MinConfidence (default 0.6).
func (c *HeuristicClassifier) score(req *RouteRequest) float64 {
    var s float64
    if strings.Contains(strings.ToLower(req.Model), "code") {
        s += 0.5
    }
    scan := c.scanText(req)
    lower := strings.ToLower(scan)
    for _, p := range c.patterns {
        if p.re.MatchString(lower) {
            s += p.weight
            slog.Debug("heuristic pattern matched", "name", p.name, "weight", p.weight, "model", req.Model)
        }
    }
    if req.Tools != nil {
        s += 0.2
    }
    if s > 1.0 {
        s = 1.0
    }
    return s
}

// scanText returns the prefix of req.Text capped at TextScanBytes. Bounds work
// so a huge prompt doesn't make every pattern scan O(n) on each request.
func (c *HeuristicClassifier) scanText(req *RouteRequest) string {
    if req.Text == "" {
        return ""
    }
    limit := c.cfg.TextScanBytes
    if limit <= 0 {
        return req.Text
    }
    if len(req.Text) <= limit {
        return req.Text
    }
    return req.Text[:limit]
}

// cacheKey hashes model + toolsFlag + first scan-bytes of text into a stable
// sha256 hex key. Tools flag is "t"/"f" so a tools-bearing clone of the same
// prompt caches distinctly (tools add +0.2 to the score).
func (c *HeuristicClassifier) cacheKey(req *RouteRequest) string {
    toolsFlag := "f"
    if req.Tools != nil {
        toolsFlag = "t"
    }
    h := sha256.New()
    h.Write([]byte(req.Model))
    h.Write([]byte{':'})
    h.Write([]byte(toolsFlag))
    h.Write([]byte{':'})
    h.Write([]byte(c.scanText(req)))
    return hex.EncodeToString(h.Sum(nil))
}

// heuristicCache is a bounded LRU with TTL, single-consumer for the
// HeuristicClassifier (Rule 2 — no shared cache abstraction).
type heuristicCache struct {
    mu    sync.RWMutex
    ll    *list.List
    items map[string]*list.Element
    cap   int
    ttl   time.Duration
}

type cacheEntry struct {
    key       string
    result    *IntentResult
    expiresAt time.Time
}

func newHeuristicCache(cap int, ttl time.Duration) *heuristicCache {
    return &heuristicCache{
        ll:    list.New(),
        items: make(map[string]*list.Element, cap),
        cap:   cap,
        ttl:   ttl,
    }
}

// get returns a cached result if present and not expired, else nil. Stale
// entries are evicted on read (lazy expiry). LRU: hit moves entry to front.
func (h *heuristicCache) get(key string) *IntentResult {
    h.mu.Lock()
    defer h.mu.Unlock()
    el, ok := h.items[key]
    if !ok {
        return nil
    }
    entry := el.Value.(*cacheEntry)
    if h.ttl > 0 && time.Since(entry.expiresAt) >= 0 {
        h.removeElement(el)
        return nil
    }
    h.ll.MoveToFront(el)
    return entry.result
}

// set inserts/updates a result. When at capacity, evicts the least-recently
// used entry first.
func (h *heuristicCache) set(key string, r *IntentResult) {
    h.mu.Lock()
    defer h.mu.Unlock()
    if el, ok := h.items[key]; ok {
        entry := el.Value.(*cacheEntry)
        entry.result = r
        if h.ttl > 0 {
            entry.expiresAt = time.Now().Add(h.ttl)
        }
        h.ll.MoveToFront(el)
        return
    }
    entry := &cacheEntry{key: key, result: r}
    if h.ttl > 0 {
        entry.expiresAt = time.Now().Add(h.ttl)
    }
    h.items[key] = h.ll.PushFront(entry)
    for h.ll.Len() > h.cap {
        h.removeElement(h.ll.Back())
    }
}

func (h *heuristicCache) removeElement(el *list.Element) {
    h.ll.Remove(el)
    delete(h.items, el.Value.(*cacheEntry).key)
}
