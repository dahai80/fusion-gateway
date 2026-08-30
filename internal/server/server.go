package server

import (
    "context"
    "fmt"
    "io"
    "net"
    "os"
    "strings"
    "sync"
    "time"

    "crypto/subtle"
    "log/slog"
    "net/http"
    "net/http/pprof"
    "runtime/debug"
    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/admin"
    "github.com/fusion-gateway/fusion-gateway/internal/connector"
    adminui "github.com/fusion-gateway/fusion-gateway/internal/admin/ui"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/browser"
    "github.com/fusion-gateway/fusion-gateway/internal/cluster"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/cost"
    "github.com/fusion-gateway/fusion-gateway/internal/crypto"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
    "github.com/fusion-gateway/fusion-gateway/internal/lifecycle"
    "github.com/fusion-gateway/fusion-gateway/internal/mcp"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
    "github.com/fusion-gateway/fusion-gateway/internal/realtime"
    "github.com/fusion-gateway/fusion-gateway/internal/safego"
    redisstore "github.com/fusion-gateway/fusion-gateway/internal/store/redis"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
    memorystore "github.com/fusion-gateway/fusion-gateway/internal/store/memory"
    "github.com/fusion-gateway/fusion-gateway/internal/tokenizer"

    "gopkg.in/natefinch/lumberjack.v2"
)

// oauth2StateEntry binds an issued OAuth2 state to the connector_key that
// initiated the flow (B13). The callback verifies stored.connectorKey matches
// the connector_key in the redirect — without this, a state issued for
// connector A could be replayed against connector B's callback (cross-connector
// state replay), exfiltrating a token into the wrong connector.
type oauth2StateEntry struct {
    connectorKey string
    issuedAt     time.Time
}

type Server struct {
    cfg             *config.ConfigSnapshot
    cfgPath         string
    hwCollector     *hardware.Collector
    router          *router.Engine
    pool            *adapter.Pool
    tokEngine       *tokenizer.Engine
    httpServer      *http.Server
    // unixListener holds the inbound UDS listener when server.unix_socket is
    // enabled; nil otherwise. Closed + unlinked in Shutdown.
    unixListener    net.Listener
    startTime       time.Time
    realtimeProxy   *realtime.Proxy
    rateLimiter     *middleware.RateLimiter
    cache           *cache.Cache
    // R2: coalesces concurrent same-key non-stream chat fetches to prevent
    // cold-key cache stampede (N misses → N upstream calls). nil-safe: the
    // fetch path skips coalescing when this is nil (e.g. cache disabled).
    fetchCoalescer  *coalescer
    costTracker     *cost.Tracker
    piiMiddleware   *middleware.PIIMiddleware
    cloudStrategy   *router.CloudStrategy
    latencyTracker  *router.LatencyTracker
    store           store.Store
    semanticCache   *cache.SemanticCache
    otelShutdown    func(context.Context) error
    // A1 fix: DI-constructed auth dependencies (no package-level globals)
    oidcAuth  *middleware.OIDCAuthenticator
    adminAuth *admin.AdminAuth
    // M1 fix: middleware chain constructed once, rebuilt on reload
    middlewareChainMu sync.RWMutex
    middlewareChain   func(http.Handler) http.Handler
    // cfgMu guards the s.cfg pointer swap on config reload. The single writer
    // is RebuildMiddlewareChain (OnReload). Background reaper goroutines
    // (reapExpiredTasks, reapExpiredStreamBuffers) read s.cfg concurrently on
    // lifecycle.Worker goroutines — without this mutex, the reload swap races
    // the reaper reads (race detector flagged server.go:937 vs
    // agent_tasks.go:91). Request-handler goroutines also read s.cfg but never
    // concurrently with a reload in the same test window; the mutex is
    // nonetheless correct for all readers. snapshot() is the RLock helper.
    cfgMu sync.RWMutex
    clusterDiscovery interface {
        Status() []cluster.NodeStatus
        GetNode(id string) (*cluster.Node, bool)
    }
    // adapterIndexRefresher is wired by main.go so the inbound model-hub
    // webhook receiver can trigger an immediate AdapterIndex refresh on
    // adapter.* events (instead of waiting for the 60s poll). nil when no
    // fusion-mlx backend is configured — the receiver then acknowledges
    // adapter events but skips the refresh (nothing to refresh).
    adapterIndexRefresher adapterIndexRefresher
    connectorRegistry *connector.Registry
    oauth2States      map[string]oauth2StateEntry
    mcpHandler        *mcp.Handler
    // #118: dedicated MCP HTTP listener for security-domain isolation from the
    // main :11432 mux. nil unless mcp.enabled && mcp.listen_enabled. Drained in
    // Shutdown BEFORE the main httpServer so MCP's forwardToNode outbound calls
    // finish before the main drain cuts their upstream (F7 ordering).
    mcpServer   *http.Server
    mcpListener net.Listener
    // mcpGate is the MCP-specific auth gate (mcp.token || auth.master_key),
    // independent of the main auth chain. Applied to BOTH the dedicated
    // listener (sole gate) and the shared-listener path (layered on top of
    // withMiddleware) so auth.enabled=false does not open MCP. nil when MCP
    // is disabled.
    mcpGate func(http.Handler) http.Handler
    oauth2StatesMu    sync.RWMutex
    // taskRegistry tracks in-flight inference tasks by id (X-Request-ID) so
    // the POST /v1/agent/tasks/{id}/cancel endpoint can propagate cancellation
    // to a running stream's ctx (#102 ADR-001 sub-task 4). Slot release stays
    // on the stream goroutine's defer — registry only holds the cancel func.
    taskRegistry *TaskRegistry
    // streamBuffers holds resumable SSE event windows keyed by stream_id
    // (issue #116) for the local MLX path only. Populated when
    // routing.stream.resume_enabled; the /v1/messages/{stream_id}/events
    // replay endpoint drains it honoring Last-Event-ID. nil when disabled.
    streamBuffers *StreamBufferStore
    // R1: lifecycle.Workers wrapping the 3 server-owned reaper goroutines so
    // Shutdown can Stop (cancel + join) each instead of leaking. The cache/
    // semantic/ratelimit evictors are owned by their respective constructors and
    // stopped via their own Close() below.
    reapTasksWorker        *lifecycle.Worker
    reapStreamBuffersWorker *lifecycle.Worker
    evictOAuth2Worker      *lifecycle.Worker
    // R6: server-wide shutdown signal. liveCtx in handleStreamChatResumable is
    // a child of shutdownCtx so a graceful Shutdown cancels every in-flight
    // resumable pump (unblocking its upstream body.Read, releasing the local
    // slot) instead of letting it keep hitting a torn-down fusion-mlx until the
    // 5-min idle watchdog or a read error fires. shutdownCancel is called in
    // Shutdown BEFORE the main httpServer drain so the pumps stop issuing new
    // upstream reads while in-flight responses still flush.
    shutdownCtx    context.Context
    shutdownCancel context.CancelFunc
    // R4 (audit): build-time version/commit, stamped by main.go via SetVersion
    // and surfaced on /v1/status. Defaults until SetVersion is called.
    version string
    commit  string
    // R10 (audit): global concurrent-stream semaphore (buffered struct channel).
    // Sized by routing.stream.max_concurrent_streams in server.New; nil when the
    // cap is 0 (disabled, back-compat). acquireStreamSlot acquires/releases.
    streamSem chan struct{}
    // #130: cross-node browser-session scheduling HTTP handler. nil when
    // browser.enabled is false (no routes registered, no subsystem wired). The
    // registry/scheduler/proxy lifecycle is owned by main.go (registry.Start via
    // a tracked lifecycle.Worker, registry.Stop on shutdown); the server only
    // holds the handler to register its routes on the main mux. Routes are
    // registered in New only when this is non-nil — a hot-disable makes the
    // handlers return 503 (matches the mcp toggle behavior); a full unregister
    // needs a restart.
    browserHandler *browser.Handler
}

