package connector

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Persistence struct {
	mu     sync.Mutex
	path   string
	cipher tokenCipher
}

func NewPersistence(path string, cipher tokenCipher) *Persistence {
	return &Persistence{
		path:   path,
		cipher: cipher,
	}
}

type persistedConnection struct {
	ID                    string            `json:"id"`
	ConnectorKey          string            `json:"connectorKey"`
	AuthType              AuthType          `json:"authType"`
	CreatedAt             string            `json:"createdAt"`
	UpdatedAt             string            `json:"updatedAt"`
	ExpiresAt             string            `json:"expiresAt,omitempty"`
	Status                string            `json:"status"`
	EncryptedAccessToken  string            `json:"encryptedAccessToken,omitempty"`
	EncryptedRefreshToken string            `json:"encryptedRefreshToken,omitempty"`
	TokenExpiry           string            `json:"tokenExpiry,omitempty"`
	AuthConfig            map[string]string `json:"authConfig,omitempty"`
}

type persistenceFile struct {
	Connections []persistedConnection `json:"connections"`
}

const timeLayout = "2006-01-02T15:04:05Z"

func (p *Persistence) Load() (map[string]*Connection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("persistence file not found, starting fresh", "path", p.path)
			return make(map[string]*Connection), nil
		}
		return nil, err
	}

	var pf persistenceFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return nil, err
	}

	connections := make(map[string]*Connection, len(pf.Connections))
	for _, pc := range pf.Connections {
		conn := &Connection{
			ID:                    pc.ID,
			ConnectorKey:          pc.ConnectorKey,
			AuthType:              pc.AuthType,
			Status:                pc.Status,
			EncryptedAccessToken:  pc.EncryptedAccessToken,
			EncryptedRefreshToken: pc.EncryptedRefreshToken,
			AuthConfig:            pc.AuthConfig,
		}
		if t, err := time.Parse(timeLayout, pc.CreatedAt); err == nil {
			conn.CreatedAt = t
		}
		if t, err := time.Parse(timeLayout, pc.UpdatedAt); err == nil {
			conn.UpdatedAt = t
		}
		if t, err := time.Parse(timeLayout, pc.ExpiresAt); err == nil {
			conn.ExpiresAt = &t
		}
		if t, err := time.Parse(timeLayout, pc.TokenExpiry); err == nil {
			conn.TokenExpiry = &t
		}
		connections[conn.ID] = conn
	}
	slog.Info("persistence loaded", "path", p.path, "connections", len(connections))
	return connections, nil
}

func (p *Persistence) Save(connections map[string]*Connection) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	pf := persistenceFile{
		Connections: make([]persistedConnection, 0, len(connections)),
	}
	for _, conn := range connections {
		pc := persistedConnection{
			ID:                    conn.ID,
			ConnectorKey:          conn.ConnectorKey,
			AuthType:              conn.AuthType,
			CreatedAt:             conn.CreatedAt.UTC().Format(timeLayout),
			UpdatedAt:             conn.UpdatedAt.UTC().Format(timeLayout),
			Status:                conn.Status,
			EncryptedAccessToken:  conn.EncryptedAccessToken,
			EncryptedRefreshToken: conn.EncryptedRefreshToken,
			AuthConfig:            conn.AuthConfig,
		}
		if conn.ExpiresAt != nil {
			pc.ExpiresAt = conn.ExpiresAt.UTC().Format(timeLayout)
		}
		if conn.TokenExpiry != nil {
			pc.TokenExpiry = conn.TokenExpiry.UTC().Format(timeLayout)
		}
		pf.Connections = append(pf.Connections, pc)
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := p.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		return err
	}
	slog.Info("persistence saved", "path", p.path, "connections", len(pf.Connections))
	return nil
}
