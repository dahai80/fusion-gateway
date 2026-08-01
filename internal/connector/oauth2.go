package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type OAuth2Config struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	Scopes       []string `json:"scopes"`
	RedirectURL  string   `json:"redirect_url"`
}

type OAuth2Provider struct {
	mu      sync.RWMutex
	configs map[string]*OAuth2Config
	client  *http.Client
	cipher  tokenCipher
}

type tokenCipher interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(encoded string) (string, error)
}

func NewOAuth2Provider() *OAuth2Provider {
	return &OAuth2Provider{
		configs: make(map[string]*OAuth2Config),
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (o *OAuth2Provider) SetCipher(c tokenCipher) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cipher = c
}

func (o *OAuth2Provider) RegisterConnector(key string, cfg *OAuth2Config) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.configs[key] = cfg
	slog.Info("oauth2 config registered", "connector", key, "auth_url", cfg.AuthURL)
}

func (o *OAuth2Provider) AuthorizationURL(connectorKey, state string) (string, error) {
	o.mu.RLock()
	cfg, ok := o.configs[connectorKey]
	o.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("no oauth2 config for connector %q", connectorKey)
	}
	u, err := url.Parse(cfg.AuthURL)
	if err != nil {
		return "", fmt.Errorf("parse auth_url: %w", err)
	}
	q := u.Query()
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURL)
	q.Set("response_type", "code")
	q.Set("state", state)
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (o *OAuth2Provider) ExchangeCode(ctx context.Context, connectorKey, code string) (*TokenResponse, error) {
	o.mu.RLock()
	cfg, ok := o.configs[connectorKey]
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no oauth2 config for connector %q", connectorKey)
	}
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("redirect_uri", cfg.RedirectURL)
	data.Set("client_id", cfg.ClientID)
	data.Set("client_secret", cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}
	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	slog.Info("oauth2 token exchanged", "connector", connectorKey, "expires_in", tokenResp.ExpiresIn)
	return &tokenResp, nil
}

func (o *OAuth2Provider) RefreshToken(ctx context.Context, connectorKey, refreshToken string) (*TokenResponse, error) {
	o.mu.RLock()
	cfg, ok := o.configs[connectorKey]
	o.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no oauth2 config for connector %q", connectorKey)
	}
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", cfg.ClientID)
	data.Set("client_secret", cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh token failed (status %d): %s", resp.StatusCode, string(body))
	}
	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	slog.Info("oauth2 token refreshed", "connector", connectorKey)
	return &tokenResp, nil
}

func (o *OAuth2Provider) EncryptToken(plaintext string) (string, error) {
	o.mu.RLock()
	c := o.cipher
	o.mu.RUnlock()
	if c == nil {
		return plaintext, nil
	}
	return c.Encrypt(plaintext)
}

func (o *OAuth2Provider) DecryptToken(encoded string) (string, error) {
	o.mu.RLock()
	c := o.cipher
	o.mu.RUnlock()
	if c == nil {
		return encoded, nil
	}
	return c.Decrypt(encoded)
}

func (o *OAuth2Provider) IsTokenExpired(conn *Connection) bool {
	if conn.TokenExpiry == nil {
		return false
	}
	return time.Now().UTC().After(*conn.TokenExpiry)
}

func (o *OAuth2Provider) RefreshIfNeeded(ctx context.Context, conn *Connection) error {
	if conn.AuthType != AuthTypeOAuth2 || !o.IsTokenExpired(conn) {
		return nil
	}
	refreshToken, err := o.DecryptToken(conn.EncryptedRefreshToken)
	if err != nil {
		return fmt.Errorf("decrypt refresh token: %w", err)
	}
	if refreshToken == "" {
		return fmt.Errorf("no refresh token available for connection %q", conn.ID)
	}
	tokenResp, err := o.RefreshToken(ctx, conn.ConnectorKey, refreshToken)
	if err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}
	encAccess, err := o.EncryptToken(tokenResp.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	conn.EncryptedAccessToken = encAccess
	if tokenResp.RefreshToken != "" {
		encRefresh, err := o.EncryptToken(tokenResp.RefreshToken)
		if err != nil {
			return fmt.Errorf("encrypt refresh token: %w", err)
		}
		conn.EncryptedRefreshToken = encRefresh
	}
	if tokenResp.ExpiresIn > 0 {
		expiry := time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		conn.TokenExpiry = &expiry
	}
	conn.UpdatedAt = time.Now().UTC()
	slog.Info("oauth2 token auto-refreshed", "connection", conn.ID)
	return nil
}
