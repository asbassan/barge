package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// authConfig holds credentials for a single registry.
type authConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// bargeConfig mirrors the top-level barge config.json structure.
type bargeConfig struct {
	Auths map[string]authConfig `json:"auths"`
}

// configPath returns the path to barge's credential store.
func configPath() string {
	return filepath.Join(os.Getenv("ProgramData"), "barge", "config.json")
}

// loadConfig reads the barge config file. Returns an empty config if the file
// does not exist yet.
func loadConfig() (*bargeConfig, error) {
	cfg := &bargeConfig{Auths: make(map[string]authConfig)}
	data, err := os.ReadFile(configPath())
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read barge config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("cannot parse barge config: %w", err)
	}
	if cfg.Auths == nil {
		cfg.Auths = make(map[string]authConfig)
	}
	return cfg, nil
}

// saveConfig writes the barge config file.
func saveConfig(cfg *bargeConfig) error {
	path := configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("cannot create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// credentialsForHost returns stored credentials for the given registry host.
// Returns empty strings (not an error) when no credentials are stored.
func credentialsForHost(host string) (string, string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", "", err
	}
	ac, ok := cfg.Auths[host]
	if !ok {
		return "", "", nil
	}
	return ac.Username, ac.Password, nil
}

// Login stores credentials for a registry in the barge config file.
func (cl *Client) Login(_ context.Context, registry, username, password string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cfg.Auths[registry] = authConfig{Username: username, Password: password}
	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("cannot save credentials: %w", err)
	}
	return nil
}

// Logout removes stored credentials for a registry from the barge config file.
func (cl *Client) Logout(_ context.Context, registry string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if _, ok := cfg.Auths[registry]; !ok {
		return fmt.Errorf("not logged in to %q", registry)
	}
	delete(cfg.Auths, registry)
	if err := saveConfig(cfg); err != nil {
		return fmt.Errorf("cannot save config: %w", err)
	}
	return nil
}
