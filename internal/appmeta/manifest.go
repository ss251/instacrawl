// Package appmeta builds instacrawl's crawlkit control-contract payloads: the
// Manifest emitted by `instacrawl metadata --json` (how the crawlctl scheduler
// discovers and schedules the tool) and the Status emitted by
// `instacrawl status --json`. Keeping these in one place makes the contract the
// single source of truth for paths, capabilities, and the privacy posture.
package appmeta

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/openclaw/crawlkit/control"

	"github.com/ss251/instacrawl/internal/session"
	"github.com/ss251/instacrawl/internal/store"
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
		"carve": {
			Title:   "Recover expired media bytes from a local cache (offline)",
			Argv:    []string{"carve"},
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
		// Carve is the zero-network local-cache lane. Its extractor states are
		// advertised honestly: the format-agnostic walker is live; the
		// format-specific Chromium/IndexedDB extractors are stubs until validated
		// against a real cache (Phase 4b) and must not be presented as working.
		"source.carve.walker.active",
		"source.carve.chromium.stub",
		"source.carve.indexeddb.stub",
		"scope.dms",
		"scope.own_content",
		"scope.saved_liked",
		"scope.media_backfill",
		"search.fts5",
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
		addArchiveCounts(&s, dbPath)
	case os.IsNotExist(err):
		s.State = "empty"
		s.Summary = "no archive yet — run `instacrawl auth import`, then `instacrawl sync`"
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

// addArchiveCounts opens the archive READ-ONLY (never creating, migrating, or
// mutating it) and appends honest per-entity counts plus the last-sync time.
// Any failure degrades to a warning and leaves State="ready" — status must never
// fail just because the archive is briefly busy.
func addArchiveCounts(s *control.Status, dbPath string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	st, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		s.Warnings = append(s.Warnings, "could not open archive for counts")
		return
	}
	defer func() { _ = st.Close() }()

	counters := []struct {
		id, label string
		fn        func(context.Context) (int, error)
	}{
		{"threads", "DM threads", st.CountThreads},
		{"messages", "messages", st.CountMessages},
		{"media", "media", st.CountMedia},
		{"own_posts", "own posts", st.CountOwnPosts},
		{"saved", "saved", st.CountSaved},
		{"liked", "liked", st.CountLiked},
		{"stories", "stories", st.CountStories},
		{"collections", "collections", st.CountCollections},
		{"blobs", "media blobs", st.CountBlobs},
		{"media_files_downloaded", "downloaded files", st.CountDownloadedFiles},
		{"unknown_messages", "messages with unrecognized types", st.CountUnknownMessages},
		{"unresolved_replies", "replies whose parent isn't archived yet", st.CountUnresolvedReplies},
	}
	var messages, media, unknown int
	for _, c := range counters {
		n, err := c.fn(ctx)
		if err != nil {
			s.Warnings = append(s.Warnings, "count "+c.id+" unavailable")
			continue
		}
		s.Counts = append(s.Counts, control.NewCount(c.id, c.label, int64(n)))
		switch c.id {
		case "messages":
			messages = n
		case "media":
			media = n
		case "unknown_messages":
			unknown = n
		}
	}
	s.Summary = fmt.Sprintf("archive present — %d messages, %d media", messages, media)
	if unknown > 0 {
		s.Warnings = append(s.Warnings, fmt.Sprintf(
			"%d message(s) use item types this build does not recognize (raw retained) — run `instacrawl reparse` after updating", unknown))
	}

	if last, err := st.MaxFetchedAt(ctx); err == nil && last != "" {
		s.LastSyncAt = last
	}
}
