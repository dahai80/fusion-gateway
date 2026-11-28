package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dispatchArtifacts runs the registered chain (withShellSession →
// requireShellAdmin → handleArtifactsRpc) exactly as muxed in shell.go.
func dispatchArtifacts(s *Server, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/artifacts", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.withShellSession(requireShellAdmin(s.handleArtifactsRpc))(w, req)
	return w
}

func TestArtifactsProxy_StampsIdentityAndForwards(t *testing.T) {
	var captured http.Header
	var capturedBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		capturedBody = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"artifacts":[]}}`))
	}))
	defer upstream.Close()

	t.Setenv("FUSION_ARTIFACTS_URL", upstream.URL)
	t.Setenv("FUSION_ARTIFACTS_API_KEY", "test-engine-key")

	s := newShellTestServer(t, false)
	_, login := loginShell(t, s, "admin", "s3cret")
	token := login["token"].(string)

	w := dispatchArtifacts(s, token, `{"jsonrpc":"2.0","id":1,"method":"artifact.list_all","params":{"caller_user_id":"spoofed","page":1}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("proxy code = %d body=%s", w.Code, w.Body.String())
	}
	if got := captured.Get("X-API-Key"); got != "test-engine-key" {
		t.Fatalf("X-API-Key = %q, want engine key", got)
	}
	var rpc struct {
		Params map[string]any `json:"params"`
		Method string         `json:"method"`
	}
	if err := json.Unmarshal(capturedBody, &rpc); err != nil {
		t.Fatalf("upstream body decode: %v", err)
	}
	if rpc.Method != "artifact.list_all" {
		t.Fatalf("method = %q", rpc.Method)
	}
	// client-sent spoofed caller must be overwritten from session claims
	if rpc.Params["caller_user_id"] != "admin" || rpc.Params["owner_user_id"] != "admin" {
		t.Fatalf("identity stamp failed: caller=%v owner=%v", rpc.Params["caller_user_id"], rpc.Params["owner_user_id"])
	}
	if w.Body.String() != `{"jsonrpc":"2.0","id":1,"result":{"artifacts":[]}}` {
		t.Fatalf("response not passed through: %s", w.Body.String())
	}
}

func TestArtifactsProxy_NonAdminAndAnonymousRejected(t *testing.T) {
	s := newShellTestServer(t, false)
	_, login := loginShell(t, s, "admin", "s3cret")
	adminToken := login["token"].(string)

	// non-admin shell user → 403 (tenant-wide engine listing stays admin-only)
	_ = shellRequest(t, s, adminToken, http.MethodPost, "/api/v1/shell/users",
		`{"username":"bob","display_name":"Bob","password":"bobpassword","permissions":[]}`)
	_, bobLogin := loginShell(t, s, "bob", "bobpassword")
	bobToken := bobLogin["token"].(string)
	if w := dispatchArtifacts(s, bobToken, `{"jsonrpc":"2.0","id":1,"method":"ping"}`); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin code = %d, want 403", w.Code)
	}
	// anonymous → 401
	if w := dispatchArtifacts(s, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous code = %d, want 401", w.Code)
	}
}

func TestArtifactsProxy_UpstreamUnreachable(t *testing.T) {
	t.Setenv("FUSION_ARTIFACTS_URL", "http://127.0.0.1:1") // dead port
	s := newShellTestServer(t, false)
	_, login := loginShell(t, s, "admin", "s3cret")
	token := login["token"].(string)
	w := dispatchArtifacts(s, token, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("code = %d, want 502", w.Code)
	}
}
