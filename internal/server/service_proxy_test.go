package server

// service_proxy_test.go — /svc/<name>/ platform-service proxy plus the #370
// Prefix alias mount: business clients reach fusion-memory at
// POST /v1/memory/{commit,retrieve,delete_scope} with their FUSION_CLIENT_KEY;
// the gateway injects the memory service credential upstream (never the
// caller's own token) and degrades with the structured A5 envelope on outage.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fusion-gateway/fusion-gateway/internal/config"
	"github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// serviceProxyUpstream stands in for a Bearer-enforced platform service
// (fm-server refuses requests without its private key): it records the
// Authorization header, path, and body of each hit and 401s anything that
// does not carry the expected gateway-held token.
func serviceProxyUpstream(t *testing.T, token string) (*httptest.Server, *[]string, *[]string, *[]string) {
	t.Helper()
	authSeen, pathSeen, bodySeen := &[]string{}, &[]string{}, &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*authSeen = append(*authSeen, r.Header.Get("Authorization"))
		*pathSeen = append(*pathSeen, r.URL.Path)
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		*bodySeen = append(*bodySeen, string(buf[:n]))
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	return srv, authSeen, pathSeen, bodySeen
}

// serviceProxyMux builds a Server with the given service routes and mounts
// them on a fresh mux, mirroring the production registration path.
func serviceProxyMux(routes map[string]config.ServiceRouteConfig) (*Server, *http.ServeMux) {
	s := newTestServer()
	s.cfg.Config.ServiceRoutes = routes
	mux := http.NewServeMux()
	s.registerServiceRoutes(mux)
	return s, mux
}

// TestServiceRoute_MemoryPrefixAliasInjectsCredential (#370): the
// /v1/memory/* alias forwards the caller's path verbatim, swaps the client
// key for the gateway-held memory service credential, and relays the body
// both ways.
func TestServiceRoute_MemoryPrefixAliasInjectsCredential(t *testing.T) {
	srv, authSeen, pathSeen, bodySeen := serviceProxyUpstream(t, "fm-memory-secret")
	defer srv.Close()
	_, mux := serviceProxyMux(map[string]config.ServiceRouteConfig{
		"memory": {URL: srv.URL, UpstreamAuth: "inject", UpstreamToken: "fm-memory-secret", Prefix: "/v1/memory"},
	})

	body := `{"jsonrpc":"2.0","id":7,"method":"v1.retrieve","params":{"query":"fusion","top_k":5}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/memory/retrieve", strings.NewReader(body))
	// The caller authenticates with its own client key — never forwarded.
	req.Header.Set("Authorization", "Bearer fk-client-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if len(*authSeen) != 1 || (*authSeen)[0] != "Bearer fm-memory-secret" {
		t.Fatalf("upstream Authorization = %v, want gateway-held fm-memory-secret", *authSeen)
	}
	if len(*pathSeen) != 1 || (*pathSeen)[0] != "/v1/memory/retrieve" {
		t.Fatalf("upstream path = %v, want verbatim /v1/memory/retrieve (no strip)", *pathSeen)
	}
	if len(*bodySeen) != 1 || !strings.Contains((*bodySeen)[0], `"method":"v1.retrieve"`) {
		t.Fatalf("upstream body = %q, want verbatim JSON-RPC body", (*bodySeen)[0])
	}
	if !strings.Contains(rec.Body.String(), `"result"`) {
		t.Fatalf("expected upstream result relayed, got: %s", rec.Body.String())
	}
}

// TestServiceRoute_UpstreamTokenFile (#370): upstream_token_file (tilde-
// expanded, 0600 secret file) seeds the injected credential without any
// plaintext token in config.yaml — the fm-server secret convention.
func TestServiceRoute_UpstreamTokenFile(t *testing.T) {
	srv, authSeen, _, _ := serviceProxyUpstream(t, "fm-from-file")
	defer srv.Close()

	keyFile := filepath.Join(t.TempDir(), "api_key")
	if err := os.WriteFile(keyFile, []byte("fm-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, mux := serviceProxyMux(map[string]config.ServiceRouteConfig{
		"memory": {URL: srv.URL, UpstreamAuth: "inject", UpstreamTokenFile: keyFile, Prefix: "/v1/memory"},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/memory/retrieve", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if len(*authSeen) != 1 || (*authSeen)[0] != "Bearer fm-from-file" {
		t.Fatalf("upstream Authorization = %v, want file-sourced fm-from-file", *authSeen)
	}
}

// TestServiceRoute_MissingTokenFileSkipsRoute (#370): an unreadable secret
// file fails closed — no route registered, no unauthenticated forwarding.
func TestServiceRoute_MissingTokenFileSkipsRoute(t *testing.T) {
	_, mux := serviceProxyMux(map[string]config.ServiceRouteConfig{
		"memory": {URL: "http://127.0.0.1:1", UpstreamAuth: "inject", UpstreamTokenFile: "/nonexistent/api_key", Prefix: "/v1/memory"},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/memory/retrieve", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (route skipped)", rec.Code)
	}
}

// TestServiceRoute_UpstreamDownA5Envelope (#370): a dead platform service
// gets the structured degradation envelope — code upstream_unavailable,
// retryable=true — never a bare 502 with a raw proxy error.
func TestServiceRoute_UpstreamDownA5Envelope(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("never dialed")
	}))
	deadURL := dead.URL
	dead.Close()
	_, mux := serviceProxyMux(map[string]config.ServiceRouteConfig{
		"memory": {URL: deadURL, UpstreamAuth: "inject", UpstreamToken: "x", Prefix: "/v1/memory"},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/memory/commit", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var env shell.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope decode: %v body=%s", err, rec.Body.String())
	}
	if env.Code != "upstream_unavailable" || !env.Retryable {
		t.Fatalf("envelope = %+v, want upstream_unavailable retryable", env)
	}
}

// TestServiceRoute_SvcMountStillStrips: the standard /svc/<name>/ mount keeps
// its historical behavior — the /svc/<name> prefix is stripped so the
// upstream sees its own path.
func TestServiceRoute_SvcMountStillStrips(t *testing.T) {
	srv, authSeen, pathSeen, _ := serviceProxyUpstream(t, "")
	defer srv.Close()
	_, mux := serviceProxyMux(map[string]config.ServiceRouteConfig{
		"rag": {URL: srv.URL},
	})

	req := httptest.NewRequest(http.MethodPost, "/svc/rag/v1/search", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer fk-client-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}
	if len(*pathSeen) != 1 || (*pathSeen)[0] != "/v1/search" {
		t.Fatalf("upstream path = %v, want stripped /v1/search", *pathSeen)
	}
	if len(*authSeen) != 1 || (*authSeen)[0] != "Bearer fk-client-key" {
		t.Fatalf("upstream Authorization = %v, want passthrough client key", *authSeen)
	}
}
