package connector

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "encoding/base64"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "testing"
    "time"
)

func TestPersistenceSaveAndLoad(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "connections.json")
    p := NewPersistence(path, nil)

    conns := map[string]*Connection{
        "conn-1": {
            ID:                    "conn-1",
            ConnectorKey:          "quickbooks",
            Status:                "active",
            EncryptedAccessToken:  "enc-access-1",
            EncryptedRefreshToken: "enc-refresh-1",
            CreatedAt:             time.Now().UTC(),
            UpdatedAt:             time.Now().UTC(),
        },
    }

    if err := p.Save(conns); err != nil {
        t.Fatalf("Save failed: %v", err)
    }

    loaded, err := p.Load()
    if err != nil {
        t.Fatalf("Load failed: %v", err)
    }
    if len(loaded) != 1 {
        t.Fatalf("expected 1 connection, got %d", len(loaded))
    }
    if loaded["conn-1"].ConnectorKey != "quickbooks" {
        t.Errorf("expected connector quickbooks, got %s", loaded["conn-1"].ConnectorKey)
    }
    if loaded["conn-1"].EncryptedAccessToken != "enc-access-1" {
        t.Errorf("expected enc-access-1, got %s", loaded["conn-1"].EncryptedAccessToken)
    }
}

func TestPersistenceLoadEmpty(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "nonexistent.json")
    p := NewPersistence(path, nil)

    loaded, err := p.Load()
    if err != nil {
        t.Fatalf("Load on nonexistent file should not error: %v", err)
    }
    if len(loaded) != 0 {
        t.Fatalf("expected 0 connections, got %d", len(loaded))
    }
}

func TestPersistenceWithEncryption(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "connections.json")

    cipher, err := createTestCipher()
    if err != nil {
        t.Skipf("cipher not available: %v", err)
    }
    p := NewPersistence(path, cipher)

    encAccess, _ := cipher.Encrypt("my-access-token")
    encRefresh, _ := cipher.Encrypt("my-refresh-token")

    conns := map[string]*Connection{
        "conn-2": {
            ID:                    "conn-2",
            ConnectorKey:          "google_workspace",
            Status:                "active",
            EncryptedAccessToken:  encAccess,
            EncryptedRefreshToken: encRefresh,
            CreatedAt:             time.Now().UTC(),
            UpdatedAt:             time.Now().UTC(),
        },
    }

    if err := p.Save(conns); err != nil {
        t.Fatalf("Save failed: %v", err)
    }

    loaded, err := p.Load()
    if err != nil {
        t.Fatalf("Load failed: %v", err)
    }

    decAccess, err := cipher.Decrypt(loaded["conn-2"].EncryptedAccessToken)
    if err != nil {
        t.Fatalf("Decrypt access token failed: %v", err)
    }
    if decAccess != "my-access-token" {
        t.Errorf("expected my-access-token, got %s", decAccess)
    }

    decRefresh, err := cipher.Decrypt(loaded["conn-2"].EncryptedRefreshToken)
    if err != nil {
        t.Fatalf("Decrypt refresh token failed: %v", err)
    }
    if decRefresh != "my-refresh-token" {
        t.Errorf("expected my-refresh-token, got %s", decRefresh)
    }
}

func TestPersistenceTokenExpiry(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "connections.json")
    p := NewPersistence(path, nil)

    expiry := time.Now().UTC().Add(1 * time.Hour)
    conns := map[string]*Connection{
        "conn-3": {
            ID:           "conn-3",
            ConnectorKey: "hubspot",
            Status:       "active",
            TokenExpiry:  &expiry,
        },
    }

    if err := p.Save(conns); err != nil {
        t.Fatalf("Save failed: %v", err)
    }

    loaded, err := p.Load()
    if err != nil {
        t.Fatalf("Load failed: %v", err)
    }

    if loaded["conn-3"].TokenExpiry == nil {
        t.Fatal("TokenExpiry should not be nil")
    }
    diff := loaded["conn-3"].TokenExpiry.Sub(expiry)
    if diff < -time.Second || diff > time.Second {
        t.Errorf("TokenExpiry mismatch: expected %v, got %v", expiry, *loaded["conn-3"].TokenExpiry)
    }
}

func TestPersistenceAtomicWrite(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "connections.json")
    p := NewPersistence(path, nil)

    conns := map[string]*Connection{
        "conn-4": {ID: "conn-4", ConnectorKey: "quickbooks", Status: "active"},
    }
    if err := p.Save(conns); err != nil {
        t.Fatalf("Save failed: %v", err)
    }

    tmpFiles, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
    if len(tmpFiles) > 0 {
        t.Errorf("temp files left behind: %v", tmpFiles)
    }

    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("ReadFile failed: %v", err)
    }
    if len(data) == 0 {
        t.Fatal("file is empty")
    }
}

func createTestCipher() (tokenCipher, error) {
    key := "test-master-key-that-is-32-bytes-long!"
    block, err := aes.NewCipher([]byte(key))
    if err != nil {
        return nil, err
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return nil, err
    }
    return &testAESCipher{gcm: gcm}, nil
}

type testAESCipher struct {
    gcm cipher.AEAD
}

func (c *testAESCipher) Encrypt(plaintext string) (string, error) {
    if plaintext == "" {
        return "", nil
    }
    nonce := make([]byte, c.gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return "", fmt.Errorf("generate nonce: %w", err)
    }
    ct := c.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
    return base64.StdEncoding.EncodeToString(ct), nil
}

func (c *testAESCipher) Decrypt(encoded string) (string, error) {
    if encoded == "" {
        return "", nil
    }
    data, err := base64.StdEncoding.DecodeString(encoded)
    if err != nil {
        return "", fmt.Errorf("base64 decode: %w", err)
    }
    ns := c.gcm.NonceSize()
    if len(data) < ns {
        return "", fmt.Errorf("ciphertext too short")
    }
    pt, err := c.gcm.Open(nil, data[:ns], data[ns:], nil)
    if err != nil {
        return "", fmt.Errorf("gcm open: %w", err)
    }
    return string(pt), nil
}
