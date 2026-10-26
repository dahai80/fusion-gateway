package config

import "fmt"

// FileLogConfig is the file_log.* namespace (G9): daily-rolled structured
// file logging in addition to stderr. The file is gateway-YYYY-MM-DD.log in
// dir; rotation happens on the first write past local midnight and files
// older than retain_days are pruned then. retain_days=0 keeps everything.
type FileLogConfig struct {
    Enabled    bool   `mapstructure:"enabled"`
    Dir        string `mapstructure:"dir"`
    RetainDays int    `mapstructure:"retain_days"`
    // Level is the file-side threshold (debug/info/warn/error). Empty
    // inherits the server.log_level value.
    Level string `mapstructure:"level"`
}

// defaultFileLogConfig seeds the values a minimal config.yaml omits.
func defaultFileLogConfig() FileLogConfig {
    return FileLogConfig{
        Enabled:    true,
        Dir:        "~/.fusion-gateway/logs",
        RetainDays: 14,
        Level:      "info",
    }
}

// validateFileLog enforces the numeric/enum ranges (EI8 pattern: a bad value
// fails Load loudly instead of producing a degenerate runtime).
func validateFileLog(cfg *Config) error {
    f := cfg.FileLog
    if !f.Enabled {
        return nil
    }
    if f.Dir == "" {
        return fmt.Errorf("file_log.enabled is true but dir is empty")
    }
    if f.RetainDays < 0 || f.RetainDays > 365 {
        return fmt.Errorf("file_log.retain_days must be 0-365, got %d", f.RetainDays)
    }
    switch f.Level {
    case "", "debug", "info", "warn", "error":
    default:
        return fmt.Errorf("file_log.level must be one of debug/info/warn/error, got %q", f.Level)
    }
    return nil
}
