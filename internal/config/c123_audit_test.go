package config

// C1/C2/C3 (audit P1-ops) tests. The three release-blocking P1 items were
// graded "operational" in audit/fusion-gateway-audit-result-product-0827.md,
// but the code layer can force them: C1 = placeholder-detection so a shipped
// config cannot Load with publicly-known credentials; C2 = fail-closed when
// OAuth2/connector is active without encryption.master_key; C3 = deploy
// manifests unified to the DefaultConfig port (11432). These tests pin each
// forcing function so a regression reopens the release block.

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

// TestC1_PlaceholderPatternDetection verifies the C1 pattern layer catches the
// shipped config.example.yaml placeholder families that evade exact-match:
// fg-* prefix, your-*-key stubs, sk-your-* stubs, *-change-me, *-do-not-ship.
// A real strong secret is not flagged.
func TestC1_PlaceholderPatternDetection(t *testing.T) {
    bad := []string{
        "fg-master-key-change-me",
        "fg-admin-key",
        "fg-demo-key-change-me",
        "fg-local-dev-jwt-secret-7f3a9c2e1b8d4e60-DO-NOT-SHIP",
        "your-baichuan-key",
        "your-volcengine-key",
        "your-qianfan-key",
        "sk-your-openai-key",
        "sk-ant-your-key",
        "change-me-secure-password",
        "change-me-at-least-32-chars-long-random-secret",
        "my-replace-me-key",
        "set-me-to-a-real-key",
        "example-key-12345",
        "placeholder-key",
        "CHANGE-ME-NOW",
    }
    for _, s := range bad {
        if !isKnownInsecureSecret(s) {
            t.Errorf("C1: expected placeholder %q to be detected, but isKnownInsecureSecret returned false", s)
        }
    }
    good := []string{
        "dahai168",
        "sk-proj-9z8HJ2kL4mN7pQ3rS6tV1wX0yB5cD8eF",
        "a7f3b9c2e1d48605ab3cd91ef27b6e0a",
        "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6",
        "",
    }
    for _, s := range good {
        if isKnownInsecureSecret(s) {
            t.Errorf("C1: expected %q to be accepted (not a placeholder), but isKnownInsecureSecret returned true", s)
        }
    }
}

// TestC1_JWTSecretPatternDetection verifies the JWT secret check reuses the
// pattern layer so "fg-local-dev-jwt-secret-...-DO-NOT-SHIP" is rejected.
func TestC1_JWTSecretPatternDetection(t *testing.T) {
    bad := []string{
        "fg-local-dev-jwt-secret-7f3a9c2e1b8d4e60-DO-NOT-SHIP",
        "change-me-at-least-32-chars-long-random-secret",
    }
    for _, s := range bad {
        if !isKnownInsecureJWTSecret(s) {
            t.Errorf("C1: expected JWT placeholder %q to be detected, got false", s)
        }
    }
    good := "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6d9e2b5a8"
    if isKnownInsecureJWTSecret(good) {
        t.Errorf("C1: expected strong JWT secret to be accepted, got detected as placeholder")
    }
}

// TestC1_AuthMasterKeyPlaceholderRejected verifies validate() refuses a
// placeholder auth.master_key. The guard fires whenever a master_key is SET,
// not only when auth.enabled=true: withMasterKey (/metrics, /debug/pprof) and
// the MCP gate honor any non-empty master_key regardless of auth.enabled, so a
// bare `helm install` (auth.enabled=false) with a placeholder master_key would
// otherwise expose the protected endpoints to anyone holding the public value.
func TestC1_AuthMasterKeyPlaceholderRejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Auth.Enabled = true
    cfg.Auth.MasterKey = "fg-master-key-change-me"
    if err := validate(&cfg); err == nil {
        t.Fatal("C1: expected validate to reject placeholder auth.master_key, got nil")
    }
    cfg.Auth.MasterKey = "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6"
    if err := validate(&cfg); err != nil {
        t.Fatalf("C1: expected strong auth.master_key to pass validate, got: %v", err)
    }
}

