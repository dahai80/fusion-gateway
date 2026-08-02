# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Fusion-Gateway is a unified hybrid inference gateway for Apple Silicon local inference + cloud LLMs. It's the core traffic entry point for Fusion-Agent Studio, Fusion-MLX, and Fusion-Coder. Written in **Go** (not Python — to avoid competing with fusion-mlx for UMA memory).

**Four design principles:**
- **Non-invasive**: Pure routing/forwarding, no inference computation, never modifies fusion-mlx or any engine internals
- **Local-first**: Short/low-overhead/privacy requests go local; long/complex requests go cloud
- **Hardware-aware**: Routes based on macOS memory/GPU/Swap/inference load — not just request-level rules
- **Protocol-unified**: Full OpenAI v1 compatibility (chat/completions, completions, embeddings)

## Architecture

```
Clients (VSCode/UI/CLI/Agent)
        ↓
Fusion-Gateway :11432
├─ Ingress Layer       — Auth, parsing, standardization, rate limiting
├─ Preprocessing       — Tokenizer counting, prompt validation, param defaults
├─ Routing Engine      — Rule engine + hardware load sensing (core differentiator)
├─ Adapter Pool        — Unified interface for all inference backends
├─ Stream Forwarding   — SSE, cancel, retry, KV cache release
└─ Observability       — Logs, metrics, hot config reload
        ↓
Heterogeneous Inference Pool
├─ Local: fusion-mlx (:11434) / llama.cpp
├─ Private: vLLM-ascend / vLLM-cuda
└─ Cloud: Volcengine / Qianfan / Claude / OpenAI
```

### Core Routing Logic

Dual-dimension decision: **request dimension + hardware load dimension**

Priority (high to low):
1. **Circuit breaker**: Local model offline / memory overload / swap triggered → force cloud
2. **Token budget**: Total tokens > 50 → force cloud
3. **Local-first**: Total tokens ≤ 50 & load healthy → route to fusion-mlx

Token counting is server-side precise (Qwen-Tokenizer WASM), not client-estimated.

### Adapter Interface

All backends implement unified interface: `Chat() / StreamChat() / Embedding()`

## Tech Stack

- **Language**: Go (mandatory — no Python, to avoid UMA memory competition with fusion-mlx)
- **Base framework**: ENTERPILOT/GoModel (OpenAI v1 protocol, SSE streaming, multi-provider adapters, auth, rate limiting, observability, MCP gateway)
- **Tokenizer**: donge/go-tokenizer or fusion-mlx `/v1/count_tokens` (server-side precise token counting)
- **Hardware metrics**: gopsutil (macOS UMA memory, Swap, GPU) + fusion-mlx `/metrics`
- **Config**: Viper + fsnotify (YAML hot reload)
- **Circuit breaker**: sony/gobreaker (Go standard circuit breaker, 3665★)
- **Structured logging**: slog (Go 1.21+ stdlib)

~80% reuse from open source, ~20% self-developed (token budget engine, hardware metrics collector, routing decision engine).

## Build & Run

```bash
# Build
go build -o fusion-gateway ./cmd/gateway

# Run
./fusion-gateway --config config.yaml

# Test
go test ./... -v
go test ./internal/router/... -v          # single package
go test -run TestTokenBudget -v            # single test

# Lint
golangci-lint run
```

## Key Integration Points

| Service | Address | Protocol |
|---------|---------|----------|
| fusion-mlx (local inference) | :11434 | HTTP/OpenAI-compatible |
| Fusion-Gateway (this service) | :11432 | HTTP/OpenAI-compatible |
| Fusion-KB | :11434 | HTTP |
| Fusion-Doc | :11449 | HTTP |

## Fusion Ecosystem Context

This gateway is part of the Fusion-MLX monorepo at `/Users/dahai/fusion/`. Sibling projects:
- **fusion-mlx** — local MLX inference engine (the primary local backend)
- **fusion-desk** — desktop automation platform (Python, MCP server)
- **fusion-plugins-ecosystem** — plugin registry with `ClaudeGateway` for Claude full-chain adaptation
- **fusion-studio** — macOS native SwiftUI desktop client
- **fusion-model-hub** — model repository and management
- **fusion-code** — code generation/editing tools

The fusion-plugins-ecosystem `ClaudeGateway` handles Claude Desktop/Code/Web integration at the plugin layer; this Fusion-Gateway handles the lower-level inference routing and load balancing.

## Configuration

Routing rules are YAML-based with hot reload support. Key config knobs:

```yaml
server:
  port: 11432
  auto_start:
    enabled: true
    command: "~/claude-home/fusion-mlx/start.sh start"
    stop_cmd: "~/claude-home/fusion-mlx/start.sh stop"
    wait_url: "http://127.0.0.1:11434/health"
    wait_secs: 120

route:
  enable_hardware_judge: true
  token_threshold: 50
  local_max_memory_ratio: 0.8
  local_max_concurrent: 8
  circuit_breaker:
    memory_overload: true
    swap_triggered: true
    model_offline: true
```

## Conventions

- Go code, 4-space indentation (multiples of 4)
- Structured logging throughout (`slog` or `zerolog`)
- No external state stores (no Redis, no DB) — single binary, local files only
- SSE streaming must be zero-buffer async forwarding
- Client cancel events must propagate downstream to release KV cache
- All metrics: local QPS, cloud QPS, local hit rate, avg latency, circuit breaker trips
