package crypto

import (
	"testing"
)

func TestAESCipher_EncryptDecrypt(t *testing.T) {
	c, err := NewAESCipher("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	original := "sk-test-api-key-12345"
	encrypted, err := c.Encrypt(original)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == original {
		t.Error("encrypted should differ from original")
	}
	decrypted, err := c.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != original {
		t.Errorf("expected %q, got %q", original, decrypted)
	}
}

func TestAESCipher_EmptyString(t *testing.T) {
	c, err := NewAESCipher("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := c.Encrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted != "" {
		t.Errorf("empty encrypt should return empty, got %q", encrypted)
	}
	decrypted, err := c.Decrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != "" {
		t.Errorf("empty decrypt should return empty, got %q", decrypted)
	}
}

func TestAESCipher_ShortKey(t *testing.T) {
	_, err := NewAESCipher("short")
	if err == nil {
		t.Error("expected error for short key")
	}
}

func TestAESCipher_WrongKey(t *testing.T) {
	c1, _ := NewAESCipher("0123456789abcdef0123456789abcdef")
	c2, _ := NewAESCipher("abcdef0123456789abcdef0123456789")
	encrypted, _ := c1.Encrypt("secret")
	_, err := c2.Decrypt(encrypted)
	if err == nil {
		t.Error("expected error decrypting with wrong key")
	}
}

func TestAESCipher_InvalidCiphertext(t *testing.T) {
	c, _ := NewAESCipher("0123456789abcdef0123456789abcdef")
	_, err := c.Decrypt("not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
	_, err = c.Decrypt("aG9ydA==")
	if err == nil {
		t.Error("expected error for too-short ciphertext")
	}
}

func TestAESCipher_DifferentNonces(t *testing.T) {
	c, _ := NewAESCipher("0123456789abcdef0123456789abcdef")
	e1, _ := c.Encrypt("same")
	e2, _ := c.Encrypt("same")
	if e1 == e2 {
		t.Error("same plaintext should produce different ciphertexts (different nonces)")
	}
	d1, _ := c.Decrypt(e1)
	d2, _ := c.Decrypt(e2)
	if d1 != "same" || d2 != "same" {
		t.Error("both should decrypt to same value")
	}
}