// SetVersion stamps the build-time version + commit onto the server so /v1/status
// reports the running binary. Called from main.go right after server.New.
func (s *Server) SetVersion(version, commit string) {
    s.version = version
    s.commit = commit
    slog.Info("gateway build version", "version", version, "commit", commit)
}

// setupLogging configures the global slog default from the server.log_level
// config knob (R5 audit fix) and, when observability.log_file is set, mirrors
// structured logs to a rotating file via lumberjack (S3). Before S3 the file
// rotation knobs were dead config and logs grew unbounded on stderr. Call once
// at startup; unknown levels fall back to Info with a warning so a typo never
// silently disables logging. An empty logFile keeps the stderr-only behavior.
func setupLogging(logLevel, logFile string, maxSize, maxBackups int) {
    level, known := parseLevel(logLevel)
    if !known {
        // Warn via a fresh Info-level handler BEFORE SetDefault so the message
        // is always visible even when the (unknown) level would have hidden it.
        slog.Warn("unknown log_level, falling back to info", "configured", logLevel)
    }
    var w io.Writer = os.Stderr
    if strings.TrimSpace(logFile) != "" {
        lw := &lumberjack.Logger{
            Filename:   logFile,
            MaxSize:    maxSize,
            MaxBackups: maxBackups,
            Compress:   true,
        }
        w = io.MultiWriter(os.Stderr, lw)
        slog.Info("file logging enabled",
            "file", logFile,
            "max_size_mb", maxSize,
            "max_backups", maxBackups)
    }
    handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
    slog.SetDefault(slog.New(handler))
}

// parseLevel maps a config log_level string to an slog.Level. Returns known=false
// for anything outside the accepted set so the caller can warn; empty defaults
// to Info silently (the common no-config path).
func parseLevel(logLevel string) (slog.Level, bool) {
    switch strings.ToLower(strings.TrimSpace(logLevel)) {
    case "debug":
        return slog.LevelDebug, true
    case "warn", "warning":
        return slog.LevelWarn, true
    case "error":
        return slog.LevelError, true
    case "", "info":
        return slog.LevelInfo, true
    default:
        return slog.LevelInfo, false
    }
}

func (s *Server) SetClusterDiscovery(d interface {
    Status() []cluster.NodeStatus
    GetNode(id string) (*cluster.Node, bool)
}) {
    s.clusterDiscovery = d
}

// SetAdapterIndexRefresher wires the LoRA AdapterIndex refresh callback used by
// the inbound model-hub webhook receiver (POST /webhooks/model-hub). main.go
// passes a shim around *adapter.AdapterIndex; nil when no fusion-mlx backend
// is configured (the receiver then skips refresh on adapter.* events).
func (s *Server) SetAdapterIndexRefresher(r adapterIndexRefresher) {
    s.adapterIndexRefresher = r
    if r == nil {
        slog.Info("adapter index refresher not wired (nil), webhook adapter.* events will skip refresh")
    } else {
        slog.Info("adapter index refresher wired to webhook receiver")
    }
}

func (s *Server) GetStore() store.Store {
    return s.store
}

func (s *Server) Cache() *cache.Cache {
    return s.cache
}