// TestC1_MasterKeyGuardFiresWithoutAuthEnabled pins the broadened guard: a
// placeholder master_key is rejected even when auth.enabled=false (the Helm
// chart default). Before the fix the auth.Enabled precondition let a bare
// `helm install` ship the literal master_key as a live /metrics gate key.
func TestC1_MasterKeyGuardFiresWithoutAuthEnabled(t *testing.T) {
    for _, bad := range []string{
        "fg-master-key-change-me",        // fg- prefix family
        "CHANGE_ME_MASTER_KEY",           // Helm values.yaml default (underscore)
        "${FG_MASTER_KEY}",               // unexpanded env-var ref in a configmap
        "change_me_to_a_real_key",        // underscore variant of change-me
        "{{ .Values.secrets.master_key }}", // unexpanded Helm template ref
    } {
        cfg := DefaultConfig()
        cfg.Auth.Enabled = false // chart default — guard must still fire
        cfg.Auth.MasterKey = bad
        if err := validate(&cfg); err == nil {
            t.Errorf("C1: expected validate to reject master_key %q with auth.enabled=false, got nil", bad)
        }
    }
}

// TestC1_MasterKeyEnvRefAndUnderscoreDetection is the unit-level guard for the
// two new placeholder classes added to looksLikePlaceholder: unexpanded
// ${VAR}/{{ VAR }} references and the underscore change_me marker. These evade
// the original exact-match table and the hyphen-only substring markers.
func TestC1_MasterKeyEnvRefAndUnderscoreDetection(t *testing.T) {
    bad := []string{
        "${FG_MASTER_KEY}",
        "${ANY_ENV_VAR}",
        "prefix-${VAR}-suffix",
        "CHANGE_ME_MASTER_KEY",
        "change_me_now",
        "{{ .Values.secrets.master_key }}",
        "{{ MASTER_KEY }}",
    }
    for _, s := range bad {
        if !isKnownInsecureSecret(s) {
            t.Errorf("C1: expected %q to be detected as placeholder, got false", s)
        }
    }
    // A real secret that happens to contain "$" but no braced ref must NOT be
    // flagged (false-positive guard on looksLikeEnvRef).
    good := []string{
        "sk-proj-9z8HJ2$kL4mN7pQ3rS6tV1wX0yB5cD8eF",
        "a7f3b9c2e1d48605ab3cd91ef27b6e0a",
        "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6",
        "",
    }
    for _, s := range good {
        if isKnownInsecureSecret(s) {
            t.Errorf("C1: expected %q to be accepted (not a placeholder), got true", s)
        }
    }
}

// TestC1_APIKeyPlaceholderRejected verifies validate() refuses a placeholder
// gateway auth api_key when auth is enabled.
func TestC1_APIKeyPlaceholderRejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Auth.Enabled = true
    cfg.Auth.APIKeys = []AuthKeyConfig{
        {Name: "demo", Key: "fg-demo-key-change-me"},
    }
    if err := validate(&cfg); err == nil {
        t.Fatal("C1: expected validate to reject placeholder api_key, got nil")
    }
    cfg.Auth.APIKeys[0].Key = "sk-proj-9z8HJ2kL4mN7pQ3rS6tV1wX0yB5cD8eF"
    if err := validate(&cfg); err != nil {
        t.Fatalf("C1: expected strong api_key to pass validate, got: %v", err)
    }
}

// TestC1_BackendAPIKeyPlaceholderRejected verifies the C1 extension: an
// ENABLED backend with a placeholder api_key is refused at Load. Disabled
// backends keep their stub (reserved, not live). This is the credential
// surface R7 never covered — upstream provider keys, not gateway auth keys.
func TestC1_BackendAPIKeyPlaceholderRejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Backends = map[string]BackendConfig{
        "volcengine": {
            Type:    "volcengine",
            BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
            APIKey:  "your-volcengine-key",
            Enabled: true,
        },
    }
    if err := validate(&cfg); err == nil {
        t.Fatal("C1: expected validate to reject placeholder backend api_key on enabled backend, got nil")
    }
    // Disabled backend with the same stub must pass (reserved, not live).
    b := cfg.Backends["volcengine"]
    b.Enabled = false
    cfg.Backends["volcengine"] = b
    if err := validate(&cfg); err != nil {
        t.Fatalf("C1: expected disabled backend with placeholder api_key to pass, got: %v", err)
    }
    // Enabled backend with a real key must pass.
    b = cfg.Backends["volcengine"]
    b.Enabled = true
    b.APIKey = "sk-proj-9z8HJ2kL4mN7pQ3rS6tV1wX0yB5cD8eF"
    cfg.Backends["volcengine"] = b
    if err := validate(&cfg); err != nil {
        t.Fatalf("C1: expected enabled backend with real api_key to pass, got: %v", err)
    }
}

