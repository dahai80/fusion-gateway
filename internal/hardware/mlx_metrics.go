package hardware

import (
    "context"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "strconv"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/httpx"
)

var mlxMetricsURL = "http://127.0.0.1:11434/metrics"

// mlxMetricsAPIKey is the optional Bearer token sent on /metrics fetches.
// Empty means no Authorization header (older fusion-mlx without auth).
var mlxMetricsAPIKey = ""

var mlxHTTPDoFn func(req *http.Request) (*http.Response, error)

func init() {
    mlxHTTPDoFn = defaultMLXHTTPDo
}

func defaultMLXHTTPDo(req *http.Request) (*http.Response, error) {
    client := &http.Client{Timeout: 2 * time.Second}
    return client.Do(req)
}

// collectMLXMetrics fetches Prometheus metrics from fusion-mlx /metrics endpoint
func collectMLXMetrics(m *HardwareMetrics) error {
    url := mlxMetricsURL

    req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
    if err != nil {
        return fmt.Errorf("create mlx metrics request: %w", err)
    }

    if mlxMetricsAPIKey != "" {
        req.Header.Set("Authorization", "Bearer "+mlxMetricsAPIKey)
    }

    resp, err := mlxHTTPDoFn(req)
    if err != nil {
        return fmt.Errorf("fetch mlx metrics: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("mlx metrics returned status %d", resp.StatusCode)
    }

    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    body, err := io.ReadAll(httpx.LimitResponseReader(resp.Body))
    if err != nil {
        return fmt.Errorf("read mlx metrics body: %w", err)
    }

    parsePrometheusMetrics(string(body), m)
    return nil
}

func parsePrometheusMetrics(data string, m *HardwareMetrics) {
    lines := strings.Split(data, "\n")
    // C9 (audit): the old HasPrefix match was last-wins per line group — with
    // multiple series (per-model memory, per-worker queue depth) the final
    // line clobbered the earlier ones, and a longer metric name sharing the
    // prefix was misattributed. Now: exact metric-name boundary (next char is
    // '{' or whitespace) and SUM across series for these additive gauges.
    for _, line := range lines {
        if line == "" || strings.HasPrefix(line, "#") {
            continue
        }
        idx := strings.IndexAny(line, "{ ")
        if idx <= 0 {
            continue
        }
        name := line[:idx]
        // extractMetricValue takes the full line (it reads the last
        // whitespace-separated token, which is the value).
        val, err := extractMetricValue(line)
        if err != nil {
            continue
        }
        switch name {
        case "fusion_mlx_model_memory_bytes":
            m.MLXActiveMemory += val
        case "fusion_mlx_models_loaded":
            m.MLXModelsLoaded += int(val)
        case "fusion_mlx_requests_total":
            m.MLXRequestsTotal += val
        case "fusion_mlx_inference_queue_depth":
            m.MLXInferenceQueueDepth += int(val)
        }
    }

    slog.Debug("mlx metrics parsed",
        "active_memory", m.MLXActiveMemory,
        "models_loaded", m.MLXModelsLoaded,
        "requests_total", m.MLXRequestsTotal,
        "queue_depth", m.MLXInferenceQueueDepth)
}
func extractMetricValue(line string) (uint64, error) {
    parts := strings.Fields(line)
    if len(parts) < 2 {
        return 0, fmt.Errorf("invalid metric line: %s", line)
    }

    valStr := parts[len(parts)-1]
    // Handle float values
    if strings.Contains(valStr, ".") {
        f, err := strconv.ParseFloat(valStr, 64)
        if err != nil {
            return 0, err
        }
        return uint64(f), nil
    }

    return strconv.ParseUint(valStr, 10, 64)
}
