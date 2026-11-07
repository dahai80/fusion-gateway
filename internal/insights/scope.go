// Endpoint scoping for the dashboard aggregations (#564). Non-LLM routes
// (admin/proxy/catalog/shell-manifest) share the request-log middleware, so
// fg_request_logs carries model='' rows for them; unscoped aggregates
// over-count requests and under-report success rate. The dashboard scope is
// the same surface the try-quota gate meters (internal/middleware/tryquota.go
// exactGatePaths/prefixGatePaths) — keep the two lists in sync. Raw log
// search (Explorer) is NOT scoped: audit needs every row.
package insights

// llmEndpointFilter is the SQL AND-clause restricting an aggregate to product
// LLM call paths. Formatted as a constant (no user input) — safe to splice.
const llmEndpointFilter = ` AND (endpoint IN ('/v1/chat/completions', '/v1/completions', '/v1/embeddings', '/v1/messages', '/v1/rerank')` +
    ` OR endpoint LIKE '/v1/images/%' OR endpoint LIKE '/v1/audio/%')`

// scopeClause resolves the `scope` query parameter: explicit "all" → no
// filter (raw audit view), anything else → the LLM filter (dashboard
// default — dashboards must opt IN to raw).
func scopeClause(scope string) string {
    if scope == "all" {
        return ""
    }
    return llmEndpointFilter
}

// scopeLabel returns the resolved scope for the response payload.
func scopeLabel(scope string) string {
    if scope == "all" {
        return "all"
    }
    return "llm"
}
