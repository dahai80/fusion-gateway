package server

import (
    "context"
    "crypto/rand"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "io"
    "log/slog"
    "net"
    "net/http"
    "net/http/pprof"
    "os"
    "strings"
    "sync"
    "time"

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
    oauth2States      map[string]time.Time
    mcpHandler        *mcp.Handler
    oauth2StatesMu    sync.RWMutex
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
        oauth2States:      make(map[string]time.Time),
        mcpHandler:        initMCPHandler(cfg),
    }
    go srv.evictOAuth2States()
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
    mux.HandleFunc("/v1/audio/transcriptions", s.withMiddleware(s.handleTranscriptions))
    mux.HandleFunc("/v1/audio/speech", s.withMiddleware(s.handleSpeech))
    mux.HandleFunc("/v1/moderations", s.withMiddleware(s.handleModeration))
    mux.HandleFunc("/v1/batches", s.withMiddleware(s.handleBatches))
    mux.HandleFunc("/v1/batches/", s.withMiddleware(s.handleBatchCRUD))

    // Model load/unload interception -> redirect to model-hub
    mux.HandleFunc("/v1/models/", s.withMiddleware(s.handleModelLoadUnload))

    // Model-hub reverse proxy routes
    s.setupModelHubRoutes(mux)

    // MCP cluster gateway routes
    if s.mcpHandler != nil {
        s.mcpHandler.RegisterRoutes(mux)
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

    mux.HandleFunc("/admin/gc", s.withMiddleware(s.handleAdminGC))
    mux.HandleFunc("/admin/config/reload", s.withMiddleware(s.handleConfigReload))
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
        if apiKey != masterKey {
            slog.Warn("invalid master_key for protected endpoint", "path", r.URL.Path, "remote", r.RemoteAddr)
            http.Error(w, `{"error":{"message":"Unauthorized","type":"auth_error"}}`, http.StatusUnauthorized)
            return
        }
        handler(w, r)
    }
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20 // default 5MB
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req adapter.ChatRequest
    if err := json.Unmarshal(body, &req); err != nil {
        slog.Error("invalid json in chat request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)

    // Issue #28: empty model would be passed through to fusion-mlx and 404.
    // Backfill from routing.default_model, or auto-discover the first loaded
    // local model when no default is configured.
    if strings.TrimSpace(req.Model) == "" {
        resolved, err := s.resolveDefaultModel(ctx)
        if err != nil {
            slog.Warn("default model resolution failed", "error", err)
        }
        if resolved != "" {
            slog.Info("backfilling empty model", "model", resolved)
            req.Model = resolved
        }
    }

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    textContent := extractTextContent(req.Messages)

    // PII scanning
    if s.piiMiddleware != nil {
        deny, detected := s.piiMiddleware.ScanText(textContent)
        if deny {
            slog.Warn("request denied: PII detected", "types", detected, "model", req.Model)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"Request contains PII (%s)","type":"pii_error"}}`, strings.Join(detected, ",")), http.StatusBadRequest)
            return
        }
    }

    inputTokens, err := s.tokEngine.CountTokens(ctx, textContent)
    if err != nil {
        slog.Error("token counting failed", "error", err)
        inputTokens = len(textContent) / 4
    }

    budget := s.tokEngine.EstimateBudget(inputTokens, req.MaxTokens, req.Model, req.Tools != nil, req.Stream)
    ctx = tokenizer.WithTokenBudget(ctx, budget)

    routeReq := &router.RouteRequest{
        Model:   req.Model,
        Text:    textContent,
        Stream:  req.Stream,
        SpaceID: adapter.SpaceIDFromContext(ctx),
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    slog.Info("route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_tokens", budget.InputTokens,
        "total_budget", budget.TotalBudget,
    )

    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, ok := s.pool.Get("fusion-mlx")
        if !ok {
            http.Error(w, `{"error":{"message":"Local backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p
        // intent:code hot-swap: the routing engine detected a coding intent
        // and selected a LoRA adapter to mount on fusion-mlx. Inject it into
        // the per-request "adapters" field so fusion-mlx hot-loads the derived
        // engine (FUSION_LORA_INPLACE_SWAP=1 = ms-scale, no base reload).
        if decision.Adapter != "" {
            req.Adapters = decision.Adapter
            slog.Info("lora adapter hot-swap on local backend",
                "adapter", decision.Adapter,
                "model", req.Model,
                "reason", decision.Reason,
            )
        }

    case router.ClusterBackend:
        if s.clusterDiscovery == nil || decision.NodeID == "" {
            slog.Warn("cluster backend selected but no discovery or nodeID, falling back to cloud",
                "node_id", decision.NodeID,
            )
            provider = s.resolveCloudProvider(decision, &req, w)
            if provider == nil {
                return
            }
        } else {
            node, ok := s.clusterDiscovery.GetNode(decision.NodeID)
            if !ok {
                slog.Warn("cluster node not found, falling back to cloud",
                    "node_id", decision.NodeID,
                )
                provider = s.resolveCloudProvider(decision, &req, w)
                if provider == nil {
                    return
                }
            } else {
                slog.Info("routing to cluster node",
                    "node_id", node.ID,
                    "node_addr", node.Address,
                    "strategy", decision.Reason,
                )
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            }
        }

    default:
        provider = s.resolveCloudProvider(decision, &req, w)
        if provider == nil {
            return
        }
    }

    if spaceID := adapter.SpaceIDFromContext(ctx); spaceID != "" {
        s.router.RecordAffinity(spaceID, provider.Name())
    }

    start := time.Now()
    tenant := "anonymous"
    if kc := middleware.GetAuthKeyConfig(r.Context()); kc != nil && kc.Name != "" {
        tenant = kc.Name
    }

    if req.Stream {
        s.handleStreamChat(ctx, w, provider, &req, decision, budget, start)
    } else {
        s.handleNonStreamChat(ctx, w, provider, &req, decision, budget, start, tenant)
    }
}

func (s *Server) resolveCloudProvider(decision *router.RouteDecision, req *adapter.ChatRequest, w http.ResponseWriter) adapter.Provider {
    cloudBackend := ""
    if decision != nil && decision.CloudTarget != "" {
        cloudBackend = decision.CloudTarget
        slog.Info("using token tier cloud target", "cloud_target", cloudBackend)
    }
    if cloudBackend == "" {
        cloudBackend = s.cfg.Config.Routing.Fallback.CloudDefault
    }
    if cloudBackend == "" {
        cloudBackend = "openai"
    }

    // Cloud strategy: if multiple cloud backends available, let strategy decide
    if s.cloudStrategy != nil && cloudBackend == s.cfg.Config.Routing.Fallback.CloudDefault {
        var availableBackends []string
        for _, name := range s.pool.ListProviders() {
            if p, ok := s.pool.Get(name); ok {
                if p.Name() != "fusion-mlx" {
                    availableBackends = append(availableBackends, name)
                }
            }
        }
        if len(availableBackends) > 1 {
            selected := s.cloudStrategy.Select(availableBackends)
            if selected != "" {
                slog.Info("cloud strategy selected backend", "strategy", s.cfg.Config.CloudRouting.Strategy, "backend", selected)
                cloudBackend = selected
            }
        }
    }

    if req != nil {
        req.Model = s.applyCloudModelMapping(req.Model, cloudBackend)
    }

    p, ok := s.pool.Get(cloudBackend)
    if !ok {
        slog.Error("cloud backend not available", "backend", cloudBackend)
        if w != nil {
            http.Error(w, `{"error":{"message":"Cloud backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
        }
        return nil
    }
    return p
}

// applyCloudModelMapping maps a client-supplied model name to the cloud
// backend's actual model id via routing.fallback.model_mapping. This is what
// lets SDK model aliases (e.g. claude code's claude-opus-4-7) reach a backend
// that only serves a different model id (e.g. glm5.2 via LiteLLM). Returns the
// input unchanged when mapping is disabled or no entry matches. s.cfg is
// swapped on hot-reload via RebuildMiddlewareChain (main.go OnReload), so newly
// added aliases take effect after /admin/config/reload (which calls
// config.Reload) — see issue #57.
func (s *Server) applyCloudModelMapping(model, cloudBackend string) string {
    if model == "" {
        return model
    }
    if !s.cfg.Config.Routing.Fallback.Enabled || s.cfg.Config.Routing.Fallback.ModelMapping == nil {
        return model
    }
    mapped, ok := s.cfg.Config.Routing.Fallback.ModelMapping[model]
    if !ok || strings.TrimSpace(mapped) == "" {
        return model
    }
    slog.Info("model mapped for cloud routing",
        "local_model", model,
        "cloud_model", mapped,
        "cloud_backend", cloudBackend,
        "config_version", s.cfg.Version,
    )
    return mapped
}

func (s *Server) handleStreamChat(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time) {
    ch, err := provider.StreamChat(ctx, req)
    if err != nil {
        slog.Error("stream chat failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Stream chat failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
    w.Header().Set("X-Fusion-Degraded", "false")

    flusher, canFlush := w.(http.Flusher)
    var degraded bool
    var outputTokens int
    includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
    var lastChunkID string
    var lastChunkModel string
    var lastChunkCreated int64

    for chunk := range ch {
        // F2 fix: check for client cancel to propagate downstream and release KV cache
        select {
        case <-ctx.Done():
            slog.Info("stream chat cancelled by client", "provider", provider.Name(), "error", ctx.Err())
            // Try to cancel on the provider if supported
            if canceller, ok := provider.(interface{ Cancel(string) }); ok {
                canceller.Cancel(lastChunkID)
            }
            return
        default:
        }

        if chunk.Usage != nil && chunk.Usage.CompletionTokens > 0 {
            outputTokens += chunk.Usage.CompletionTokens
        }
        lastChunkID = chunk.ID
        lastChunkModel = chunk.Model
        lastChunkCreated = chunk.Created

        if chunk.Degraded {
            degraded = true //nolint:ineffassign
            w.Header().Set("X-Fusion-Degraded", "true")
            slog.Warn("stream degraded: backpressure triggered, falling back to non-streaming")

            warningEvt := map[string]string{"type": "warning", "message": "stream degraded, falling back to non-streaming"}
            warningData, _ := json.Marshal(warningEvt)
            if _, err := fmt.Fprintf(w, "event: warning\ndata: %s\n\n", warningData); err != nil {
                slog.Error("failed to write warning event", "error", err)
                return
            }
            if canFlush {
                flusher.Flush()
            }

            // L5 fix: deep copy request to avoid shared slice race
            nonStreamReq := *req
            nonStreamReq.Stream = false
            nonStreamReq.Messages = make([]adapter.ChatMessage, len(req.Messages))
            copy(nonStreamReq.Messages, req.Messages)
            resp, fallbackErr := provider.Chat(ctx, &nonStreamReq)
            if fallbackErr != nil {
                slog.Error("non-streaming fallback failed", "error", fallbackErr)
                errEvt := map[string]string{"type": "error", "message": "non-streaming fallback also failed"}
                errData, _ := json.Marshal(errEvt)
                if _, err := fmt.Fprintf(w, "event: error\ndata: %s\n\n", errData); err != nil {
                    slog.Error("failed to write error event", "error", err)
                }
                if canFlush {
                    flusher.Flush()
                }
                return
            }

            finalChunk := adapter.StreamChunk{
                ID:      resp.ID,
                Object:  "chat.completion.chunk",
                Created: resp.Created,
                Model:   resp.Model,
                Choices: make([]adapter.ChoiceDelta, len(resp.Choices)),
            }
            for i, c := range resp.Choices {
                finishReason := c.FinishReason
                finalChunk.Choices[i] = adapter.ChoiceDelta{
                    Index:        c.Index,
                    Delta:        c.Message,
                    FinishReason: &finishReason,
                }
            }

            data, _ := json.Marshal(finalChunk)
            fmt.Fprintf(w, "data: %s\n\n", data)
            if canFlush {
                flusher.Flush()
            }
            return
        }

        data, err := json.Marshal(chunk)
        if err != nil {
            slog.Error("marshal stream chunk failed", "error", err)
            continue
        }

        fmt.Fprintf(w, "data: %s\n\n", data)
        if canFlush {
            flusher.Flush()
        }
    }

    // Send usage chunk if stream_options.include_usage was requested
    if includeUsage {
        usageChunk := adapter.StreamChunk{
            ID:      lastChunkID,
            Object:  "chat.completion.chunk",
            Created: lastChunkCreated,
            Model:   lastChunkModel,
            Choices: []adapter.ChoiceDelta{},
            Usage: &adapter.UsageResponse{
                PromptTokens:     budget.InputTokens,
                CompletionTokens: outputTokens,
                TotalTokens:      budget.InputTokens + outputTokens,
            },
        }
        usageData, _ := json.Marshal(usageChunk)
        fmt.Fprintf(w, "data: %s\n\n", usageData)
        if canFlush {
            flusher.Flush()
        }
    }

    fmt.Fprintf(w, "data: [DONE]\n\n")
    if canFlush {
        flusher.Flush()
    }

    duration := time.Since(start).Seconds()
    if degraded {
        observability.RecordRequest(string(decision.Backend), req.Model, "degraded")
    } else {
        observability.RecordRequest(string(decision.Backend), req.Model, "success")
    }
    observability.RecordDuration(string(decision.Backend), req.Model, duration)
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)
    s.router.RecordSuccess(string(decision.Backend))

    // Update request log with model/channel/token details
    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = string(decision.Backend)
        logEntry.InputTokens = budget.InputTokens
        logEntry.OutputTokens = outputTokens
        logEntry.TotalTokens = budget.InputTokens + outputTokens
    }

    // Latency tracking for stream
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), time.Duration(duration*float64(time.Second)))
    }

    // Cost tracking for stream
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, budget.InputTokens, outputTokens)
    }
}

func (s *Server) handleNonStreamChat(ctx context.Context, w http.ResponseWriter, provider adapter.Provider, req *adapter.ChatRequest, decision *router.RouteDecision, budget tokenizer.TokenBudget, start time.Time, tenantName string) {
    // Cache lookup
    var cacheKey string
    if s.cache != nil {
        cacheKey = cache.ComputeCacheKey(req.Model, req.Messages, req.Temperature, req.MaxTokens, req.TopP,
            "tenant", tenantName, "tools", req.Tools, "tool_choice", req.ToolChoice, "stop", req.Stop)
        if cached, ok := s.cache.Get(cacheKey); ok {
            slog.Debug("cache hit for non-stream chat", "model", req.Model)
            w.Header().Set("Content-Type", "application/json")
            w.Header().Set("X-Cache", "HIT")
            w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
            w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
            _, _ = w.Write(cached)
            return
        }
    }

    // Retry wrapper
    chatFn := func(ctx context.Context, r *adapter.ChatRequest) (*adapter.ChatResponse, error) {
        return provider.Chat(ctx, r)
    }
    chatFn = middleware.RetryChat(s.cfg.Config.Routing.Retry, chatFn)

    resp, err := chatFn(ctx, req)
    if err != nil {
        slog.Error("chat failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.router.RecordFailure(string(decision.Backend))

        // A4 fix: runtime backend switch fallback — if local fails, try cloud
        if decision.Backend == router.LocalBackend || decision.Backend == router.ClusterBackend {
            fallbackProvider := s.resolveCloudProvider(nil, req, nil)
            if fallbackProvider != nil {
                slog.Info("A4 fallback: switching to cloud after local/cluster failure",
                    "original_backend", string(decision.Backend),
                    "fallback_provider", fallbackProvider.Name(),
                )
                fallbackResp, fallbackErr := fallbackProvider.Chat(ctx, req)
                if fallbackErr == nil {
                    duration := time.Since(start)
                    observability.RecordRequest("cloud", req.Model, "success")
                    observability.RecordDuration("cloud", req.Model, duration.Seconds())
                    observability.RecordTokens("input", "cloud", budget.InputTokens)
                    if fallbackResp.Usage.CompletionTokens > 0 {
                        observability.RecordTokens("output", "cloud", fallbackResp.Usage.CompletionTokens)
                    }
                    s.router.RecordSuccess("cloud")

                    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
                        logEntry.Model = req.Model
                        logEntry.ChannelName = fallbackProvider.Name()
                        logEntry.ChannelType = "cloud"
                        logEntry.InputTokens = budget.InputTokens
                        logEntry.OutputTokens = fallbackResp.Usage.CompletionTokens
                        logEntry.TotalTokens = budget.InputTokens + fallbackResp.Usage.CompletionTokens
                    }
                    if s.latencyTracker != nil {
                        s.latencyTracker.Record(fallbackProvider.Name(), duration)
                    }
                    if s.costTracker != nil {
                        keyCfg := middleware.GetAuthKeyConfig(ctx)
                        keyName := "anonymous"
                        if keyCfg != nil && keyCfg.Name != "" {
                            keyName = keyCfg.Name
                        }
                        s.costTracker.Record(keyName, "cloud", req.Model, budget.InputTokens, fallbackResp.Usage.CompletionTokens)
                    }
                    if s.cache != nil && cacheKey != "" {
                        if respData, marshalErr := json.Marshal(fallbackResp); marshalErr == nil {
                            s.cache.Set(cacheKey, respData)
                        }
                    }
                    w.Header().Set("Content-Type", "application/json")
                    w.Header().Set("X-Route-Decision", fmt.Sprintf("cloud:fallback_from_%s", decision.Backend))
                    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
                    if s.cache != nil {
                        w.Header().Set("X-Cache", "MISS")
                    }
                    _ = json.NewEncoder(w).Encode(fallbackResp)
                    return
                }
                slog.Error("A4 fallback: cloud also failed", "error", fallbackErr)
            }
        }

        http.Error(w, `{"error":{"message":"Chat failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    duration := time.Since(start)
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration.Seconds())
    observability.RecordTokens("input", string(decision.Backend), budget.InputTokens)
    if resp.Usage.CompletionTokens > 0 {
        observability.RecordTokens("output", string(decision.Backend), resp.Usage.CompletionTokens)
    }
    s.router.RecordSuccess(string(decision.Backend))

    // Update request log with model/channel/token details
    if logEntry := middleware.GetRequestLog(ctx); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = string(decision.Backend)
        logEntry.InputTokens = budget.InputTokens
        logEntry.OutputTokens = resp.Usage.CompletionTokens
        logEntry.TotalTokens = budget.InputTokens + resp.Usage.CompletionTokens
    }

    // Latency tracking
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), duration)
    }

    // Cost tracking
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, budget.InputTokens, resp.Usage.CompletionTokens)
    }

    // Cache store
    if s.cache != nil && cacheKey != "" {
        if respData, marshalErr := json.Marshal(resp); marshalErr == nil {
            s.cache.Set(cacheKey, respData)
        }
    }

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    w.Header().Set("X-Token-Budget", fmt.Sprintf("%d", budget.TotalBudget))
    if s.cache != nil {
        w.Header().Set("X-Cache", "MISS")
    }
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    var req adapter.EmbeddingRequest
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    inputLen := len(req.Input)

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    // Route through router engine for embedding request type
    routeReq := &router.RouteRequest{
        Model: req.Model,
        Type:  router.RequestTypeEmbedding,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    slog.Info("embedding route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_count", inputLen,
    )

    // Try cluster sharding for large batch + cluster route
    if decision.Backend == router.ClusterBackend && inputLen > 32 && s.clusterDiscovery != nil {
        if d, ok := s.clusterDiscovery.(*cluster.Discovery); ok {
            resp, err := cluster.ShardEmbedding(ctx, d, &req, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            if err == nil {
                w.Header().Set("Content-Type", "application/json")
                w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
                _ = json.NewEncoder(w).Encode(resp)
                return
            }
            slog.Warn("cluster embedding sharding failed, falling back to single node",
                "error", err,
                "input_count", inputLen,
            )
        }
    }

    // Resolve provider based on routing decision
    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, err := s.pool.GetByBackend("fusion-mlx")
        if err != nil {
            http.Error(w, `{"error":{"message":"Local embedding backend not available"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p

    case router.ClusterBackend:
        if s.clusterDiscovery != nil && decision.NodeID != "" {
            if node, ok := s.clusterDiscovery.GetNode(decision.NodeID); ok {
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            }
        }
        if provider == nil {
            p := s.resolveCloudProvider(decision, nil, w)
            if p == nil {
                return
            }
            provider = p
        }

    default:
        p := s.resolveCloudProvider(decision, nil, w)
        if p == nil {
            return
        }
        provider = p
    }

    embStart := time.Now()
    resp, err := provider.Embedding(ctx, &req)
    if err != nil {
        slog.Error("embedding failed", "provider", provider.Name(), "error", err)
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Embedding failed"}}`, http.StatusBadGateway)
        return
    }

    embDuration := time.Since(embStart)
    s.router.RecordSuccess(string(decision.Backend))

    // Latency + cost tracking for embedding
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), embDuration)
    }
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, inputLen, 0)
    }

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRerank(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    var req adapter.RerankRequest
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20)).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)

    if !middleware.CheckModelAllowlist(r, req.Model) {
        slog.Warn("model not allowed for this key", "model", req.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    routeReq := &router.RouteRequest{
        Model: req.Model,
        Type:  router.RequestTypeRerank,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    slog.Info("rerank route decision",
        "model", req.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
    )

    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, err := s.pool.GetByBackend("fusion-mlx")
        if err != nil {
            http.Error(w, `{"error":{"message":"Local rerank backend not available"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p

    case router.ClusterBackend:
        if s.clusterDiscovery != nil && decision.NodeID != "" {
            if node, ok := s.clusterDiscovery.GetNode(decision.NodeID); ok {
                provider = cluster.NewClusterNodeProvider(node, s.cfg.Config.Routing, s.cfg.Config.Cluster.Master.SharedToken)
            }
        }
        if provider == nil {
            p := s.resolveCloudProvider(decision, nil, w)
            if p == nil {
                return
            }
            provider = p
        }

    default:
        p := s.resolveCloudProvider(decision, nil, w)
        if p == nil {
            return
        }
        provider = p
    }

    start := time.Now()
    resp, err := provider.Rerank(ctx, &req)
    if err != nil {
        slog.Error("rerank failed", "provider", provider.Name(), "error", err)
        observability.RecordRequest(string(decision.Backend), req.Model, "error")
        s.router.RecordFailure(string(decision.Backend))
        http.Error(w, `{"error":{"message":"Rerank failed"}}`, http.StatusBadGateway)
        return
    }

    duration := time.Since(start)
    observability.RecordRequest(string(decision.Backend), req.Model, "success")
    observability.RecordDuration(string(decision.Backend), req.Model, duration.Seconds())
    s.router.RecordSuccess(string(decision.Backend))

    // Latency + cost tracking for rerank
    if s.latencyTracker != nil {
        s.latencyTracker.Record(provider.Name(), duration)
    }
    if s.costTracker != nil {
        keyCfg := middleware.GetAuthKeyConfig(ctx)
        keyName := "anonymous"
        if keyCfg != nil && keyCfg.Name != "" {
            keyName = keyCfg.Name
        }
        s.costTracker.Record(keyName, string(decision.Backend), req.Model, len(req.Documents), 0)
    }

    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Route-Decision", fmt.Sprintf("%s:%s", decision.Backend, decision.Reason))
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
    if !s.cfg.Config.Realtime.Enabled || s.realtimeProxy == nil {
        http.Error(w, `{"error":{"message":"Realtime API not enabled","type":"invalid_request"}}`, http.StatusNotFound)
        return
    }

    backendURL := s.cfg.Config.Realtime.BackendURL
    if backendURL == "" {
        http.Error(w, `{"error":{"message":"Realtime backend not configured","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    apiKey := s.cfg.Config.Realtime.APIKey

    slog.Info("realtime: incoming connection",
        "client", r.RemoteAddr,
        "backend", backendURL,
    )

    observability.RecordRouteDecision("realtime", "websocket_proxy")
    s.realtimeProxy.UpgradeAndProxy(w, r, backendURL, apiKey)
}

// listModelsPerProviderTimeout bounds each provider's ListModels call so a
// single unreachable cloud backend cannot stall the /v1/models endpoint.
const listModelsPerProviderTimeout = 3 * time.Second

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
    ctx := adapter.WithFusionHeaders(r.Context(), r)
    snap := config.SnapshotFromContext(ctx)

    providerNames := s.pool.ListProviders()

    // mode=local: only return models from local providers (fusion-mlx/kb/hub),
    // so a downstream health check never waits on slow cloud backends.
    if snap.Config.Routing.Mode == "local" {
        localOnly := make([]string, 0, len(providerNames))
        for _, name := range providerNames {
            if s.pool.IsLocalProvider(name) {
                localOnly = append(localOnly, name)
            }
        }
        slog.Debug("mode=local, listing only local providers", "total", len(providerNames), "local", len(localOnly))
        providerNames = localOnly
    }

    models := s.listModelsConcurrent(ctx, providerNames)

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "object": "list",
        "data":   models,
    })
}

// listModelsConcurrent fans out ListModels across providers with a per-provider
// timeout. Failed or timed-out providers are skipped (logged at Warn) so the
// fast local models are never blocked by a slow/unreachable cloud backend, but
// a misconfigured/route-rejected backend still surfaces visibly (#29, Rule 12).
func (s *Server) listModelsConcurrent(ctx context.Context, names []string) []adapter.ModelInfo {
    type providerResult struct {
        name   string
        models []adapter.ModelInfo
        err    error
    }

    results := make(chan providerResult, len(names))
    var wg sync.WaitGroup

    for _, name := range names {
        wg.Add(1)
        go func(name string) {
            defer wg.Done()
            provider, ok := s.pool.Get(name)
            if !ok {
                results <- providerResult{name: name, err: fmt.Errorf("provider not found")}
                return
            }
            pCtx, cancel := context.WithTimeout(ctx, listModelsPerProviderTimeout)
            defer cancel()
            providerModels, err := provider.ListModels(pCtx)
            results <- providerResult{name: name, models: providerModels, err: err}
        }(name)
    }

    wg.Wait()
    close(results)

    models := make([]adapter.ModelInfo, 0)
    for res := range results {
        if res.err != nil {
            slog.Warn("list models failed for provider, skipping", "provider", res.name, "error", res.err)
            continue
        }
        for _, m := range res.models {
            m.AvailableBackends = []string{res.name}
            models = append(models, m)
        }
    }
    return models
}

// resolveDefaultModel backfills an empty request model (issue #28).
// Priority: routing.default_model config value, then the first model returned
// by a local provider (fusion-mlx/kb/hub) via auto-discovery. Returns "" if
// nothing usable is found, leaving the original empty model to flow on.
func (s *Server) resolveDefaultModel(ctx context.Context) (string, error) {
    if dm := strings.TrimSpace(s.cfg.Config.Routing.DefaultModel); dm != "" {
        slog.Debug("using configured default_model", "model", dm)
        return dm, nil
    }

    // Auto-discover: only query local providers so a slow/unreachable cloud
    // backend never blocks the request (mirrors the /v1/models fix in #21).
    localNames := make([]string, 0)
    for _, name := range s.pool.ListProviders() {
        if s.pool.IsLocalProvider(name) {
            localNames = append(localNames, name)
        }
    }
    if len(localNames) == 0 {
        return "", fmt.Errorf("no local provider registered for default-model auto-discovery")
    }

    models := s.listModelsConcurrent(ctx, localNames)
    if len(models) == 0 {
        return "", fmt.Errorf("default_model not configured and no local model loaded")
    }
    slog.Debug("auto-discovered default model", "model", models[0].ID, "candidates", len(models))
    return models[0].ID, nil
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req adapter.ImageRequest
    if err := json.Unmarshal(body, &req); err != nil {
        slog.Error("invalid json in image request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    if req.N <= 0 {
        req.N = 1
    }

    cloudBackend := s.cfg.Config.Routing.Fallback.CloudDefault
    if cloudBackend == "" {
        cloudBackend = "openai"
    }

    provider, ok := s.pool.Get(cloudBackend)
    if !ok {
        http.Error(w, `{"error":{"message":"Image generation backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    imgProvider, ok := provider.(interface {
        Images(ctx context.Context, req *adapter.ImageRequest) (*adapter.ImageResponse, error)
    })
    if !ok {
        http.Error(w, `{"error":{"message":"Selected backend does not support image generation","type":"server_error"}}`, http.StatusBadRequest)
        return
    }

    start := time.Now()
    resp, err := imgProvider.Images(r.Context(), &req)
    if err != nil {
        slog.Error("image generation failed", "provider", provider.Name(), "error", err)
        http.Error(w, `{"error":{"message":"Image generation failed","type":"server_error"}}`, http.StatusBadGateway)
        return
    }

    if logEntry := middleware.GetRequestLog(r.Context()); logEntry != nil {
        logEntry.Model = req.Model
        logEntry.ChannelName = provider.Name()
        logEntry.ChannelType = "cloud"
        logEntry.StatusCode = http.StatusOK
    }

    slog.Info("image generation completed",
        "model", req.Model,
        "provider", provider.Name(),
        "latency_ms", time.Since(start).Milliseconds(),
    )

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
    results := s.pool.HealthCheckAll(r.Context())
    allHealthy := true
    for _, err := range results {
        if err != nil {
            allHealthy = false
            break
        }
    }


    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "status":   healthStatus(allHealthy),
        "backends": results,
    })
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, "ok")
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
    ctx := adapter.WithFusionHeaders(r.Context(), r)
    localReady := s.router.CircuitBreakerState("local") != router.StateOpen
    localReasons := []string{}

    if localReady {
        if p, ok := s.pool.Get("fusion-mlx"); ok {
            if err := p.HealthCheck(ctx); err != nil {
                localReady = false
                localReasons = append(localReasons, "health_check_failed")
            }
        } else {
            localReady = false
            localReasons = append(localReasons, "no_local_provider")
        }
    } else {
        localReasons = append(localReasons, "circuit_breaker_open")
    }

    // GPU memory availability check
    if localReady {
        hwMetrics := s.hwCollector.Latest()
        if hwMetrics.GPUAllocMemory > 0 && hwMetrics.GPUInUseMemory > 0 {
            gpuAvail := hwMetrics.GPUAllocMemory - hwMetrics.GPUInUseMemory
            if gpuAvail < uint64(float64(hwMetrics.GPUAllocMemory)*0.1) {
                localReady = false
                localReasons = append(localReasons, "gpu_memory_critical")
            }
        }
        // Inference queue depth check
        if hwMetrics.MLXInferenceQueueDepth > 0 {
            maxQueue := s.cfg.Config.Routing.LocalPriority.MaxConcurrent
            if maxQueue > 0 && hwMetrics.MLXInferenceQueueDepth >= maxQueue {
                localReady = false
                localReasons = append(localReasons, "inference_queue_full")
            }
        }
        // Recent success rate check
        successRate := observability.SuccessRate("local")
        if successRate < 0.5 {
            localReady = false
            localReasons = append(localReasons, fmt.Sprintf("low_success_rate=%.2f", successRate))
        }
    }

    cloudReady := true
    if s.cfg.Config.Routing.Fallback.CloudDefault != "" {
        if p, ok := s.pool.Get(s.cfg.Config.Routing.Fallback.CloudDefault); ok {
            if err := p.HealthCheck(ctx); err != nil {
                cloudReady = false
            }
        }
    }

    if !localReady && !cloudReady {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusServiceUnavailable)
        _ = json.NewEncoder(w).Encode(map[string]interface{}{
            "status":        "not_ready",
            "local_reasons": localReasons,
        })
        return
    }

    mode := "full"
    if !localReady {
        mode = "degraded"
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "status":        "ready",
        "mode":          mode,
        "local_reasons": localReasons,
    })
}

func (s *Server) handleLivez(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, "alive")
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
    hwMetrics := s.hwCollector.Latest()
    total, local, cloud := observability.Stats()

    localHitRate := 0.0
    if total > 0 {
        localHitRate = float64(local) / float64(total)
    }

    status := map[string]interface{}{
        "status":         "ok",
        "uptime_seconds": int(time.Since(s.startTime).Seconds()),
        "config_version": s.cfg.Version,
        "backends":       s.buildBackendStatus(r.Context()),
        "hardware":       s.buildHardwareStatus(hwMetrics),
        "circuit_breakers": map[string]string{
            "local":  s.router.CircuitBreakerState("local").String(),
            "cloud":  s.router.CircuitBreakerState("cloud").String(),
            "cluster": s.router.CircuitBreakerState("cluster").String(),
        },
        "stats": map[string]interface{}{
            "total_requests": total,
            "local_requests": local,
            "cloud_requests": cloud,
            "local_hit_rate": localHitRate,
        },
    }

    if s.clusterDiscovery != nil {
        status["cluster"] = s.clusterDiscovery.Status()
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(status)
}

func (s *Server) handleAdminGC(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }

    provider, ok := s.pool.Get("fusion-mlx")
    if !ok {
        http.Error(w, `{"error":{"message":"fusion-mlx not configured"}}`, http.StatusNotFound)
        return
    }

    mlxProvider, ok := provider.(*adapter.FusionMLXProvider)
    if !ok {
        http.Error(w, `{"error":{"message":"provider is not fusion-mlx"}}`, http.StatusInternalServerError)
        return
    }

    if mlxProvider.InFlight() != 0 {
        go mlxProvider.TriggerGCWhenIdle()
        w.WriteHeader(http.StatusAccepted)
        fmt.Fprintf(w, `{"status":"gc_queued","in_flight":%d,"message":"GC will execute when in-flight requests reach zero"}`, mlxProvider.InFlight())
        return
    }

    go mlxProvider.SafeGC()
    w.WriteHeader(http.StatusOK)
    fmt.Fprint(w, `{"status":"gc_triggered"}`)
}

func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    snap, err := config.Reload(s.cfgPath)
    if err != nil {
        slog.Error("admin config reload failed", "path", s.cfgPath, "error", err)
        http.Error(w, fmt.Sprintf(`{"error":{"message":"config reload failed: %s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
        return
    }
    slog.Info("admin config reload succeeded", "path", s.cfgPath, "version", snap.Version)
    writeJSON(w, http.StatusOK, map[string]any{
        "status":  "reloaded",
        "version": snap.Version,
        "path":    s.cfgPath,
    })
}

func (s *Server) buildBackendStatus(ctx context.Context) map[string]interface{} {
    results := s.pool.HealthCheckAll(ctx)
    status := make(map[string]interface{})

    for name, err := range results {
        entry := map[string]interface{}{
            "healthy": err == nil,
        }
        if err != nil {
            entry["error"] = err.Error()
        }
        if name == "fusion-mlx" {
            if p, ok := s.pool.Get(name); ok {
                if mlx, ok := p.(*adapter.FusionMLXProvider); ok {
                    entry["in_flight"] = mlx.InFlight()
                }
            }
        }
        status[name] = entry
    }

    return status
}

func (s *Server) buildHardwareStatus(m hardware.HardwareMetrics) map[string]interface{} {
    return map[string]interface{}{
        "memory_used_ratio":       m.MemoryUsedRatio,
        "gpu_device_utilization":  m.GPUDeviceUtilization,
        "gpu_in_use_memory_bytes": m.GPUInUseMemory,
        "mlx_active_memory_bytes": m.MLXActiveMemory,
        "swap_page_in_rate":       m.SwapPageInRate,
        "collection_error":        errorString(m.CollectionError),
    }
}

func (s *Server) handleCost(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    if s.costTracker == nil {
        http.Error(w, `{"error":{"message":"Cost tracking not enabled","type":"invalid_request"}}`, http.StatusNotFound)
        return
    }

    keyName := r.URL.Query().Get("key")
    var summary *cost.CostSummary
    if keyName != "" {
        summary = s.costTracker.SummaryByKey(keyName)
    } else {
        summary = s.costTracker.Summary()
    }

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(summary)
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }

    // Legacy /v1/completions: convert to chat format and re-use chat handler
    var legacyReq struct {
        Model       string   `json:"model"`
        Prompt      string   `json:"prompt"`
        Temperature *float64 `json:"temperature,omitempty"`
        MaxTokens   *int     `json:"max_tokens,omitempty"`
        Stream      bool     `json:"stream"`
        Stop        []string `json:"stop,omitempty"`
        TopP        *float64 `json:"top_p,omitempty"`
    }
    if err := json.NewDecoder(r.Body).Decode(&legacyReq); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    slog.Info("legacy completions request, converting to chat format", "model", legacyReq.Model)

    // Convert prompt to single user message
    chatReq := adapter.ChatRequest{
        Model: legacyReq.Model,
        Messages: []adapter.ChatMessage{
            {Role: "user", Content: legacyReq.Prompt},
        },
        Temperature: legacyReq.Temperature,
        MaxTokens:   legacyReq.MaxTokens,
        Stream:      legacyReq.Stream,
        Stop:        legacyReq.Stop,
        TopP:        legacyReq.TopP,
    }

    // Re-encode and forward to chat completions handler via internal call
    ctx := adapter.WithFusionHeaders(r.Context(), r)

    if !middleware.CheckModelAllowlist(r, chatReq.Model) {
        slog.Warn("model not allowed for this key", "model", chatReq.Model)
        http.Error(w, `{"error":{"message":"Model not allowed for this API key","type":"auth_error"}}`, http.StatusForbidden)
        return
    }

    textContent := legacyReq.Prompt
    inputTokens, err := s.tokEngine.CountTokens(ctx, textContent)
    if err != nil {
        slog.Error("token counting failed", "error", err)
        inputTokens = len(textContent) / 4
    }

    budget := s.tokEngine.EstimateBudget(inputTokens, chatReq.MaxTokens, chatReq.Model, chatReq.Tools != nil, chatReq.Stream)
    ctx = tokenizer.WithTokenBudget(ctx, budget)

    routeReq := &router.RouteRequest{
        Model:  chatReq.Model,
        Text:   textContent,
        Stream: chatReq.Stream,
    }
    decision := s.router.Decide(ctx, routeReq)
    observability.RecordRouteDecision(string(decision.Backend), decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    slog.Info("route decision (completions)",
        "model", chatReq.Model,
        "backend", string(decision.Backend),
        "reason", decision.Reason,
        "input_tokens", budget.InputTokens,
    )

    var provider adapter.Provider
    switch decision.Backend {
    case router.LocalBackend:
        p, ok := s.pool.Get("fusion-mlx")
        if !ok {
            http.Error(w, `{"error":{"message":"Local backend not available","type":"server_error"}}`, http.StatusServiceUnavailable)
            return
        }
        provider = p
    default:
        provider = s.resolveCloudProvider(decision, &chatReq, w)
        if provider == nil {
            return
        }
    }

    start := time.Now()

    tenant := "anonymous"
    if kc := middleware.GetAuthKeyConfig(r.Context()); kc != nil && kc.Name != "" {
        tenant = kc.Name
    }
    if chatReq.Stream {
        s.handleStreamChat(ctx, w, provider, &chatReq, decision, budget, start)
    } else {
        s.handleNonStreamChat(ctx, w, provider, &chatReq, decision, budget, start, tenant)
    }
}

func extractTextContent(messages []adapter.ChatMessage) string {
    var sb string
    for _, msg := range messages {
        if str, ok := msg.Content.(string); ok {
            sb += str + " "
        }
    }
    return sb
}

func healthStatus(healthy bool) string {
    if healthy {
        return "ok"
    }
    return "degraded"
}

func errorString(err error) string {
    if err == nil {
        return ""
    }
    return err.Error()
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    // R2 fix: use MaxBytesReader to limit request body size
    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var antReq adapter.AnthropicRequest
    if err := json.Unmarshal(body, &antReq); err != nil {
        slog.Error("invalid json in anthropic messages request", "error", err)
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: antReq.Model, Stream: antReq.Stream})
    slog.Info("anthropic messages route decision", "model", antReq.Model, "backend", string(decision.Backend), "reason", decision.Reason)

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    var provider adapter.Provider
    if decision.Backend == router.LocalBackend {
        provider, _ = s.pool.Get("fusion-mlx")
    } else {
        provider = s.resolveCloudProvider(decision, nil, w)
        if provider == nil { return }
        // Apply model alias mapping (e.g. claude-opus-4-7 -> glm5.2) before
        // forwarding. Without this, SDK-supplied aliases are sent raw to the
        // cloud backend which rejects them with 400 -> 502 ("response stopped
        // arriving" in claude code). resolveCloudProvider cannot do this for
        // the anthropic path because it mutates *ChatRequest, not
        // *AnthropicRequest.
        if mapped := s.applyCloudModelMapping(antReq.Model, provider.Name()); mapped != antReq.Model {
            antReq.Model = mapped
        }
    }
    if provider == nil {
        http.Error(w, `{"error":{"message":"No provider available","type":"server_error"}}`, http.StatusServiceUnavailable)
        return
    }

    if msgProv, ok := provider.(adapter.MessagesProvider); ok {
        if antReq.Stream {
            s.handleStreamAnthropicMessages(ctx, w, msgProv, &antReq)
        } else {
            s.handleNonStreamAnthropicMessages(ctx, w, msgProv, &antReq)
        }
    } else {
        chatReq := adapter.AnthropicToOpenAIChatRequest(&antReq)
        chatReq.Stream = antReq.Stream
        start := time.Now()
        budget := tokenizer.TokenBudget{InputTokens: 0, TotalBudget: antReq.MaxTokens}
        tenant := "anonymous"
        if kc := middleware.GetAuthKeyConfig(r.Context()); kc != nil && kc.Name != "" {
            tenant = kc.Name
        }
        if antReq.Stream {
            s.handleStreamChat(ctx, w, provider, chatReq, decision, budget, start)
        } else {
            s.handleNonStreamChat(ctx, w, provider, chatReq, decision, budget, start, tenant)
        }
    }
}

// writeMessagesError surfaces an upstream error to the client preserving the
// upstream HTTP status code and request-id (issue #40) so fusion-code's error
// bridge (isApiErrorLike) can identify failures. Non-MessagesHTTPError errors
// fall back to 502 as before.
func (s *Server) writeMessagesError(w http.ResponseWriter, err error) {
    status := http.StatusBadGateway
    body := fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error())
    if httpErr, ok := err.(*adapter.MessagesHTTPError); ok {
        status = httpErr.StatusCode
        if httpErr.Body != "" {
            body = httpErr.Body
        } else {
            body = fmt.Sprintf(`{"error":{"message":"upstream status %d","type":"api_error"}}`, httpErr.StatusCode)
        }
        if httpErr.RequestID != "" {
            w.Header().Set("x-request-id", httpErr.RequestID)
        }
    }
    slog.Error("anthropic messages upstream error", "error", err, "status", status)
    http.Error(w, body, status)
}

func (s *Server) handleNonStreamAnthropicMessages(ctx context.Context, w http.ResponseWriter, p adapter.MessagesProvider, req *adapter.AnthropicRequest) {
    // Internal stream + aggregate: reasoning upstreams (glm5.2 via LiteLLM)
    // withhold non-stream response headers until full generation completes,
    // tripping Client.Timeout / client-cancel 502s. Stream path has a 2s TTFB,
    // so we stream upstream and aggregate events into a non-stream response.
    // We force stream=true on a local copy so the original request struct is
    // untouched (the same req may be inspected elsewhere).
    streamReq := *req
    streamReq.Stream = true
    ch, err := p.StreamMessages(ctx, &streamReq)
    if err != nil {
        s.writeMessagesError(w, err)
        return
    }
    resp, err := adapter.AggregateAnthropicStreamEvents(ch)
    if err != nil {
        s.writeMessagesError(w, err)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleStreamAnthropicMessages(ctx context.Context, w http.ResponseWriter, p adapter.MessagesProvider, req *adapter.AnthropicRequest) {
    ch, err := p.StreamMessages(ctx, req)
    if err != nil {
        s.writeMessagesError(w, err)
        return
    }

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no")
    flusher, _ := w.(http.Flusher)

    // sawMessageStop tracks whether the upstream already emitted a
    // message_stop. We must not append a second one: the Anthropic SDK
    // finalizes the message on the first message_stop, and a duplicate
    // (especially a malformed data:{} with no "type") confuses the client
    // ("Content block not found" / stream-ended errors, issue #46).
    sawMessageStop := false
    for event := range ch {
        if event.Type == "message_stop" {
            sawMessageStop = true
        }
        data, _ := json.Marshal(event)
        fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
        if flusher != nil { flusher.Flush() }
    }
    // Client cancelled mid-stream (context canceled): the upstream goroutine
    // closed the channel early, possibly with content blocks still open.
    // Emitting a terminal message_stop now would hand the SDK an unmatched
    // block ("Content block not found"). The client already gave up, so just
    // stop writing — do NOT synthesize a closing event.
    if ctx.Err() != nil {
        slog.Warn("anthropic stream client canceled, suppressing synthetic message_stop", "error", ctx.Err())
        return
    }
    // Upstream closed the channel without a message_stop (e.g. error/truncation):
    // synthesize a well-formed one so the SDK can finalize cleanly. Only when
    // we have not already forwarded one.
    if !sawMessageStop {
        slog.Warn("anthropic stream ended without message_stop, synthesizing terminal event")
        fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
        if flusher != nil { flusher.Flush() }
    }
}

func (s *Server) handleTranscriptions(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    if err := r.ParseMultipartForm(32 << 20); err != nil {
        slog.Error("failed to parse multipart form", "error", err)
        http.Error(w, `{"error":{"message":"Failed to parse multipart form","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer func() { _ = r.MultipartForm.RemoveAll() }()

    model := r.FormValue("model")
    if model == "" { model = "whisper-1" }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: model})

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type TranscriptionProvider interface {
        Transcription(ctx context.Context, r *http.Request) (json.RawMessage, error)
    }
    if tp, ok := provider.(TranscriptionProvider); ok {
        result, err := tp.Transcription(ctx, r)
        if err != nil {
            slog.Error("transcription failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write(result)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support transcription","type":"invalid_request"}}`, http.StatusBadRequest)
}

func (s *Server) handleSpeech(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    // R2 fix: use MaxBytesReader to limit request body size
    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req struct {
        Model string `json:"model"`
        Input string `json:"input"`
        Voice string `json:"voice"`
    }
    if err := json.Unmarshal(body, &req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: req.Model})

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type SpeechProvider interface {
        Speech(ctx context.Context, reqBody []byte) ([]byte, string, error)
    }
    if sp, ok := provider.(SpeechProvider); ok {
        audioData, contentType, err := sp.Speech(ctx, body)
        if err != nil {
            slog.Error("speech synthesis failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", contentType)
        _, _ = w.Write(audioData)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support speech","type":"invalid_request"}}`, http.StatusBadRequest)
}

func (s *Server) handleModeration(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    // R2 fix: use MaxBytesReader to limit request body size
    maxBodySize := int64(s.cfg.Config.Server.MaxRequestBodySize)
    if maxBodySize <= 0 {
        maxBodySize = 5 << 20
    }
    body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
    if err != nil {
        http.Error(w, `{"error":{"message":"Failed to read request","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    defer r.Body.Close()

    var req struct {
        Model string      `json:"model,omitempty"`
        Input interface{} `json:"input"`
    }
    if err := json.Unmarshal(body, &req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }

    ctx := adapter.WithFusionHeaders(r.Context(), r)
    decision := s.router.Decide(ctx, &router.RouteRequest{Model: req.Model})

    if !s.checkBackendAccess(w, r, string(decision.Backend)) {
        return
    }

    provider := s.resolveCloudProvider(decision, nil, w)
    if provider == nil { return }

    type ModerationProvider interface {
        Moderation(ctx context.Context, reqBody []byte) (json.RawMessage, error)
    }
    if mp, ok := provider.(ModerationProvider); ok {
        result, err := mp.Moderation(ctx, body)
        if err != nil {
            slog.Error("moderation failed", "error", err)
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"api_error"}}`, err.Error()), http.StatusBadGateway)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        _, _ = w.Write(result)
        return
    }
    http.Error(w, `{"error":{"message":"Provider does not support moderation","type":"invalid_request"}}`, http.StatusBadRequest)
}

func (s *Server) withAdminOnly(handler http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if !middleware.IsAdmin(r.Context()) {
            http.Error(w, `{"error":{"message":"Admin access required","type":"rbac_error"}}`, http.StatusForbidden)
            return
        }
        s.withMiddleware(handler)(w, r)
    }
}

func (s *Server) handleAdminTeams(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        teams, err := s.store.ListTeams()
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
            return
        }
        writeJSON(w, http.StatusOK, teams)
    case http.MethodPost:
        var team store.Team
        if err := json.NewDecoder(r.Body).Decode(&team); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if team.ID == "" {
            http.Error(w, `{"error":{"message":"Team ID is required","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if err := s.store.CreateTeam(&team); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusConflict)
            return
        }
        writeJSON(w, http.StatusCreated, team)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleAdminTeamsCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/admin/teams/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Team ID required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        team, err := s.store.GetTeam(id)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, team)
    case http.MethodPut:
        var team store.Team
        if err := json.NewDecoder(r.Body).Decode(&team); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        team.ID = id
        if err := s.store.UpdateTeam(&team); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, team)
    case http.MethodDelete:
        if err := s.store.DeleteTeam(id); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
            return
        }
        w.WriteHeader(http.StatusNoContent)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleAdminOrgs(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        orgs, err := s.store.ListOrgs()
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
            return
        }
        writeJSON(w, http.StatusOK, orgs)
    case http.MethodPost:
        var org store.Organization
        if err := json.NewDecoder(r.Body).Decode(&org); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if org.ID == "" {
            http.Error(w, `{"error":{"message":"Organization ID is required","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if err := s.store.CreateOrg(&org); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusConflict)
            return
        }
        writeJSON(w, http.StatusCreated, org)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleAdminOrgsCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/admin/orgs/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Organization ID required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        org, err := s.store.GetOrg(id)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, org)
    case http.MethodDelete:
        if err := s.store.DeleteOrg(id); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
            return
        }
        w.WriteHeader(http.StatusNoContent)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(v); err != nil {
        slog.Error("failed to encode json response", "error", err)
    }
}

func (s *Server) handleBatches(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodPost:
        var req struct {
            Requests         []store.BatchRequest `json:"requests"`
            Endpoint         string               `json:"endpoint"`
            CompletionWindow string               `json:"completion_window"`
        }
        if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        b, err := s.store.CreateBatch(req.Requests, req.Endpoint, req.CompletionWindow)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
            return
        }
        writeJSON(w, http.StatusOK, b)
    case http.MethodGet:
        batches, err := s.store.ListBatches()
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
            return
        }
        writeJSON(w, http.StatusOK, batches)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleBatchCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/v1/batches/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Batch ID required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        b, err := s.store.GetBatch(id)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, b)
    case http.MethodPost:
        if strings.HasSuffix(r.URL.Path, "/cancel") {
            b, err := s.store.CancelBatch(id)
            if err != nil {
                http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
                return
            }
            writeJSON(w, http.StatusOK, b)
            return
        }
        http.Error(w, `{"error":{"message":"Unknown action","type":"invalid_request"}}`, http.StatusBadRequest)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleConnectorList(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    connectors := s.connectorRegistry.ListConnectors()
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "connectors": connectors,
    })
}

func (s *Server) handleConnectorTest(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    var req connector.ActionRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }
    req.TestMode = true
    resp, _ := s.connectorRegistry.ExecuteAction(r.Context(), &req)
    writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnectorAction(w http.ResponseWriter, r *http.Request) {
    // Path: /gateway/v1/connector/{connectorKey}/action/{actionKey}
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    path := strings.TrimPrefix(r.URL.Path, "/gateway/v1/connector/")
    parts := strings.SplitN(path, "/", 3)
    if len(parts) < 3 || parts[1] != "action" {
        http.Error(w, `{"error":{"message":"Invalid path, expected /gateway/v1/connector/{key}/action/{action}"}}`, http.StatusBadRequest)
        return
    }
    connectorKey := parts[0]
    actionKey := parts[2]

    var body struct {
        Params       map[string]interface{} `json:"params"`
        ConnectionID string                 `json:"connectionId"`
    }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }

    // Also check header for connection ID
    connectionID := body.ConnectionID
    if v := r.Header.Get("X-Fusion-Connection-Id"); v != "" {
        connectionID = v
    }

    req := &connector.ActionRequest{
        ConnectorKey: connectorKey,
        ActionKey:    actionKey,
        ConnectionID: connectionID,
        Params:       body.Params,
    }
    resp, _ := s.connectorRegistry.ExecuteAction(r.Context(), req)
    writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnectionList(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        connections := s.connectorRegistry.ListConnections()
        writeJSON(w, http.StatusOK, map[string]interface{}{
            "connections": connections,
        })
    case http.MethodPost:
        var conn connector.Connection
        if err := json.NewDecoder(r.Body).Decode(&conn); err != nil {
            http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
            return
        }
        if err := s.connectorRegistry.CreateConnection(&conn); err != nil {
            writeJSON(w, http.StatusConflict, map[string]interface{}{
                "success": false,
                "code":    connector.ErrValidation,
                "message": err.Error(),
            })
            return
        }
        writeJSON(w, http.StatusCreated, conn)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleConnectionCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/gateway/v1/connection/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Connection ID required"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        conn, err := s.connectorRegistry.GetConnection(id)
        if err != nil {
            writeJSON(w, http.StatusNotFound, map[string]interface{}{
                "success": false,
                "code":    connector.ErrNotFound,
                "message": err.Error(),
            })
            return
        }
        writeJSON(w, http.StatusOK, conn)
    case http.MethodDelete:
        if err := s.connectorRegistry.DeleteConnection(id); err != nil {
            writeJSON(w, http.StatusNotFound, map[string]interface{}{
                "success": false,
                "code":    connector.ErrNotFound,
                "message": err.Error(),
            })
            return
        }
        w.WriteHeader(http.StatusNoContent)
    case http.MethodPost:
        if strings.HasSuffix(r.URL.Path, "/refresh") {
            if err := s.connectorRegistry.RefreshConnection(r.Context(), id); err != nil {
                writeJSON(w, http.StatusBadRequest, map[string]interface{}{
                    "success": false,
                    "code":    connector.ErrAuthExpired,
                    "message": err.Error(),
                })
                return
            }
            writeJSON(w, http.StatusOK, map[string]interface{}{
                "success": true,
                "code":    0,
                "message": "refreshed",
            })
            return
        }
        http.Error(w, `{"error":{"message":"Unknown action"}}`, http.StatusBadRequest)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) evictOAuth2States() {
    ticker := time.NewTicker(10 * time.Minute)
    defer ticker.Stop()
    for range ticker.C {
        s.oauth2StatesMu.Lock()
        now := time.Now()
        for state, issuedAt := range s.oauth2States {
            if now.Sub(issuedAt) > 10*time.Minute {
                delete(s.oauth2States, state)
            }
        }
        s.oauth2StatesMu.Unlock()
    }
}

func (s *Server) handleOAuth2Authorize(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, `{"error":{"message":"Method not allowed"}}`, http.StatusMethodNotAllowed)
        return
    }
    var req struct {
        ConnectorKey string `json:"connectorKey"`
        State        string `json:"state"`
    }
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, `{"error":{"message":"Invalid JSON"}}`, http.StatusBadRequest)
        return
    }
    if req.ConnectorKey == "" {
        http.Error(w, `{"error":{"message":"connectorKey is required"}}`, http.StatusBadRequest)
        return
    }
    if req.State == "" {
        b := make([]byte, 16)
        if _, err := rand.Read(b); err != nil {
            slog.Error("generate state failed", "error", err)
            http.Error(w, `{"error":{"message":"internal error"}}`, http.StatusInternalServerError)
            return
        }
        req.State = hex.EncodeToString(b)
    }
    // Enforce length limit on client-supplied state to prevent unbounded map growth
    if len(req.State) > 64 {
        http.Error(w, `{"error":{"message":"state parameter too long"}}`, http.StatusBadRequest)
        return
    }
    s.oauth2StatesMu.Lock()
    s.oauth2States[req.State] = time.Now().UTC()
    s.oauth2StatesMu.Unlock()
    authURL, err := s.connectorRegistry.OAuth2().AuthorizationURL(req.ConnectorKey, req.State)
    if err != nil {
        writeJSON(w, http.StatusBadRequest, map[string]interface{}{
            "success": false,
            "message": err.Error(),
        })
        return
    }
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "authorization_url": authURL,
        "state":             req.State,
    })
}

