# Fusion-Gateway Observability

Production monitoring stack for Fusion-Gateway. Closes the metrics → alerting
→ triage loop that the enterprise production-readiness audit flagged as a gap:
the gateway already exports Prometheus metrics on `/metrics`, but had no
shipped dashboard, alert rules, or alert-to-runbook mapping.

## Artifacts

| File | Purpose |
|------|---------|
| `alerts.yaml` | Prometheus alerting rule groups (availability, capacity, latency). Load via `promtool` or a `PrometheusRule` CRD. |
| `grafana-dashboard.json` | Grafana dashboard: request rate, latency p50/p99, success rate, circuit breaker, in-flight, memory/GPU, swap, routing reasons. Import via the Grafana UI or provisioning. |

## Metric source

All metrics are prefixed `fusion_gateway_` and defined in
`internal/observability/metrics.go`. The `/metrics` endpoint is registered in
`internal/server/server.go` behind `withMasterKey` — a scrape config must
authenticate, OR the deployment must enable unauthenticated metrics access
behind a network policy.

### Metric inventory

| Metric | Type | Labels | Meaning |
|--------|------|--------|---------|
| `fusion_gateway_requests_total` | counter | backend, model, status | Total requests |
| `fusion_gateway_request_duration_seconds` | histogram | backend, model | Request latency |
| `fusion_gateway_request_tokens_total` | counter | direction, backend | Tokens processed |
| `fusion_gateway_route_decisions_total` | counter | backend, reason | Routing decision counts |
| `fusion_gateway_circuit_breaker_state` | gauge | backend | 0=closed, 1=open, 2=half_open |
| `fusion_gateway_circuit_breaker_trips_total` | counter | backend, reason | Breaker trip counts |
| `fusion_gateway_hw_memory_used_ratio` | gauge | — | System memory ratio |
| `fusion_gateway_hw_swap_used_bytes` | gauge | — | Swap used |
| `fusion_gateway_hw_swap_page_in_rate` | gauge | — | Swap page-in/s |
| `fusion_gateway_hw_swap_page_out_rate` | gauge | — | Swap page-out/s |
| `fusion_gateway_hw_gpu_device_utilization` | gauge | — | GPU device util ratio |
| `fusion_gateway_hw_mlx_active_memory_bytes` | gauge | — | MLX active memory |
| `fusion_gateway_hw_mlx_models_loaded` | gauge | — | MLX loaded model count |
| `fusion_gateway_hw_mlx_inference_queue_depth` | gauge | — | MLX inference queue |
| `fusion_gateway_hw_collection_errors_total` | counter | source | Collector errors |
| `fusion_gateway_config_version` | gauge | — | Config version |
| `fusion_gateway_in_flight_requests` | gauge | backend | In-flight count |

## Prometheus scrape config

```yaml
scrape_configs:
  - job_name: fusion-gateway
    scrape_interval: 15s
    metrics_path: /metrics
    # /metrics is master-key gated. Pass the key as a bearer header, or
    # enable unauthenticated metrics behind a network policy.
    bearer_token: <master-key>
    static_configs:
      - targets: ["<gateway-host>:11432"]
```

## Alert severity model

- **critical** — a user-facing path is failing now: backend 5xx, circuit
  breaker open, swap thrashing. Page on-call.
- **warning** — a precursor / capacity signal: memory climbing, MLX queue
  rising, hardware collection errors, p99 regression. Intervene before it
  becomes critical.

Every alert carries a `runbook` annotation linking to a triage section in
[`docs/runbook-recovery.md`](../../docs/runbook-recovery.md#alert-triage).

## Deploying the dashboard

1. Grafana UI → Dashboards → Import → upload `grafana-dashboard.json`.
2. Select the Prometheus datasource (the dashboard templatizes it as
   `${DS_PROMETHEUS}`).
3. Tag `fusion-gateway` / `production`; refresh 30s; default window 1h.

## Validating the alerts

```bash
# If promtool is installed (ships with Prometheus):
promtool check rules deploy/observability/alerts.yaml
```
