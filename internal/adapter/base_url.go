package adapter

import (
    "log/slog"
    "regexp"
    "strings"
)

// versionedBaseRe matches a base_url that already ends in a version segment
// (/v1, /v3, /api/v3, /compatible-mode/v1, /api/paas/v4 ...): the last path
// component is exactly "v<digits>". Such a base_url is treated as the fully
// versioned prefix, so the adapter only appends the unversioned resource
// path (/chat/completions, /models, ...) instead of adding a second /v1.
var versionedBaseRe = regexp.MustCompile(`/v[0-9]+$`)

// normalizeVersionedBaseURL implements the #700 base_url contract:
// base_url is the vendor-documented versioned prefix, and the caller
// appends unversioned resource paths. A base_url that lacks a trailing
// version segment gets versionPath appended, which keeps every legacy
// configuration shape working:
//
//   https://api.deepseek.com/v1            -> unchanged (versioned)
//   https://api.deepseek.com               -> .../v1
//   https://api.deepseek.com/v1/           -> .../v1 (trailing slash trimmed)
//   https://open.bigmodel.cn               -> .../api/paas/v4 (zhipu)
//   https://open.bigmodel.cn/api/paas/v4   -> unchanged (versioned)
//
// An empty base_url returns "" and is rejected later by config validation.
func normalizeVersionedBaseURL(baseURL, versionPath string) string {
    trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
    if trimmed == "" {
        return ""
    }
    if versionedBaseRe.MatchString(trimmed) {
        return trimmed
    }
    out := trimmed + versionPath
    slog.Info("backend base_url normalized to versioned prefix", "base_url", baseURL, "normalized", out)
    return out
}
