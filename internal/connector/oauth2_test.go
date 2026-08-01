package connector

import (
    "testing"
    "time"
)

func timeNowUTC() time.Time {
    return time.Now().UTC()
}

func TestOAuth2ProviderRegisterAndAuthorize(t *testing.T) {
    o := NewOAuth2Provider()
    o.RegisterConnector("test-connector", &OAuth2Config{
        ClientID:     "cid",
        ClientSecret: "csecret",
        AuthURL:      "https://example.com/oauth2/authorize",
        TokenURL:     "https://example.com/oauth2/token",
        RedirectURL:  "http://localhost:8100/gateway/v1/oauth2/callback",
        Scopes:       []string{"read", "write"},
    })

    url, err := o.AuthorizationURL("test-connector", "random-state")
    if err != nil {
        t.Fatalf("AuthorizationURL failed: %v", err)
    }
    if url == "" {
        t.Fatal("expected non-empty URL")
    }
    expected := "https://example.com/oauth2/authorize?"
    if len(url) < len(expected) || url[:len(expected)] != expected {
        t.Errorf("unexpected URL prefix: %s", url)
    }
}

func TestOAuth2ProviderAuthorizeMissingConnector(t *testing.T) {
    o := NewOAuth2Provider()
    _, err := o.AuthorizationURL("nonexistent", "state")
    if err == nil {
        t.Fatal("expected error for missing connector")
    }
}

func TestOAuth2ProviderEncryptDecryptWithoutCipher(t *testing.T) {
    o := NewOAuth2Provider()
    enc, err := o.EncryptToken("hello")
    if err != nil {
        t.Fatalf("EncryptToken failed: %v", err)
    }
    if enc != "hello" {
        t.Errorf("expected plaintext passthrough, got %s", enc)
    }
    dec, err := o.DecryptToken("hello")
    if err != nil {
        t.Fatalf("DecryptToken failed: %v", err)
    }
    if dec != "hello" {
        t.Errorf("expected plaintext passthrough, got %s", dec)
    }
}

func TestOAuth2ProviderEncryptDecryptWithCipher(t *testing.T) {
    o := NewOAuth2Provider()
    cipher, err := createTestCipher()
    if err != nil {
        t.Skipf("cipher not available: %v", err)
    }
    o.SetCipher(cipher)

    enc, err := o.EncryptToken("my-secret-token")
    if err != nil {
        t.Fatalf("EncryptToken failed: %v", err)
    }
    if enc == "my-secret-token" {
        t.Error("token should be encrypted, not plaintext")
    }

    dec, err := o.DecryptToken(enc)
    if err != nil {
        t.Fatalf("DecryptToken failed: %v", err)
    }
    if dec != "my-secret-token" {
        t.Errorf("expected my-secret-token, got %s", dec)
    }
}

func TestOAuth2ProviderIsTokenExpired(t *testing.T) {
    o := NewOAuth2Provider()

    conn := &Connection{ID: "test"}
    if o.IsTokenExpired(conn) {
        t.Error("connection without expiry should not be expired")
    }

    future := timeNowUTC().Add(1 * time.Hour)
    conn.TokenExpiry = &future
    if o.IsTokenExpired(conn) {
        t.Error("future expiry should not be expired")
    }

    past := timeNowUTC().Add(-1 * time.Hour)
    conn.TokenExpiry = &past
    if !o.IsTokenExpired(conn) {
        t.Error("past expiry should be expired")
    }
}