// TestC1_FusionMLXAPIKeyEnvSkip verifies the FUSION_MLX_API_KEY escape hatch:
// the ecosystem default mlx key ("fg-admin-key", set by fusion-cli) starts
// with the "fg-" placeholder prefix and would trip the C1 guard. When an
// operator injects it via FUSION_MLX_API_KEY (the ecosystem convention),
// validate must skip the placeholder refusal for the fusion-mlx backend —
// the env is operator-provided at runtime, not a shipped template stub. A
// "fg-" key NOT matching the env is still refused (the guard stays intact
// for config.yaml stubs and for other backends).
func TestC1_FusionMLXAPIKeyEnvSkip(t *testing.T) {
    t.Setenv("FUSION_MLX_API_KEY", "fg-admin-key")
    cfg := DefaultConfig()
    cfg.Backends = map[string]BackendConfig{
        "fusion-mlx": {
            Type:    "fusion-mlx",
            BaseURL: "http://127.0.0.1:11434",
            APIKey:  "fg-admin-key",
            Enabled: true,
        },
    }
    if err := validate(&cfg); err != nil {
        t.Fatalf("C1: expected fusion-mlx api_key injected via FUSION_MLX_API_KEY to pass, got: %v", err)
    }
    // Same "fg-" key but env NOT set (or mismatched) must still be refused —
    // the escape hatch is opt-in via env, not a blanket local-backend bypass.
    t.Setenv("FUSION_MLX_API_KEY", "")
    if err := validate(&cfg); err == nil {
        t.Fatal("C1: expected fusion-mlx fg- key WITHOUT env to be rejected, got nil")
    }
    // A non-fusion-mlx backend with a "fg-" stub key must be rejected even
    // when FUSION_MLX_API_KEY is set — the env escape is local-mlx-only.
    t.Setenv("FUSION_MLX_API_KEY", "fg-admin-key")
    cfg.Backends["glm52"] = BackendConfig{
        Type:    "anthropic",
        BaseURL: "http://example.invalid",
        APIKey:  "fg-some-stub",
        Enabled: true,
    }
    if err := validate(&cfg); err == nil {
        t.Fatal("C1: expected non-fusion-mlx backend with fg- stub to be rejected even with env, got nil")
    }
}

// TestC1_AdminPasswordPlaceholderRejected verifies validate() refuses a
// placeholder admin password when admin is enabled.
func TestC1_AdminPasswordPlaceholderRejected(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Admin = &AdminConfig{
        Enabled:   true,
        JWTSecret: "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6d9e2b5a8",
        Users:     map[string]string{"admin": "change-me-secure-password"},
    }
    if err := validate(&cfg); err == nil {
        t.Fatal("C1: expected validate to reject placeholder admin password, got nil")
    }
    cfg.Admin.Users["admin"] = "9k2m5n8q1r4s7t0v3w6x9y2b5c8d"
    if err := validate(&cfg); err != nil {
        t.Fatalf("C1: expected strong admin password to pass validate, got: %v", err)
    }
}

// TestC2_OIDCEnabledNoMasterKeyFails verifies C2 is fail-closed: OIDC enabled
// without encryption.master_key now ERRORS at Load (was only a WARN), so a
// deployment that activates OAuth2 flows cannot persist tokens plaintext.
func TestC2_OIDCEnabledNoMasterKeyFails(t *testing.T) {
    cfg := DefaultConfig()
    cfg.OIDC.Enabled = true
    cfg.Encryption = &EncryptionConfig{MasterKey: ""}
    if err := validate(&cfg); err == nil {
        t.Fatal("C2: expected validate to FAIL when OIDC enabled + master_key empty (was WARN), got nil")
    }
    // Strong master_key passes.
    cfg.Encryption.MasterKey = "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6d9e2b5a8"
    if err := validate(&cfg); err != nil {
        t.Fatalf("C2: expected OIDC + strong master_key to pass, got: %v", err)
    }
}