func (s *Server) checkBackendAccess(w http.ResponseWriter, r *http.Request, backend string) bool {
    if !middleware.CheckBackendAccess(r, backend) {
        slog.Warn("backend access denied", "backend", backend)
        http.Error(w, `{"error":{"message":"Backend not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return false
    }
    return true
}

// recordOutcome routes a success/failure record to the right breaker (RR5).
// For cluster requests with a specific node ID, it records on the per-node
// breaker so one bad node trips itself without poisoning the N-1 healthy
// nodes through the shared cluster breaker. For local/cloud (no node ID) it
// falls back to the legacy per-backend breaker.
func (s *Server) recordOutcome(backend router.Backend, nodeID string, success bool) {
    if backend == router.ClusterBackend && nodeID != "" {
        if success {
            s.router.RecordNodeSuccess(nodeID)
        } else {
            s.router.RecordNodeFailure(nodeID)
        }
        return
    }
    if success {
        s.router.RecordSuccess(string(backend))
    } else {
        s.router.RecordFailure(string(backend))
    }
}

// streamDeadline (R9 audit) wraps ctx with a per-request duration ceiling from
// routing.stream.max_request_duration. The stream HTTP client uses Timeout:0
// (R3), so Client.Timeout no longer bounds the body read; without a server-side
// deadline a cloud backend configured Timeout:0 could stall a handler goroutine
// + client connection indefinitely. Returns the parent ctx unchanged (no
// cancel) when the configured duration is 0 — preserves back-compat and leaves
// short ops (embeddings, rerank, models) unbounded by the stream ceiling. The
// caller MUST defer the returned cancel so the deadline ctx releases when the
// handler returns, even on panic.
func (s *Server) streamDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
    d := s.cfg.Config.Routing.Stream.MaxRequestDuration
    if d <= 0 {
        return ctx, func() {}
    }
    return context.WithTimeout(ctx, d)
}

// acquireStreamSlot (R10 audit) acquires a slot from the global concurrent-
// stream semaphore, sized by routing.stream.max_concurrent_streams. Returns
// true on success. On a full pool, returns false so the caller answers 429
// (the local path is already hard-capped by max_concurrent slots; this caps
// cloud fan-out so a burst cannot exhaust FDs/goroutines). A nil/empty channel
// disables the cap (back-compat). The returned release func MUST be deferred.
// Implemented with a buffered struct channel — a counting semaphore without
// pulling in golang.org/x/sync (keeps the single-binary offline-first build
// lean per the project's no-new-dep preference).
func (s *Server) acquireStreamSlot() (bool, func()) {
    if s.streamSem == nil {
        return true, func() {}
    }
    select {
    case s.streamSem <- struct{}{}:
        return true, func() {
            <-s.streamSem
        }
    default:
        return false, func() {}
    }
}

func newConnectorRegistry(cfg *config.ConfigSnapshot) *connector.Registry {
    r := connector.NewRegistry()
    connector.RegisterBuiltins(r)

    // Set up encryption cipher if master key configured
    var cipher *crypto.AESCipher
    if cfg.Config.Encryption != nil && cfg.Config.Encryption.MasterKey != "" {
        var err error
        cipher, err = crypto.NewAESCipher(cfg.Config.Encryption.MasterKey)
        if err != nil {
            slog.Error("encryption init failed, tokens will not be encrypted", "error", err)
        } else {
            slog.Info("AES-256-GCM encryption enabled")
        }
    }

    // Set up OAuth2 cipher
    if cipher != nil {
        r.OAuth2().SetCipher(cipher)
        r.SetCipher(cipher)
    }

    // Set up persistence
    persistPath := "data/connections.json"
    if cfg.Config.Connector != nil && cfg.Config.Connector.PersistencePath != "" {
        persistPath = cfg.Config.Connector.PersistencePath
    }
    p := connector.NewPersistence(persistPath, cipher)
    r.SetPersistence(p)

    // Load existing connections
    if err := r.LoadFromPersistence(); err != nil {
        slog.Warn("failed to load persisted connections, starting fresh", "error", err)
    }

    return r
}

func New(
    cfg *config.ConfigSnapshot,
    hwCollector *hardware.Collector,
    routerEngine *router.Engine,
    pool *adapter.Pool,
    tokEngine *tokenizer.Engine,
    cfgPath string,
    browserHandler *browser.Handler,
) *Server {
    // R5 (audit): wire config log_level to the global slog default. Without
    // this, the server log_level knob was dead config — slog defaulted to
    // Info regardless. Static at startup; a hot-reload of log_level needs a
    // re-SetDefault (documented in runbook, out of scope here).
    // S3: mirror structured logs to observability.log_file (rotating via
    // lumberjack) so logs are no longer an unbounded stderr stream.
    setupLogging(
        cfg.Config.Server.LogLevel,
        cfg.Config.Observability.LogFile,
        cfg.Config.Observability.LogRotationMaxSize,
        cfg.Config.Observability.LogRotationMaxBackups,
    )

    var rp *realtime.Proxy
    if cfg.Config.Realtime.Enabled {
        rp = realtime.NewProxy(
            cfg.Config.Routing.Negotiation.RouteHeader,
            cfg.Config.Routing.Negotiation.RouteHeaderValue,
            cfg.Config.Realtime.MaxMessageMB,
        )
        slog.Info("realtime proxy enabled", "backend_url", cfg.Config.Realtime.BackendURL)
    }
    lt := router.NewLatencyTracker(1000)
    cs := router.NewCloudStrategy(cfg.Config.CloudRouting, lt)

    logMaxLen := 10000
    if cfg.Config.Admin != nil && cfg.Config.Admin.LogMaxLen > 0 {
        logMaxLen = cfg.Config.Admin.LogMaxLen
    }

    // A3 fix: store factory — select memory or redis backend from config
    var s store.Store
    storeBackend := cfg.Config.Store.Backend
    if storeBackend == "" {
        storeBackend = "memory"
    }
    switch storeBackend {
    case "redis":
        addr := cfg.Config.Store.Redis.Addr
        if addr == "" {
            slog.Error("store.redis.addr is required for redis backend, falling back to memory")
            s = memorystore.NewMemoryStoreWithConfig(logMaxLen, cfg.Config.Batch)
        } else {
            rs, err := redisstore.NewRedisStore(addr, cfg.Config.Store.Redis.Password, cfg.Config.Store.Redis.DB)
            if err != nil {
                slog.Error("redis store init failed, falling back to memory", "error", err)
                s = memorystore.NewMemoryStoreWithConfig(logMaxLen, cfg.Config.Batch)
            } else {
                s = rs
                slog.Info("using redis store", "addr", addr)
            }
        }
    default:
        s = memorystore.NewMemoryStoreWithConfig(logMaxLen, cfg.Config.Batch)
        slog.Info("using memory store")
    }

    if cfg.Config.Store.DataDir != "" {
        if ms, ok := s.(*memorystore.MemoryStore); ok {
            if err := ms.EnablePersistence(cfg.Config.Store.DataDir); err != nil {
                slog.Error("store persistence init failed, continuing non-persistent", "error", err)
            }
        }
    }

    otelShutdown, err := observability.InitTracing(context.Background(), observability.OTelConfig{
        Enabled:     cfg.Config.Observability.OtelEnabled,
        Endpoint:    cfg.Config.Observability.OtelEndpoint,
        Protocol:    cfg.Config.Observability.OtelProtocol,
        ServiceName: cfg.Config.Observability.OtelServiceName,
    })
    if err != nil {
        slog.Warn("otel tracing init failed, continuing without tracing", "error", err)
    }

    // A1 fix: construct OIDCAuthenticator via DI instead of global InitOIDC
    oidcAuth, err := middleware.NewOIDCAuthenticator(middleware.OIDCConfig{
        Enabled:   cfg.Config.OIDC.Enabled,
        Issuer:    cfg.Config.OIDC.Issuer,
        ClientID:  cfg.Config.OIDC.ClientID,
        Audiences: cfg.Config.OIDC.Audiences,
        Scopes:    cfg.Config.OIDC.Scopes,
        ClaimMappings: cfg.Config.OIDC.ClaimMappings,
    })
    if err != nil {
        slog.Warn("oidc authenticator init failed, OIDC auth disabled", "error", err)
    }
    if oidcAuth.Enabled() {
        slog.Info("oidc authenticator initialized", "issuer", cfg.Config.OIDC.Issuer)
    }

    // A1 fix: construct AdminAuth via DI instead of global SetJWTSecret/SetAdminUsers
    var adminAuthObj *admin.AdminAuth
    if cfg.Config.Admin != nil && cfg.Config.Admin.Enabled {
        adminAuthObj, err = admin.NewAdminAuth(cfg.Config.Admin.JWTSecret, cfg.Config.Admin.Users)
        if adminAuthObj != nil && cfg.Config.Server.TLS == nil {
            adminAuthObj.SetInsecureCookie(true)
        }
        if err != nil {
            slog.Warn("admin auth init failed", "error", err)
        } else if adminAuthObj.Enabled() {
            slog.Info("admin auth initialized")
        }
    }
    if adminAuthObj == nil {
        adminAuthObj = &admin.AdminAuth{}
    }

    // R6: server-wide shutdown signal for the resumable-stream liveCtx roots.
    shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
    srv := &Server{
        cfg:               cfg,
        cfgPath:           cfgPath,
        hwCollector:       hwCollector,
        router:            routerEngine,
        pool:              pool,
        tokEngine:         tokEngine,
        startTime:         time.Now(),
        realtimeProxy:     rp,
        rateLimiter:       middleware.NewRateLimiter(),
        cache:             cache.New(cfg.Config.Cache),
        fetchCoalescer:    newCoalescer(),
        costTracker:       cost.NewTracker(10000),
        piiMiddleware:     middleware.NewPIIMiddleware(cfg.Config.PII),
        cloudStrategy:     cs,
        latencyTracker:    lt,
        store:             s,
        semanticCache:     cache.NewSemanticCache(cfg.Config.SemanticCache, nil),
        otelShutdown:      otelShutdown,
        oidcAuth:          oidcAuth,
        adminAuth:         adminAuthObj,
        connectorRegistry: newConnectorRegistry(cfg),
        oauth2States:      make(map[string]oauth2StateEntry),
        mcpHandler:        initMCPHandler(cfg),
        taskRegistry:      NewTaskRegistry(),
        shutdownCtx:       shutdownCtx,
        shutdownCancel:    shutdownCancel,
        browserHandler:    browserHandler,
    }
    // R10 (audit): size the global concurrent-stream semaphore. 0 disables the
    // cap (streamSem stays nil → acquireStreamSlot is a no-op, back-compat).
    if n := cfg.Config.Routing.Stream.MaxConcurrentStreams; n > 0 {
        srv.streamSem = make(chan struct{}, n)
        slog.Info("global concurrent stream cap enabled", "max", n)
    }
    srv.taskRegistry.SetLimits(
        cfg.Config.Routing.AgentTasks.TTL,
        cfg.Config.Routing.AgentTasks.MaxEntries,
    )
    // #118: build the MCP auth gate once. Credentials resolve from mcp.token
    // (preferred) falling back to auth.master_key. Config validation already
    // rejects an enabled MCP with no credential, so the gate here is defense in
    // depth — if both are empty and MCP somehow enabled, the gate rejects every
    // request (fail-closed). nil when MCP is disabled.
    if cfg.Config.MCP.Enabled {
        srv.mcpGate = mcp.AuthGate(mcp.AuthConfig{
            Token:     cfg.Config.MCP.Token,
            MasterKey: cfg.Config.Auth.MasterKey,
        })
        slog.Info("MCP auth gate armed (independent of main auth chain)",
            "has_token", cfg.Config.MCP.Token != "",
            "has_master_key", cfg.Config.Auth.MasterKey != "")
    }
    if cfg.Config.Routing.Stream.ResumeEnabled {
        srv.streamBuffers = NewStreamBufferStore(
            cfg.Config.Routing.Stream.ResumeMaxEvents,
            cfg.Config.Routing.Stream.ResumeMaxBytes,
            cfg.Config.Routing.Stream.ResumeMaxEntries,
            cfg.Config.Routing.Stream.ResumeTTL,
        )
        slog.Info("resumable streams enabled (local MLX path)",
            "max_events", cfg.Config.Routing.Stream.ResumeMaxEvents,
            "max_bytes", cfg.Config.Routing.Stream.ResumeMaxBytes,
            "max_entries", cfg.Config.Routing.Stream.ResumeMaxEntries,
            "ttl", cfg.Config.Routing.Stream.ResumeTTL.String())
        // R1: launch the stream-buffer reaper through lifecycle.Worker so
        // Shutdown can Stop (cancel + join) it instead of leaking. H3
        // panic-restart inherited from the Worker; the loop honors ctx.Done().
        srv.reapStreamBuffersWorker = lifecycle.Start(context.Background(),
            "server_reap_expired_stream_buffers", srv.reapExpiredStreamBuffers)
    }
    // R1: launch the task + oauth2 reapers through lifecycle.Worker so Shutdown
    // can Stop (cancel + join) each instead of leaking. H3 panic-restart
    // inherited from the Worker; each loop honors ctx.Done().
    srv.reapTasksWorker = lifecycle.Start(context.Background(),
        "server_reap_expired_tasks", srv.reapExpiredTasks)
    srv.evictOAuth2Worker = lifecycle.Start(context.Background(),
        "server_evict_oauth2_states", srv.evictOAuth2States)
    return srv
}

func initMCPHandler(cfg *config.ConfigSnapshot) *mcp.Handler {
    if !cfg.Config.MCP.Enabled {
        slog.Info("MCP gateway disabled")
        return nil
    }
    gwCfg := mcp.GatewayConfig{
        Host:        cfg.Config.MCP.Host,
        Port:        cfg.Config.MCP.Port,
        TokenBudget: cfg.Config.MCP.TokenBudget,
        MaxRequests: cfg.Config.MCP.MaxRequests,
        NodePort:    cfg.Config.MCP.NodePort,
        LocalPort:   cfg.Config.MCP.LocalPort,
    }
    gw := mcp.NewMCPClusterGateway(gwCfg)
    gw.Start()
    // #129 Gap 3: install the managed-MCP per-node tool allowlist from config.
    // Empty = unrestricted; non-empty rejects unlisted tools at admission.
    if len(cfg.Config.MCP.ManagedToolAllowlist) > 0 {
        gw.SetManagedToolAllowlist(cfg.Config.MCP.ManagedToolAllowlist)
        slog.Info("MCP managed tool allowlist wired",
            "permitted_tools", len(cfg.Config.MCP.ManagedToolAllowlist),
        )
    }
    slog.Info("MCP cluster gateway initialized", "host", gwCfg.Host, "port", gwCfg.Port)
    return mcp.NewHandler(gw)
}

func (s *Server) Start() error {
    // M1 fix: build middleware chain once at startup
    s.buildMiddlewareChain()

    mux := http.NewServeMux()

    mux.HandleFunc("/v1/chat/completions", s.withMiddleware(s.handleChatCompletions))
    mux.HandleFunc("/v1/completions", s.withMiddleware(s.handleCompletions))
    mux.HandleFunc("/v1/embeddings", s.withMiddleware(s.handleEmbeddings))
    mux.HandleFunc("/v1/rerank", s.withMiddleware(s.handleRerank))
    mux.HandleFunc("/v1/realtime", s.withMiddleware(s.handleRealtime))
    mux.HandleFunc("/v1/models", s.withMiddleware(s.handleModels))
    mux.HandleFunc("/v1/cost", s.withMiddleware(s.handleCost))
    mux.HandleFunc("/v1/images/generations", s.withMiddleware(s.handleImages))
    mux.HandleFunc("/v1/messages", s.withMiddleware(s.handleAnthropicMessages))
    // #116 resumable SSE: replay endpoint for the local MLX path. Prefix-matched
    // (/v1/messages/{stream_id}/events); path parsed in handleStreamResume. Only
    // responds when routing.stream.resume_enabled — otherwise 404.
    mux.HandleFunc("/v1/messages/", s.withMiddleware(s.handleStreamResume))
    // #139 namespace-neutral replay route: /v1/stream/{stream_id}/events.
    // Same handler as /v1/messages/{sid}/events so an OpenAI-wire client
    // (/v1/chat/completions) need not cross into the Anthropic namespace to
    // resume a broken stream — the self-describing X-Fusion-Stream-Resume-URL
    // header on the original stream response points here.
    mux.HandleFunc("/v1/stream/", s.withMiddleware(s.handleStreamResume))
    // #102 ADR-001 sub-task 4: agent task cancel endpoint. Prefix-matched so
    // the {id}/cancel suffix is parsed manually in handleAgentTask (no Go
    // 1.22 path-pattern dep). Auth via withMiddleware (existing key auth).
    mux.HandleFunc("/v1/agent/tasks/", s.withMiddleware(s.handleAgentTask))
    mux.HandleFunc("/v1/audio/transcriptions", s.withMiddleware(s.handleTranscriptions))
    mux.HandleFunc("/v1/audio/speech", s.withMiddleware(s.handleSpeech))
    mux.HandleFunc("/v1/moderations", s.withMiddleware(s.handleModeration))
    mux.HandleFunc("/v1/batches", s.withMiddleware(s.handleBatches))
    mux.HandleFunc("/v1/batches/", s.withMiddleware(s.handleBatchCRUD))

    // Model load/unload interception -> redirect to model-hub
    mux.HandleFunc("/v1/models/", s.withMiddleware(s.handleModelLoadUnload))

    // Model-hub reverse proxy routes
    s.setupModelHubRoutes(mux)

    // MCP cluster gateway routes. F1 fix: route through withMiddleware so MCP
    // endpoints share the gateway auth/rate-limit/budget gate — previously
    // mounted on the bare mux, /mcp/v1/call was reachable unauthenticated and
    // could trigger forwardToNode's outbound dial (SSRF amplifier).
    // #118: the shared chain alone is NOT enough — auth.enabled=false or
    // passthrough reopens /mcp/v1/call. The MCP auth gate (mcp.token ||
    // master_key, independent of the main chain) is now layered on top. When a
    // dedicated MCP listener is configured (mcp.listen_enabled), MCP routes are
    // served ONLY there with the gate as the sole middleware — NOT on the shared
    // mux — for security-domain isolation. Otherwise they stay on the shared mux
    // under shared-chain + gate.
    if s.mcpHandler != nil {
        if s.cfg.Config.MCP.ListenEnabled {
            // Dedicated listener owns MCP routes exclusively; do NOT register
            // them on the shared mux. Listener started below in startMCPListener.
            slog.Info("MCP routes delegated to dedicated listener (not on shared mux)",
                "host", s.cfg.Config.MCP.Host, "port", s.cfg.Config.MCP.Port)
        } else {
            s.mcpHandler.RegisterRoutesWithGate(mux, s.withMiddleware, s.mcpGate)
        }
    }

    // #130: cross-node browser-session scheduling routes. Registered only when
    // the browser handler is non-nil (browser.enabled in config). Create/
    // execute/close go through the key-auth wrap; the admin node map + metrics
    // go through the admin-role wrap — same auth surfaces as /v1/* and /admin/*,
    // no parallel auth path. RegisterRoutes is a no-op when the handler/ proxy
    // is nil, so a disabled subsystem adds no routes.
    if s.browserHandler != nil {
        s.browserHandler.RegisterRoutes(mux, s.withMiddleware, s.withAdminOnly)
        slog.Info("browser scheduling routes registered",
            "node_count", len(s.cfg.Config.Browser.Nodes))
    }

    // Connector plugin framework routes
    mux.HandleFunc("/gateway/v1/connector/list", s.withMiddleware(s.handleConnectorList))
    mux.HandleFunc("/gateway/v1/connector/test", s.withMiddleware(s.handleConnectorTest))
    mux.HandleFunc("/gateway/v1/connector/", s.withMiddleware(s.handleConnectorAction))
    mux.HandleFunc("/gateway/v1/connection", s.withMiddleware(s.handleConnectionList))
    mux.HandleFunc("/gateway/v1/connection/", s.withMiddleware(s.handleConnectionCRUD))
    mux.HandleFunc("/gateway/v1/oauth2/authorize", s.withMiddleware(s.handleOAuth2Authorize))
    mux.HandleFunc("/gateway/v1/oauth2/callback", s.withMiddleware(s.handleOAuth2Callback))

    mux.HandleFunc("/health", s.handleHealth)
    mux.HandleFunc("/healthz", s.handleHealthz)
    mux.HandleFunc("/readyz", s.handleReadyz)
    mux.HandleFunc("/livez", s.handleLivez)
    mux.HandleFunc("/v1/status", s.withMiddleware(s.handleStatus))

    // #34: proxy fusion-mlx /stats through the gateway so clients configured to
    // route mlx traffic via :11432 (e.g. fusion-cli bench mem) can fetch
    // server-side memory stats. Shares the /v1/* fg-key auth chain
    // (withMiddleware); the provider's ReverseProxy injects the backend
    // Authorization + X-Fusion-Route. Path is forwarded verbatim (/stats).
    mux.HandleFunc("/stats", s.withMiddleware(s.handleMLXStats))

    // Audit fix: /metrics requires master-key auth
    mux.HandleFunc("/metrics", s.withMasterKey(observability.Handler().ServeHTTP))

    // Inbound model-hub webhook receiver (POST /webhooks/model-hub): verifies
    // HMAC-SHA256 over the raw body with routing.webhooks.model_hub.secret, and
    // on adapter.* events triggers an immediate AdapterIndex refresh. Only
    // registered when routing.webhooks.model_hub.enabled is true. Not behind
    // withMiddleware (fg-key auth chain) — webhooks authenticate via the shared
    // HMAC secret in the X-Webhook-Signature header, not an API key.
    if modelHubWebhookEnabled(s.cfg) {
        mux.HandleFunc("/webhooks/model-hub", s.handleModelHubWebhook)
        slog.Info("registered inbound model-hub webhook receiver", "path", "/webhooks/model-hub")
    }

    // B11: admin ops behind withAdminOnly (admin JWT role), not withMiddleware
    // (API-key auth). The prior wiring let any low-privilege inference key
    // (even one restricted by allowed_models) force a GC pause or trigger a
    // config reload racing in-flight requests. /admin/teams + /admin/orgs
    // already use withAdminOnly; gc/reload were the outliers.
    mux.HandleFunc("/admin/gc", s.withAdminOnly(s.handleAdminGC))
    mux.HandleFunc("/admin/config/reload", s.withAdminOnly(s.handleConfigReload))
    mux.HandleFunc("/admin/teams", s.withAdminOnly(s.handleAdminTeams))
    mux.HandleFunc("/admin/teams/", s.withAdminOnly(s.handleAdminTeamsCRUD))
    mux.HandleFunc("/admin/orgs", s.withAdminOnly(s.handleAdminOrgs))
    mux.HandleFunc("/admin/orgs/", s.withAdminOnly(s.handleAdminOrgsCRUD))

    // Admin API + Dashboard UI
    if s.cfg.Config.Admin != nil && s.cfg.Config.Admin.Enabled {
        // A1 fix: use DI-constructed adminAuth instead of globals
        adminHandler := admin.NewHandler(s.store, s.adminAuth, s.cfgPath)
        adminHandler.RegisterRoutes(mux)
        mux.HandleFunc("/admin/api/login", s.adminAuth.HandleLogin)
        mux.HandleFunc("/admin/api/logout", s.adminAuth.HandleLogout)
        // #30: proxy fusion-mlx admin fine-tune API through the gateway so
        // fusion-trainer can target :11432 exclusively. Registered before the
        // /admin/ SPA catch-all so it takes precedence; shares the /v1/*
        // fg-key auth chain (withMiddleware). Gateway injects X-Fusion-Route
        // internally — clients send nothing extra.
        mux.HandleFunc("/admin/api/fine-tune/", s.withMiddleware(s.handleMLXAdminProxy))
        mux.Handle("/admin/", http.StripPrefix("/admin/", adminui.Handler()))
    }

    // pprof endpoints — disabled by default, enable via server.enable_pprof
    if s.cfg.Config.Server.EnablePProf {
        mux.HandleFunc("/debug/pprof/", s.withMasterKey(pprof.Index))
        mux.HandleFunc("/debug/pprof/cmdline", s.withMasterKey(pprof.Cmdline))
        mux.HandleFunc("/debug/pprof/profile", s.withMasterKey(pprof.Profile))
        mux.HandleFunc("/debug/pprof/symbol", s.withMasterKey(pprof.Symbol))
        mux.HandleFunc("/debug/pprof/trace", s.withMasterKey(pprof.Trace))
        slog.Warn("pprof endpoints enabled — access requires master_key")
    }

    addr := fmt.Sprintf("%s:%d", s.cfg.Config.Server.Host, s.cfg.Config.Server.Port)

    s.httpServer = &http.Server{
        Addr:              addr,
        Handler:           mux,
        ReadTimeout:       30 * time.Second,
        ReadHeaderTimeout: 10 * time.Second,
        WriteTimeout:      0, // F3 fix: SSE streams need no write deadline; per-request timeouts handled by ctx
        IdleTimeout:       120 * time.Second,
    }

    // Inbound UDS listener (client -> gateway), orthogonal to TCP. When enabled
    // it serves the same mux on a unix socket. Served in a safeGo goroutine so
    // the TCP path still blocks Start() (main.go relies on Start() blocking until
    // shutdown). When only UDS is desired, TCP still binds (port 0 collision is
    // caught by validate) — keeping TCP up is intentional: admin/dashboard and
    // health probes stay reachable, and UDS is the low-latency app path.
    if uds := s.cfg.Config.Server.UnixSocket; uds != nil && uds.Enabled {
        ln, err := s.listenUnix(uds)
        if err != nil {
            return fmt.Errorf("listen unix socket: %w", err)
        }
        s.unixListener = ln
        slog.Info("inbound UDS listener serving", "path", uds.Path)
        safego.Go("uds_serve", func() {
            if err := s.serve(ln); err != nil && err != http.ErrServerClosed {
                slog.Error("uds serve error", "path", uds.Path, "error", err)
            }
        })
    }

    // #118: dedicated MCP HTTP listener — security-domain isolation from the
    // main :11432 mux. Serves ONLY the MCP routes under the MCP auth gate (sole
    // middleware; no shared rate-limit/budget chain, those are inference
    // concerns). Bound before the main ListenAndServe so a port collision fails
    // Start() fast rather than after the main server is up. Served in a safeGo
    // goroutine; drained in Shutdown BEFORE the main httpServer (F7 ordering —
    // MCP's forwardToNode outbound calls finish before the main drain cuts their
    // upstream). ListenEnabled + Host/Port + credential are validated in
    // config.Validate, so reaching here means a bind is safe to attempt.
    if s.mcpHandler != nil && s.cfg.Config.MCP.ListenEnabled {
        if err := s.startMCPListener(); err != nil {
            return fmt.Errorf("start MCP listener: %w", err)
        }
    }

    if s.cfg.Config.Server.TLS != nil && s.cfg.Config.Server.TLS.CertFile != "" && s.cfg.Config.Server.TLS.KeyFile != "" {
        slog.Info("server starting with TLS", "addr", addr, "cert", s.cfg.Config.Server.TLS.CertFile)
        return s.httpServer.ListenAndServeTLS(s.cfg.Config.Server.TLS.CertFile, s.cfg.Config.Server.TLS.KeyFile)
    }

    slog.Info("server starting", "addr", addr)
    return s.httpServer.ListenAndServe()
}

// listenUnix creates the inbound UDS listener. A stale socket file from a
// previous unclean shutdown is removed first (os.Remove ignores NotExist), then
// net.Listen binds the socket and os.Chmod applies the configured permission
// bits (default 0660) so only the owning group can connect.
func (s *Server) listenUnix(uds *config.UnixSocketConfig) (net.Listener, error) {
    if err := os.Remove(uds.Path); err != nil && !os.IsNotExist(err) {
        return nil, fmt.Errorf("remove stale socket %s: %w", uds.Path, err)
    }
    ln, err := net.Listen("unix", uds.Path)
    if err != nil {
        return nil, fmt.Errorf("listen unix %s: %w", uds.Path, err)
    }
    mode := uds.Mode
    if mode == 0 {
        mode = 0660
    }
    if err := os.Chmod(uds.Path, os.FileMode(mode)); err != nil {
        ln.Close()
        return nil, fmt.Errorf("chmod socket %s: %w", uds.Path, err)
    }
    slog.Info("inbound UDS listener bound", "path", uds.Path, "mode", fmt.Sprintf("%04o", mode))
    return ln, nil
}

// startMCPListener binds the dedicated MCP HTTP listener (#118) on mcp.host:port
// and serves the MCP routes under the MCP auth gate as the sole middleware. The
// listener + server are stored on s for Shutdown drain. Config validation has
// already guaranteed Host/Port are set and a credential exists when MCP is
// enabled, so this only runs when a bind is valid to attempt. Served in a
// safeGo goroutine so the main TCP ListenAndServe still blocks Start().
func (s *Server) startMCPListener() error {
    addr := fmt.Sprintf("%s:%d", s.cfg.Config.MCP.Host, s.cfg.Config.MCP.Port)
    ln, err := net.Listen("tcp", addr)
    if err != nil {
        return fmt.Errorf("listen tcp %s: %w", addr, err)
    }
    s.mcpListener = ln
    mcpMux := http.NewServeMux()
    s.mcpHandler.RegisterRoutesMCPOnly(mcpMux, s.mcpGate)
    s.mcpServer = &http.Server{
        Handler:           mcpMux,
        ReadTimeout:       30 * time.Second,
        ReadHeaderTimeout: 10 * time.Second,
        WriteTimeout:      0, // MCP tool calls may stream / long-poll; no write deadline
        IdleTimeout:       120 * time.Second,
    }
    slog.Info("dedicated MCP listener serving (security-domain isolated, MCP auth gate only)",
        "addr", addr)
    safego.Go("mcp_serve", func() {
        if err := s.mcpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
            slog.Error("mcp serve error", "addr", addr, "error", err)
        }
    })
    return nil
}

// serve runs httpServer.Serve on a listener. Used by the UDS path; the TCP
// path keeps ListenAndServe for its built-in addr binding.
func (s *Server) serve(ln net.Listener) error {
    return s.httpServer.Serve(ln)
}

func (s *Server) Shutdown(ctx context.Context) error {
    slog.Info("server shutting down")
    if s.otelShutdown != nil {
        if err := s.otelShutdown(ctx); err != nil {
            slog.Warn("otel shutdown error", "error", err)
        }
    }
    // #118: drain the dedicated MCP listener BEFORE the main httpServer. MCP
    // tool calls route through forwardToNode to fusion-mlx; draining MCP first
    // lets those in-flight outbound calls complete before the main drain (and
    // the subsequent autoStopLocal) cuts their upstream. Mirrors the F7
    // ordering principle (drain dependents before the upstream they depend on).
    // No-op when no dedicated listener is configured (shared-path MCP drains
    // with the main httpServer).
    if s.mcpServer != nil {
        if merr := s.mcpServer.Shutdown(ctx); merr != nil {
            slog.Warn("mcp listener shutdown error", "error", merr)
        }
    }
    // R6: cancel the resumable-stream liveCtx root so in-flight pumps stop
    // reading from a torn-down fusion-mlx and release their local slots,
    // instead of contending slots with the next process on a fast restart.
    // Done BEFORE the main httpServer drain: the pumps stop issuing new upstream
    // reads while the drain still flushes any in-flight client responses.
    if s.shutdownCancel != nil {
        s.shutdownCancel()
    }
    var err error
    if s.httpServer != nil {
        err = s.httpServer.Shutdown(ctx)
    }
    // Inbound UDS cleanup: close the listener (stops the safeGo serve loop) and
    // unlink the socket file so a stale inode doesn't block the next start.
    if s.unixListener != nil {
        if cerr := s.unixListener.Close(); cerr != nil {
            slog.Warn("unix listener close error", "error", cerr)
        }
        if uds := s.cfg.Config.Server.UnixSocket; uds != nil && uds.Path != "" {
            if rerr := os.Remove(uds.Path); rerr != nil && !os.IsNotExist(rerr) {
                slog.Warn("unix socket unlink error", "path", uds.Path, "error", rerr)
            }
        }
    }
    // F2: flush any pending debounced team quota/cost write so the last burst
    // of AddCost before shutdown reaches disk (memory store only; redis
    // persists per-call). Done after HTTP drain so in-flight billed requests
    // have already recorded their cost. #117: the memory/redis write-profile
    // asymmetry (memory coalesces via debouncedPersister, redis per-call SET)
    // is the documented parity contract — see persistParityNote in
    // internal/store/memory/persist_debounce.go.
    if ms, ok := s.store.(*memorystore.MemoryStore); ok {
        ms.FlushQuota()
    }
    // R1: Stop (cancel + join) the 3 server-owned reapers and the 3
    // constructor-owned evictors so they do not outlive Shutdown and keep
    // ticking against a torn-down store/dead backend (goroutine leak). Before
    // R1 these used `for range ticker.C` with no stop signal and never joined.
    if s.reapTasksWorker != nil {
        s.reapTasksWorker.Stop()
    }
    if s.reapStreamBuffersWorker != nil {
        s.reapStreamBuffersWorker.Stop()
    }
    if s.evictOAuth2Worker != nil {
        s.evictOAuth2Worker.Stop()
    }
    s.rateLimiter.Close()
    s.cache.Close()
    s.semanticCache.Close()
    return err
}

func (s *Server) withMiddleware(handler http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // M1 fix: use cached middleware chain, rebuild on reload
        s.middlewareChainMu.RLock()
        chain := s.middlewareChain
        s.middlewareChainMu.RUnlock()

        start := time.Now()
        entry := middleware.InitRequestLog(r)
        r = middleware.WithRequestLogContext(r, entry)
        rec := middleware.NewResponseRecorder(w)

        // Per-request: inject fresh config snapshot, then run cached chain
        snap := config.GetSnapshot()
        snapMiddleware := middleware.ConfigSnapshot(snap)

        var final http.Handler = snapMiddleware(chain(handler))

        // R2 (audit): a panic in any handler/middleware escaped withMiddleware
        // with no recovery — Go's net/http top-level recover closed the
        // connection (no 500 body, no log) so the operator saw a silent drop
        // and the goroutine's panic stack was lost. Catch it here at the
        // middleware boundary: log the stack + path, write a 500 so the client
        // gets a body, and let the recorder still capture the status for the
        // request log. Keeps the process alive (net/http would too) but makes
        // the failure loud and observable.
        defer func() {
            if rv := recover(); rv != nil {
                slog.Error("handler panic recovered",
                    "path", r.URL.Path,
                    "method", r.Method,
                    "panic", rv,
                    "stack", string(debug.Stack()))
                if !rec.Written() {
                    http.Error(rec, `{"error":{"message":"internal server error"}}`, http.StatusInternalServerError)
                }
            }
        }()
        final.ServeHTTP(rec, r)

        keyCfg := middleware.GetAuthKeyConfig(r.Context())
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        entry.StatusCode = rec.StatusCode
        middleware.FinalizeAndAppendLog(entry, s.store, start, keyName)
    }
}

// buildMiddlewareChain constructs the static middleware chain (everything except ConfigSnapshot)
func (s *Server) buildMiddlewareChain() {
    chain := []func(http.Handler) http.Handler{
        middleware.RequestID,
        observability.HTTPMiddleware,
        middleware.CORS(&s.cfg.Config.CORS),
        middleware.APIKeyAuthWithStore(&s.cfg.Config.Auth, s.store),
        s.oidcAuth.Middleware(&s.cfg.Config.Auth),
        middleware.RBACAuth(&s.cfg.Config.RBAC, &s.cfg.Config.Team),
        middleware.PromptInjectionMiddleware(s.cfg.Config.PromptInjection),
        middleware.BudgetBlock(s.store),
        middleware.RateLimit(&s.cfg.Config.Routing.RateLimit, s.rateLimiter, nil),
    }

    s.middlewareChainMu.Lock()
    var combined func(http.Handler) http.Handler
    for i := len(chain) - 1; i >= 0; i-- {
        if combined == nil {
            combined = chain[i]
        } else {
            prev := combined
            mw := chain[i]
            combined = func(next http.Handler) http.Handler {
                return mw(prev(next))
            }
        }
    }
    if combined == nil {
        combined = func(next http.Handler) http.Handler { return next }
    }
    s.middlewareChain = combined
    s.middlewareChainMu.Unlock()
}

// RebuildMiddlewareChain rebuilds the middleware chain on config reload
func (s *Server) RebuildMiddlewareChain(newCfg *config.ConfigSnapshot) {
    // cfgMu: the pointer swap must not race background reaper goroutines that
    // read s.cfg on their own lifecycle.Worker (race detector: server.go:937
    // write vs agent_tasks.go:91 read). Take the write lock for the swap, then
    // rebuild the middleware chain under the existing middlewareChainMu.
    s.cfgMu.Lock()
    s.cfg = newCfg
    s.cfgMu.Unlock()
    s.buildMiddlewareChain()
    // #129 Gap 3: propagate managed-MCP tool allowlist changes on hot-reload
    // so toggling mcp.managed_tool_allowlist takes effect without a restart.
    // No-op when MCP disabled (mcpHandler nil). Empty list = unrestricted.
    if s.mcpHandler != nil {
        s.mcpHandler.SetManagedToolAllowlist(newCfg.Config.MCP.ManagedToolAllowlist)
    }
    slog.Info("middleware chain rebuilt on config reload")
}

// snapshot returns the current config snapshot under cfgMu.RLock. Background
// goroutines (reapers) use this instead of a bare s.cfg read so the reload
// pointer swap cannot race them.
func (s *Server) snapshot() *config.ConfigSnapshot {
    s.cfgMu.RLock()
    defer s.cfgMu.RUnlock()
    return s.cfg
}

// withMasterKey wraps handler to require master_key authentication
func (s *Server) withMasterKey(handler http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        masterKey := s.cfg.Config.Auth.MasterKey
        if masterKey == "" {
            slog.Warn("master_key not configured, denying access to protected endpoint", "path", r.URL.Path)
            http.Error(w, `{"error":{"message":"Unauthorized","type":"auth_error"}}`, http.StatusUnauthorized)
            return
        }
        apiKey := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
        if subtle.ConstantTimeCompare([]byte(apiKey), []byte(masterKey)) != 1 {
            slog.Warn("invalid master_key for protected endpoint", "path", r.URL.Path, "remote", r.RemoteAddr)
            http.Error(w, `{"error":{"message":"Unauthorized","type":"auth_error"}}`, http.StatusUnauthorized)
            return
        }
        handler(w, r)
    }
}

// maxAdminBodySize caps request bodies on admin/connector/oauth2 CRUD paths.
// B10: those handlers decode JSON directly off r.Body with no MaxBytesReader,
// so an authenticated key (or anonymous when auth.enabled=false) could OOM the
// gateway with an unbounded body. Admin payloads are small structured JSON
// (team/org/connector config) — 2 MiB is far beyond any legitimate request.
const maxAdminBodySize int64 = 2 << 20

// maxLegacyBodySize caps /v1/completions request bodies, matching the 10 MiB
// limit already applied to its sibling /v1/chat/completions and /v1/messages
// decode paths. B10: this legacy endpoint was the only inference path missing
// the cap.
const maxLegacyBodySize int64 = 10 << 20

// maxProxyBodySize caps request bodies on reverse-proxy paths (model-hub,
// fusion-mlx admin/fine-tune, /stats, model load/unload). E5 (audit): those
// handlers passed r.Body straight to httputil.ReverseProxy with no cap, so an
// authenticated client could stream an unbounded body into the fusion-mlx
// admin/fine-tune API → single-process OOM or admin event-loop stall, dragging
// local inference down with it. 256 MiB is far beyond any legitimate proxy
// payload (fine-tune config/dataset metadata, model serve request, stats read)
// — actual model binaries are fetched hub↔MLX directly, not through this body.
const maxProxyBodySize int64 = 256 << 20

// proxyBodyCap resolves the per-request body cap for reverse-proxy paths. E5
// (audit): a positive server.proxy_max_body_size from config overrides the
// const default; 0 falls back to the const so an omitted YAML key still caps
// the body (defense in depth — the const alone was never enforced before E5
// because the proxy paths never wrapped r.Body).
func (s *Server) proxyBodyCap() int64 {
    if s.cfg != nil && s.cfg.Config.Server.ProxyMaxBodySize > 0 {
        return s.cfg.Config.Server.ProxyMaxBodySize
    }
    return maxProxyBodySize
}

// wrapProxyBody caps r.Body for a reverse-proxy forward. E5 (audit): returns
// r with the body wrapped in http.MaxBytesReader so an authenticated client
// cannot stream an unbounded body into fusion-mlx / model-hub admin APIs.
func (s *Server) wrapProxyBody(w http.ResponseWriter, r *http.Request) *http.Request {
    r.Body = http.MaxBytesReader(w, r.Body, s.proxyBodyCap())
    return r
}

