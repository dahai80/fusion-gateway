package exchange

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #595: signing key persistence + mint/verify roundtrip.

func newTestSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(t.TempDir())
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestNewSigner_PersistsKeyAndReloads(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewSigner(dir)
	if err != nil {
		t.Fatalf("first boot: %v", err)
	}
	path := filepath.Join(dir, signingKeyFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("signing key file missing: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("signing key perms = %v, want 0600", info.Mode().Perm())
	}
	s2, err := NewSigner(dir)
	if err != nil {
		t.Fatalf("second boot: %v", err)
	}
	tok, _, err := s1.Sign("sub", "aud", time.Minute, "", nil, 0, 0)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := s2.Verify(tok); err != nil {
		t.Fatalf("token from first boot must verify after reload: %v", err)
	}
}

func TestNewSigner_EmptyDataDirRejected(t *testing.T) {
	if _, err := NewSigner(""); err == nil {
		t.Fatal("empty data_dir must be rejected, not silently per-boot")
	}
}

func TestNewSigner_CorruptKeyRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, signingKeyFile), []byte("not a pem"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigner(dir); err == nil {
		t.Fatal("corrupt signing key must fail loudly")
	}
}

func TestSignVerify_Roundtrip(t *testing.T) {
	s := newTestSigner(t)
	tok, exp, err := s.Sign("office-key", "fusion-api", 5*time.Minute, "team-a", []string{"glm5.2"}, 30, 20000)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if time.Until(exp) > 5*time.Minute+time.Second {
		t.Fatalf("exp too far out: %v", exp)
	}
	claims, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "office-key" || claims.Tenant != "team-a" || claims.RPM != 30 || claims.TPM != 20000 {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if len(claims.Models) != 1 || claims.Models[0] != "glm5.2" {
		t.Fatalf("models mismatch: %v", claims.Models)
	}
	if claims.Issuer != "fusion-gateway" {
		t.Fatalf("issuer = %q", claims.Issuer)
	}
}

func TestVerify_RejectsTampered(t *testing.T) {
	s := newTestSigner(t)
	tok, _, err := s.Sign("sub", "aud", time.Minute, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected JWS compact form, got %d parts", len(parts))
	}
	bad := parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2]))
	if _, err := s.Verify(bad); err == nil {
		t.Fatal("tampered signature must be rejected")
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	s := newTestSigner(t)
	tok, _, err := s.Sign("sub", "aud", -time.Second, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(tok); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestVerify_RejectsForeignToken(t *testing.T) {
	s := newTestSigner(t)
	other := newTestSigner(t)
	tok, _, err := other.Sign("sub", "aud", time.Minute, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(tok); err == nil {
		t.Fatal("token signed by another key must be rejected")
	}
}

func TestJWKS_ShapeMatchesTokenHeader(t *testing.T) {
	s := newTestSigner(t)
	tok, _, err := s.Sign("sub", "aud", time.Minute, "", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(s.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(jwks, &parsed); err != nil {
		t.Fatalf("jwks not valid json: %v", err)
	}
	if len(parsed.Keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(parsed.Keys))
	}
	k := parsed.Keys[0]
	for _, f := range []string{"kty", "use", "alg", "kid", "n", "e"} {
		if _, ok := k[f]; !ok {
			t.Fatalf("jwk missing field %q: %s", f, jwks)
		}
	}
	if k["kty"] != "RSA" || k["alg"] != "RS256" || k["use"] != "sig" {
		t.Fatalf("bad jwk fields: %v", k)
	}
	header := strings.Split(tok, ".")[0]
	var hdr struct {
		KID string `json:"kid"`
	}
	raw, _ := base64.RawURLEncoding.DecodeString(header)
	if err := json.Unmarshal(raw, &hdr); err != nil {
		t.Fatalf("token header: %v", err)
	}
	if hdr.KID != k["kid"] {
		t.Fatalf("token kid %q != jwks kid %v", hdr.KID, k["kid"])
	}
}
