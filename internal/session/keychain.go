package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zalando/go-keyring"
)

// Keychain identifiers. The account carries the sub-scope so an owner with two
// of their OWN accounts can hold two bundles without a repaint later
// (account = "web-session" or "web-session:<ds_user_id>").
const (
	KeychainService = "instacrawl"
	KeychainAccount = "web-session"
)

// ErrNoSession is returned when no bundle is stored. It wraps keyring.ErrNotFound
// so callers can errors.Is on either.
var ErrNoSession = errors.New("no session bundle stored")

// Indirected through package vars so tests can stub the keyring without a real
// OS keychain (the discrawl pattern). Production code always calls these.
var (
	sessionKeyringSet    = keyring.Set
	sessionKeyringGet    = keyring.Get
	sessionKeyringDelete = keyring.Delete
)

// StoreBundle marshals the bundle to a single JSON string and writes it to the
// OS keychain under (service, account).
func StoreBundle(service, account string, b *WebBundle) error {
	if b == nil {
		return errors.New("cannot store a nil bundle")
	}
	if b.ImportedAt.IsZero() {
		b.ImportedAt = time.Now().UTC()
	}
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal session bundle: %w", err)
	}
	if err := sessionKeyringSet(service, account, string(data)); err != nil {
		return fmt.Errorf("write session bundle to keychain %s/%s: %w", service, account, err)
	}
	return nil
}

// LoadBundle reads and unmarshals the bundle from the OS keychain. It returns
// ErrNoSession (wrapping keyring.ErrNotFound) when nothing is stored.
func LoadBundle(service, account string) (*WebBundle, error) {
	raw, err := sessionKeyringGet(service, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, fmt.Errorf("%w: keychain item %s/%s: %w", ErrNoSession, service, account, err)
		}
		return nil, fmt.Errorf("read session bundle from keychain %s/%s: %w", service, account, err)
	}
	if raw == "" {
		return nil, fmt.Errorf("%w: keychain item %s/%s is empty", ErrNoSession, service, account)
	}
	var b WebBundle
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		return nil, fmt.Errorf("unmarshal stored session bundle: %w", err)
	}
	b.Normalize()
	return &b, nil
}

// DeleteBundle removes the stored bundle. Deleting an absent item is not an
// error (idempotent logout).
func DeleteBundle(service, account string) error {
	err := sessionKeyringDelete(service, account)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete session bundle from keychain %s/%s: %w", service, account, err)
	}
	return nil
}
