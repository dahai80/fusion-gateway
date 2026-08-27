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
    // logs is a fixed-capacity ring buffer: len==cap==maxLen, pre-allocated
    // once in NewLogStore and NEVER grown. Append overwrites in place (no
    // reslice, no append) so the backing array cap stays == maxLen forever —
    // burst traffic cannot permanently inflate resident memory (EI11: the
    // prior `s.logs = s.logs[1:]` + append resliced but never shrank the
    // backing array, then realloc grew cap, leaving evicted RequestLog
    // pointers retained until the next realloc).
    logs    []*store.RequestLog
    head    int // index of the oldest live entry (0 until the ring wraps)
    count   int // number of live entries (<= maxLen)
    maxLen  int
    counter int64
}

func NewLogStore(maxLen int) *LogStore {
    if maxLen <= 0 {
        maxLen = 10000
    }
    return &LogStore{
        logs:   make([]*store.RequestLog, maxLen, maxLen),
        maxLen: maxLen,
    }
}

// ordered returns the live entries oldest→newest, unwrapping the ring. Callers
// hold (at least) RLock. Allocates a slice of len(count) per read — acceptable
// because log reads are admin-only and infrequent; the hot write path (Append)
// stays zero-alloc.
func (s *LogStore) ordered() []*store.RequestLog {
    out := make([]*store.RequestLog, 0, s.count)
    for i := 0; i < s.count; i++ {
        out = append(out, s.logs[(s.head+i)%s.maxLen])
    }
    return out
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

    // Ring write: overwrite the oldest slot and advance head once full, else
    // fill the next free slot. No append, no reslice → cap never grows (EI11).
    if s.count < s.maxLen {
        s.logs[s.count] = log
        s.count++
    } else {
        s.logs[s.head] = log
        s.head = (s.head + 1) % s.maxLen
    }

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
    for _, l := range s.ordered() {
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

    for _, l := range s.ordered() {
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
    for _, l := range s.ordered() {
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
    return s.ordered()
}

func (s *LogStore) DistinctFilters() *store.LogFilters {
    s.mu.RLock()
    defer s.mu.RUnlock()

    models := make(map[string]struct{})
    channels := make(map[string]struct{})
    for _, l := range s.ordered() {
        if l.Model != "" {
            models[l.Model] = struct{}{}
        }
        if l.ChannelName != "" {
            channels[l.ChannelName] = struct{}{}
        }
    }

    out := &store.LogFilters{
        Models:   make([]string, 0, len(models)),
        Channels: make([]string, 0, len(channels)),
    }
    for m := range models {
        out.Models = append(out.Models, m)
    }
    for c := range channels {
        out.Channels = append(out.Channels, c)
    }
    sort.Strings(out.Models)
    sort.Strings(out.Channels)
    return out
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

// csvSanitize prevents CSV formula injection by prefixing a single-quote
// when a user-controlled field starts with =, +, -, or @.
func csvSanitize(s string) string {
    if len(s) == 0 {
        return s
    }
    switch s[0] {
    case '=', '+', '-', '@':
        return "'" + s
    }
    return s
}

func exportCSV(logs []*store.RequestLog) ([]byte, error) {
    var buf []byte
    writer := csv.NewWriter(&byteWriter{buf: &buf})

    header := []string{"id", "timestamp", "key_name", "model", "request_type", "channel", "route_reason",
        "input_tokens", "output_tokens", "total_tokens", "cost", "latency_ms", "status_code", "is_success", "error"}
    _ = writer.Write(header)

    for _, l := range logs {
        row := []string{
            csvSanitize(l.ID),
            l.Timestamp.Format(time.RFC3339),
            csvSanitize(l.APIKeyName),
            csvSanitize(l.Model),
            csvSanitize(l.RequestType),
            csvSanitize(l.ChannelName),
            csvSanitize(l.RouteReason),
            fmt.Sprintf("%d", l.InputTokens),
            fmt.Sprintf("%d", l.OutputTokens),
            fmt.Sprintf("%d", l.TotalTokens),
            fmt.Sprintf("%.6f", l.Cost),
            fmt.Sprintf("%.2f", l.Latency),
            fmt.Sprintf("%d", l.StatusCode),
            fmt.Sprintf("%v", l.IsSuccess),
            csvSanitize(l.ErrorMessage),
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
