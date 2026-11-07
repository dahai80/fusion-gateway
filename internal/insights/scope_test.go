package insights

import (
    "strings"
    "testing"
)

// TestLLMEndpointFilter_Paths pins the LLM endpoint scope (#564): the filter
// must cover exactly the try-quota gate surface (exact paths + image/audio
// prefixes) so non-LLM traffic (admin/proxy/catalog, model='') never lands
// in dashboard aggregates.
func TestLLMEndpointFilter_Paths(t *testing.T) {
    for _, exact := range []string{
        "/v1/chat/completions", "/v1/completions", "/v1/embeddings",
        "/v1/messages", "/v1/rerank",
    } {
        if !strings.Contains(llmEndpointFilter, "'"+exact+"'") {
            t.Fatalf("llmEndpointFilter missing exact path %s", exact)
        }
    }
    for _, prefix := range []string{"/v1/images/", "/v1/audio/"} {
        if !strings.Contains(llmEndpointFilter, "'"+prefix+"%'") {
            t.Fatalf("llmEndpointFilter missing prefix %s%%", prefix)
        }
    }
    if !strings.Contains(llmEndpointFilter, "endpoint") {
        t.Fatal("filter must reference the endpoint column")
    }
}

// TestScopeFilter_AllOpts pins scope resolution: only explicit "all"
// passthrough (no clause), anything else (including omitted) falls back to
// the LLM filter. Default = llm — dashboards must opt IN to raw.
func TestScopeFilter_AllOpts(t *testing.T) {
    if got := scopeClause("all"); got != "" {
        t.Fatalf("scope=all must not filter, got %q", got)
    }
    if got := scopeClause("llm"); got != llmEndpointFilter {
        t.Fatalf("scope=llm must use the LLM filter, got %q", got)
    }
    if got := scopeClause("garbage"); got != llmEndpointFilter {
        t.Fatalf("unknown scope must fall back to llm, got %q", got)
    }
}
