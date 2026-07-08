package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	crawlconfig "github.com/openclaw/crawlkit/config"
)

// Env var names for CI / headless resolution. These hold the SECRET at runtime
// and are never written to disk.
const (
	EnvSessionID = "INSTACRAWL_SESSIONID"
	EnvCSRFToken = "INSTACRAWL_CSRFTOKEN"
	EnvDSUserID  = "INSTACRAWL_DS_USER_ID"
)

// SessionMeta is the ONLY session-related thing written to config.toml: where
// the secret lives, never the secret itself (the discrawl remote_token.go
// contract). Source is a tri-state: "env" | "keyring" | "none".
type SessionMeta struct {
	Source  string `toml:"source"`
	Service string `toml:"service"`
	Account string `toml:"account"`
}

// Config is the instacrawl on-disk config. Phase 1 only carries session
// metadata; later phases extend it.
type Config struct {
	Session SessionMeta `toml:"session"`
}

// service/account with defaults applied.
func (m SessionMeta) resolved() (service, account string) {
	service = strings.TrimSpace(m.Service)
	if service == "" {
		service = KeychainService
	}
	account = strings.TrimSpace(m.Account)
	if account == "" {
		account = KeychainAccount
	}
	return service, account
}

// LoadConfig reads config.toml, returning a zero Config (Source "") if the file
// does not exist yet.
func LoadConfig(path string) (Config, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	var cfg Config
	if err := crawlconfig.LoadTOML(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("load config %s: %w", path, err)
	}
	return cfg, nil
}

// SaveConfig writes config.toml at 0o600, creating the parent dir at 0o700.
func SaveConfig(path string, cfg Config) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config dir %s: %w", dir, err)
		}
	}
	if err := crawlconfig.WriteTOML(path, cfg, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// ResolveBundle finds the active session bundle following env → keychain → none.
// getenv is injectable for tests (pass os.Getenv in production). It returns the
// bundle and the source it came from ("env"|"keyring"), or ErrNoSession.
func ResolveBundle(cfg Config, getenv func(string) string) (*WebBundle, string, error) {
	if b := bundleFromEnv(getenv); b != nil {
		return b, "env", nil
	}
	if cfg.Session.Source == "none" {
		return nil, "", fmt.Errorf("%w: session source is 'none' (run `instacrawl auth import`)", ErrNoSession)
	}
	service, account := cfg.Session.resolved()
	b, err := LoadBundle(service, account)
	if err != nil {
		return nil, "", err
	}
	return b, "keyring", nil
}

func bundleFromEnv(getenv func(string) string) *WebBundle {
	sid := strings.TrimSpace(getenv(EnvSessionID))
	if sid == "" {
		return nil
	}
	b := &WebBundle{
		SessionID: sid,
		CSRFToken: strings.TrimSpace(getenv(EnvCSRFToken)),
		DSUserID:  strings.TrimSpace(getenv(EnvDSUserID)),
		Acquired:  "env",
	}
	b.Normalize()
	return b
}
