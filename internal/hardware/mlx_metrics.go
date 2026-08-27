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

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
)

var mlxMetricsURL = "http://127.0.0.1:11434/metrics"

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

    resp, err := mlxHTTPDoFn(req)
    if err != nil {
        return fmt.Errorf("fetch mlx metrics: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return fmt.Errorf("mlx metrics returned status %d", resp.StatusCode)
    }

    // RR9 (audit P0): bound success-path read (10 MiB) — see LimitResponseReader.
    body, err := io.ReadAll(adapter.LimitResponseReader(resp.Body))
    if err != nil {
        return fmt.Errorf("read mlx metrics body: %w", err)
    }

    parsePrometheusMetrics(string(body), m)
    return nil
}

func parsePrometheusMetrics(data string, m *HardwareMetrics) {
    lines := strings.Split(data, "\n")
    for _, line := range lines {
        if strings.HasPrefix(line, "#") || line == "" {
            continue
        }

        // Parse fusion_mlx_model_memory_bytes
        if strings.HasPrefix(line, "fusion_mlx_model_memory_bytes") {
            if val, err := extractMetricValue(line); err == nil {
                m.MLXActiveMemory = val
            }
        }

        // Parse fusion_mlx_models_loaded
        if strings.HasPrefix(line, "fusion_mlx_models_loaded") {
            if val, err := extractMetricValue(line); err == nil {
                m.MLXModelsLoaded = int(val)
            }
        }

        // Parse fusion_mlx_requests_total
        if strings.HasPrefix(line, "fusion_mlx_requests_total") {
            if val, err := extractMetricValue(line); err == nil {
                m.MLXRequestsTotal = val
            }
        }

        // Parse inference queue depth
        if strings.HasPrefix(line, "fusion_mlx_inference_queue_depth") {
            if val, err := extractMetricValue(line); err == nil {
                m.MLXInferenceQueueDepth = int(val)
            }
        }
    }

    slog.Debug("mlx metrics parsed",
        "active_memory", m.MLXActiveMemory,
        "models_loaded", m.MLXModelsLoaded,
        "requests_total", m.MLXRequestsTotal,
        "queue_depth", m.MLXInferenceQueueDepth,
    )
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
