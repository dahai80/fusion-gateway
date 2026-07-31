package memory

import (
    "encoding/csv"
    "encoding/json"
    "fmt"
    "log/slog"
    "sort"
    "sync"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type LogStore struct {
    mu      sync.RWMutex
    logs    []*store.RequestLog
    maxLen  int
    counter int64
}

func NewLogStore(maxLen int) *LogStore {
    if maxLen <= 0 {
        maxLen = 10000
    }
    return &LogStore{
        logs:   make([]*store.RequestLog, 0, maxLen),
        maxLen: maxLen,
    }
}

func (s *LogStore) Append(log *store.RequestLog) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    s.counter++
    if log.ID == "" {
        log.ID = fmt.Sprintf("log-%d", s.counter)
    }
    if log.Timestamp.IsZero() {
        log.Timestamp = time.Now()
    }

    if len(s.logs) >= s.maxLen {
        s.logs = s.logs[1:]
    }
    s.logs = append(s.logs, log)

    slog.Debug("request log appended",
        "id", log.ID,
        "model", log.Model,
        "channel", log.ChannelName,
        "latency_ms", log.Latency,
    )
    return nil
}

func (s *LogStore) Query(filter store.LogFilter) ([]*store.RequestLog, int, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    filtered := make([]*store.RequestLog, 0)
    for _, l := range s.logs {
        if !matchFilter(l, filter) {
            continue
        }
        filtered = append(filtered, l)
    }

    total := len(filtered)
    sort.Slice(filtered, func(i, j int) bool {
        return filtered[i].Timestamp.After(filtered[j].Timestamp)
    })

    page := filter.Page
    if page < 1 {
        page = 1
    }
    pageSize := filter.PageSize
    if pageSize < 1 {
        pageSize = 20
    }
    if pageSize > 100 {
        pageSize = 100
    }

    start := (page - 1) * pageSize
    if start >= total {
        return []*store.RequestLog{}, total, nil
    }
    end := start + pageSize
    if end > total {
        end = total
    }

    return filtered[start:end], total, nil
}

func (s *LogStore) Get(id string) (*store.RequestLog, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    for _, l := range s.logs {
        if l.ID == id {
            return l, nil
        }
    }
    return nil, fmt.Errorf("log not found: %s", id)
}

func (s *LogStore) Export(filter store.LogFilter, format string) ([]byte, error) {
    s.mu.RLock()
    defer s.mu.RUnlock()

    filtered := make([]*store.RequestLog, 0)
    for _, l := range s.logs {
        if !matchFilter(l, filter) {
            continue
        }
        filtered = append(filtered, l)
    }

    switch format {
    case "csv":
        return exportCSV(filtered)
    case "json":
        return json.Marshal(filtered)
    default:
        return json.Marshal(filtered)
    }
}

func (s *LogStore) AllLogs() []*store.RequestLog {
    s.mu.RLock()
    defer s.mu.RUnlock()
    result := make([]*store.RequestLog, len(s.logs))
    copy(result, s.logs)
    return result
}

func matchFilter(l *store.RequestLog, f store.LogFilter) bool {
    if f.StartTime != nil && l.Timestamp.Before(*f.StartTime) {
        return false
    }
    if f.EndTime != nil && l.Timestamp.After(*f.EndTime) {
        return false
    }
    if f.KeyName != "" && l.APIKeyName != f.KeyName {
        return false
    }
    if f.Model != "" && l.Model != f.Model {
        return false
    }
    if f.Channel != "" && l.ChannelName != f.Channel {
        return false
    }
    if f.Status == "success" && !l.IsSuccess {
        return false
    }
    if f.Status == "error" && l.IsSuccess {
        return false
    }
    if f.MinTokens > 0 && l.TotalTokens < f.MinTokens {
        return false
    }
    if f.MinCost > 0 && l.Cost < f.MinCost {
        return false
    }
    return true
}

func exportCSV(logs []*store.RequestLog) ([]byte, error) {
    var buf []byte
    writer := csv.NewWriter(&byteWriter{buf: &buf})

    header := []string{"id", "timestamp", "key_name", "model", "request_type", "channel", "route_reason",
        "input_tokens", "output_tokens", "total_tokens", "cost", "latency_ms", "status_code", "is_success", "error"}
    _ = writer.Write(header)

    for _, l := range logs {
        row := []string{
            l.ID,
            l.Timestamp.Format(time.RFC3339),
            l.APIKeyName,
            l.Model,
            l.RequestType,
            l.ChannelName,
            l.RouteReason,
            fmt.Sprintf("%d", l.InputTokens),
            fmt.Sprintf("%d", l.OutputTokens),
            fmt.Sprintf("%d", l.TotalTokens),
            fmt.Sprintf("%.6f", l.Cost),
            fmt.Sprintf("%.2f", l.Latency),
            fmt.Sprintf("%d", l.StatusCode),
            fmt.Sprintf("%v", l.IsSuccess),
            l.ErrorMessage,
        }
        _ = writer.Write(row)
    }
    writer.Flush()
    return buf, nil
}

type byteWriter struct {
    buf *[]byte
}

func (w *byteWriter) Write(p []byte) (n int, err error) {
    *w.buf = append(*w.buf, p...)
    return len(p), nil
}
