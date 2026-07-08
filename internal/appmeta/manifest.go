// Package appmeta builds instacrawl's crawlkit control-contract payloads: the
// Manifest emitted by `instacrawl metadata --json` (how the crawlctl scheduler
// discovers and schedules the tool) and the Status emitted by
// `instacrawl status --json`. Keeping these in one place makes the contract the
// single source of truth for paths, capabilities, and the privacy posture.
package appmeta

import (
	"os"
	"path/filepath"

	"github.com/openclaw/crawlkit/control"

	"github.com/ss251/instacrawl/internal/session"
)

const (
	// ID is the stable app identifier the scheduler keys on.
	ID = "instacrawl"
	// DisplayName is the human label.
	DisplayName = "instacrawl"
	// BinaryName is the executable name on PATH.
	BinaryName = "instacrawl"
	// AccentColor is Instagram's brand pink, used only for local UI branding.
	AccentColor = "#e1306c"

	// dataDirName is the legacy-convention home-relative archive directory,
	// matching the rest of the family (~/.<tool>/<tool>.db). Provisional in the
	// Phase 0 scaffold; a later phase may switch to crawlkit's platform dirs.
	dataDirName = ".instacrawl"
	dbFileName  = "instacrawl.db"
	configFile  = "config.toml"

	// ConfigEnv overrides the config path.
	ConfigEnv = "INSTACRAWL_CONFIG"
)

// DataDir returns the archive directory (~/.instacrawl), creating no files.
func DataDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dataDirName // degrade to a relative dir rather than panic
	}
	return filepath.Join(home, dataDirName)
}

// DatabasePath returns the archive SQLite path (~/.instacrawl/instacrawl.db).
func DatabasePath() string { return filepath.Join(DataDir(), dbFileName) }

// MediaDir returns the content-addressed media directory (~/.instacrawl/media).
func MediaDir() string { return filepath.Join(DataDir(), "media") }

// ConfigPath returns the config path, honoring INSTACRAWL_CONFIG.
func ConfigPath() string {
	if v := os.Getenv(ConfigEnv); v != "" {
		return v
	}
	return filepath.Join(DataDir(), configFile)
}

// BuildManifest returns the control.Manifest for instacrawl. It is honest about
// two things the scheduler and any UI must know: this tool contains PRIVATE
// MESSAGES (DMs), and every scope is local-only (nothing is uploaded).
func BuildManifest() control.Manifest {
	m := control.NewManifest(ID, DisplayName, BinaryName)
	m.Description = "Local-first archiver for your own Instagram data — DMs, posts/reels/stories, saved and liked. Reuses your logged-in session; nothing is uploaded."
	m.Branding = control.Branding{
		SymbolName:  "camera",
		AccentColor: AccentColor,
	}
	m.Paths = control.Paths{
		DefaultConfig:   ConfigPath(),
		ConfigEnv:       ConfigEnv,
		DefaultDatabase: DatabasePath(),
	}
	m.Commands = map[string]control.Command{
		"sync": {
			Title:   "Sync your Instagram archive",
			Argv:    []string{"sync"},
			Mutates: true,
		},
		"status": {
			Title: "Report archive status",
			Argv:  []string{"status", "--json"},
			JSON:  true,
		},
		"metadata": {
			Title: "Print the app manifest",
			Argv:  []string{"metadata", "--json"},
			JSON:  true,
		},
		"auth-status": {
			Title: "Report session/auth status",
			Argv:  []string{"auth", "status", "--json"},
			JSON:  true,
		},
	}
	// Capabilities describe the source lanes and scopes honestly. The mobile
	// lane is advertised as opt-in; it is off by default.
	m.Capabilities = []string{
		"source.web",        // default lane: real browser cookies
		"source.mobile.opt", // opt-in lane: private mobile API, off by default
		"source.carve",      // zero-network local cache carve
		"scope.dms",
		"scope.own_content",
		"scope.saved_liked",
		"incremental",
	}
	m.Privacy = control.Privacy{
		ContainsPrivateMessages: true, // DMs are archived
		ExportsSecrets:          false,
		LocalOnlyScopes:         []string{"dms", "messages", "media", "saved", "liked"},
	}
	return m
}

// BuildStatus returns the current control.Status by inspecting the archive on
// disk. With no schema written yet (Phase 0 scaffold), it reports an honest
// "empty" state rather than a fabricated summary.
func BuildStatus() control.Status {
	dbPath := DatabasePath()
	s := control.NewStatus(ID, "no archive yet")
	s.ConfigPath = ConfigPath()
	s.DatabasePath = dbPath

	info, err := os.Stat(dbPath)
	switch {
	case err == nil:
		s.State = "ready"
		s.Summary = "archive present"
		s.DatabaseBytes = info.Size()
		if wal, werr := os.Stat(dbPath + "-wal"); werr == nil {
			s.WALBytes = wal.Size()
		}
	case os.IsNotExist(err):
		s.State = "empty"
		s.Summary = "no archive yet — `instacrawl sync` is not implemented in this scaffold"
	default:
		s.State = "error"
		s.Summary = "cannot stat archive database"
		s.Errors = []string{err.Error()}
	}

	// Report whether a session is configured — read the config offline only
	// (never touches the keychain or the network from a control-status call).
	if cfg, cerr := session.LoadConfig(ConfigPath()); cerr == nil && cfg.Session.Source == "keyring" {
		s.Counts = append(s.Counts, control.NewCount("session", "session configured", 1))
	} else {
		s.Warnings = append(s.Warnings, "no session imported — run `instacrawl auth import`")
	}
	return s
}