func (s *Server) handleOAuth2Callback(w http.ResponseWriter, r *http.Request) {
    code := r.URL.Query().Get("code")
    state := r.URL.Query().Get("state")
    connectorKey := r.URL.Query().Get("connector_key")
    if code == "" || connectorKey == "" {
        http.Error(w, `{"error":{"message":"missing code or connector_key"}}`, http.StatusBadRequest)
        return
    }
    // CSRF: validate state matches a previously-issued one
    if state == "" {
        http.Error(w, `{"error":{"message":"missing state parameter"}}`, http.StatusBadRequest)
        return
    }
    s.oauth2StatesMu.Lock()
    issuedAt, ok := s.oauth2States[state]
    if ok {
        delete(s.oauth2States, state)
    }
    s.oauth2StatesMu.Unlock()
    if !ok {
        http.Error(w, `{"error":{"message":"invalid or expired state parameter"}}`, http.StatusBadRequest)
        return
    }
    if time.Since(issuedAt) > 10*time.Minute {
        http.Error(w, `{"error":{"message":"state parameter expired"}}`, http.StatusBadRequest)
        return
    }

    slog.Info("oauth2 callback received", "connector", connectorKey, "state", state)
    tokenResp, err := s.connectorRegistry.OAuth2().ExchangeCode(r.Context(), connectorKey, code)
    if err != nil {
        writeJSON(w, http.StatusBadRequest, map[string]interface{}{
            "success": false,
            "message": err.Error(),
        })
        return
    }
    encAccess, err := s.connectorRegistry.OAuth2().EncryptToken(tokenResp.AccessToken)
    if err != nil {
        slog.Error("encrypt access token failed, aborting callback", "error", err)
        writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
            "success": false,
            "message": "token encryption failed",
        })
        return
    }
    encRefresh, err := s.connectorRegistry.OAuth2().EncryptToken(tokenResp.RefreshToken)
    if err != nil {
        slog.Error("encrypt refresh token failed, aborting callback", "error", err)
        writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
            "success": false,
            "message": "token encryption failed",
        })
        return
    }
    conn := &connector.Connection{
        ID:                     fmt.Sprintf("conn-%s-%d", connectorKey, time.Now().UnixNano()),
        ConnectorKey:           connectorKey,
        AuthType:               connector.AuthTypeOAuth2,
        Status:                 "active",
        EncryptedAccessToken:   encAccess,
        EncryptedRefreshToken:  encRefresh,
    }
    if tokenResp.ExpiresIn > 0 {
        expiry := time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
        conn.TokenExpiry = &expiry
    }
    if err := s.connectorRegistry.CreateConnection(conn); err != nil {
        writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
            "success": false,
            "message": err.Error(),
        })
        return
    }
    writeJSON(w, http.StatusOK, map[string]interface{}{
        "success":       true,
        "connection_id": conn.ID,
        "expires_in":    tokenResp.ExpiresIn,
    })
}
