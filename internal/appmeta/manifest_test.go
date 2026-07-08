package appmeta

import (
	"encoding/json"
	"testing"

	"github.com/openclaw/crawlkit/control"
)

func TestManifestSatisfiesControlV1(t *testing.T) {
	m := BuildManifest()
	if m.SchemaVersion != control.SchemaVersion {
		t.Fatalf("schema_version = %q, want %q", m.SchemaVersion, control.SchemaVersion)
	}
	if m.ID != ID || m.DisplayName != DisplayName || m.Binary.Name != BinaryName {
		t.Fatalf("identity fields wrong: %+v", m)
	}
	// The scheduler's default-job fallback keys on Commands["sync"]; it must exist.
	if _, ok := m.Commands["sync"]; !ok {
		t.Fatal("manifest must declare a sync command for scheduler discovery")
	}
	// Honesty invariants: this tool archives DMs and uploads nothing.
	if !m.Privacy.ContainsPrivateMessages {
		t.Fatal("manifest must declare ContainsPrivateMessages=true (it archives DMs)")
	}
	if m.Privacy.ExportsSecrets {
		t.Fatal("manifest must not declare ExportsSecrets=true")
	}
	if len(m.Privacy.LocalOnlyScopes) == 0 {
		t.Fatal("manifest must declare local-only scopes")
	}
}

func TestManifestJSONRoundTrips(t *testing.T) {
	m := BuildManifest()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back control.Manifest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.SchemaVersion != control.SchemaVersion {
		t.Fatalf("round-trip lost schema_version: %q", back.SchemaVersion)
	}
}

func TestStatusIsHonestWhenEmpty(t *testing.T) {
	// With INSTACRAWL_CONFIG unset and (almost certainly) no db in a test env,
	// status must report empty/ready/error — never fabricate counts.
	s := BuildStatus()
	if s.SchemaVersion != control.SchemaVersion {
		t.Fatalf("status schema_version = %q", s.SchemaVersion)
	}
	if s.AppID != ID {
		t.Fatalf("status app_id = %q", s.AppID)
	}
	switch s.State {
	case "empty", "ready", "error":
	default:
		t.Fatalf("unexpected status state %q", s.State)
	}
}