// TestC2_ConnectorConfiguredNoMasterKeyFails verifies the connector signal:
// a configured connector block (persistence_path set) without master_key is
// refused, since stored connector tokens would persist plaintext.
func TestC2_ConnectorConfiguredNoMasterKeyFails(t *testing.T) {
    cfg := DefaultConfig()
    cfg.Connector = &ConnectorConfig{PersistencePath: "data/connections.json"}
    cfg.Encryption = &EncryptionConfig{MasterKey: ""}
    if err := validate(&cfg); err == nil {
        t.Fatal("C2: expected validate to FAIL when connector configured + master_key empty, got nil")
    }
    cfg.Encryption.MasterKey = "9k2m5n8q1r4s7t0v3w6x9y2b5c8d1e4f7a0b3c6d9e2b5a8"
    if err := validate(&cfg); err != nil {
        t.Fatalf("C2: expected connector + strong master_key to pass, got: %v", err)
    }
}

// TestC2_OIDCDisabledNoMasterKeyOK verifies a local-only no-OIDC/no-connector
// deployment needs no master_key (nothing to encrypt) — C2 forcing only bites
// when OAuth2 token persistence is actually active.
func TestC2_OIDCDisabledNoMasterKeyOK(t *testing.T) {
    cfg := DefaultConfig()
    cfg.OIDC.Enabled = false
    cfg.Connector = nil
    cfg.Encryption = nil
    if err := validate(&cfg); err != nil {
        t.Fatalf("C2: expected local-only config with no master_key to pass, got: %v", err)
    }
}

// TestC2_PlaceholderMasterKeyFails verifies a placeholder encryption.master_key
// is refused when OAuth2 is active (encrypting with a publicly-known key is as
// bad as plaintext).
func TestC2_PlaceholderMasterKeyFails(t *testing.T) {
    cfg := DefaultConfig()
    cfg.OIDC.Enabled = true
    cfg.Encryption = &EncryptionConfig{MasterKey: "fg-master-key-change-me"}
    if err := validate(&cfg); err == nil {
        t.Fatal("C2: expected validate to FAIL when OIDC + placeholder master_key, got nil")
    }
}

// TestC2_ShortMasterKeyFails verifies the >=32 char floor on encryption.master_key
// when OAuth2 is active.
func TestC2_ShortMasterKeyFails(t *testing.T) {
    cfg := DefaultConfig()
    cfg.OIDC.Enabled = true
    cfg.Encryption = &EncryptionConfig{MasterKey: "tooshort"}
    if err := validate(&cfg); err == nil {
        t.Fatal("C2: expected validate to FAIL when OIDC + master_key < 32 chars, got nil")
    }
}

// TestC3_DeployPortUnified verifies C3: every deploy manifest (Dockerfile,
// kubernetes/*, helm values, terraform outputs) uses 11432, NOT 8100. The
// audit found Dockerfile/k8s/helm at 8100 while DefaultConfig=11432, so probes
// and services mismatched the actual listener. This test pins the unification.
func TestC3_DeployPortUnified(t *testing.T) {
    deployFiles := []string{
        "../../deploy/Dockerfile",
        "../../deploy/kubernetes/deployment.yaml",
        "../../deploy/kubernetes/service.yaml",
        "../../deploy/kubernetes/configmap.yaml",
        "../../deploy/kubernetes/ingress.yaml",
        "../../deploy/kubernetes/networkpolicy.yaml",
        "../../deploy/helm/fusion-gateway/values.yaml",
        "../../deploy/terraform/outputs.tf",
        "../../deploy/terraform/gcp/outputs.tf",
        "../../deploy/terraform/aws/outputs.tf",
    }
    for _, rel := range deployFiles {
        path := filepath.Join(".", rel)
        data, err := os.ReadFile(path)
        if err != nil {
            t.Fatalf("C3: cannot read deploy file %s: %v", rel, err)
        }
        if strings.Contains(string(data), "8100") {
            t.Errorf("C3: deploy file %s still contains 8100 — must be unified to 11432", rel)
        }
    }
    // DefaultConfig port must match the manifests.
    cfg := DefaultConfig()
    if cfg.Server.Port != 11432 {
        t.Errorf("C3: DefaultConfig Server.Port = %d, want 11432 (must match deploy manifests)", cfg.Server.Port)
    }
}
