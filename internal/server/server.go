package server

import (
    "context"
    "fmt"
    "net"
    "os"
    "strings"
    "sync"
    "time"

    "crypto/subtle"
    "log/slog"
    "net/http"
    "net/http/pprof"
    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/admin"
    "github.com/fusion-gateway/fusion-gateway/internal/connector"
    adminui "github.com/fusion-gateway/fusion-gateway/internal/admin/ui"
    "github.com/fusion-gateway/fusion-gateway/internal/cache"
    "github.com/fusion-gateway/fusion-gateway/internal/cluster"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/cost"
    "github.com/fusion-gateway/fusion-gateway/internal/crypto"
    "github.com/fusion-gateway/fusion-gateway/internal/hardware"
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
    oauth2StatesMu    sync.RWMutex
    // taskRegistry tracks in-flight inference tasks by id (X-Request-ID) so
    // the POST /v1/agent/tasks/{id}/cancel endpoint can propagate cancellation
    // to a running stream's ctx (#102 ADR-001 sub-task 4). Slot release stays
    // on the stream goroutine's defer — registry only holds the cancel func.
    taskRegistry *TaskRegistry
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
) *Server {
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
    }
    srv.taskRegistry.SetLimits(
        cfg.Config.Routing.AgentTasks.TTL,
        cfg.Config.Routing.AgentTasks.MaxEntries,
    )
    safego.Go("server_reap_expired_tasks", srv.reapExpiredTasks)
    safego.Go("server_evict_oauth2_states", srv.evictOAuth2States)
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
    if s.mcpHandler != nil {
        s.mcpHandler.RegisterRoutesWithMiddleware(mux, s.withMiddleware)
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
    err := s.httpServer.Shutdown(ctx)
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
    // have already recorded their cost.
    if ms, ok := s.store.(*memorystore.MemoryStore); ok {
        ms.FlushQuota()
    }
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
    s.cfg = newCfg
    s.buildMiddlewareChain()
    slog.Info("middleware chain rebuilt on config reload")
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

