package main

import (
	"encoding/json"
	"testing"

	"github.com/openclaw/crawlkit/control"
)

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no args is usage", nil, 2},
		{"unknown command is usage", []string{"bogus"}, 2},
		{"unknown flag is usage", []string{"status", "--nope"}, 2},
		{"version ok", []string{"version"}, 0},
		{"help ok", []string{"help"}, 0},
		{"metadata ok", []string{"metadata", "--json"}, 0},
		{"status ok", []string{"status"}, 0},
		{"sync not implemented is runtime error", []string{"sync"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(c.args); got != c.want {
				t.Fatalf("run(%v) = %d, want %d", c.args, got, c.want)
			}
		})
	}
}

func TestHoistJSONAnywhere(t *testing.T) {
	// --json must work before or after the subcommand.
	if j, rest := hoistJSON([]string{"--json", "status"}); !j || len(rest) != 1 || rest[0] != "status" {
		t.Fatalf("pre-position: json=%v rest=%v", j, rest)
	}
	if j, rest := hoistJSON([]string{"status", "--json"}); !j || len(rest) != 1 || rest[0] != "status" {
		t.Fatalf("post-position: json=%v rest=%v", j, rest)
	}
	if j, rest := hoistJSON([]string{"status"}); j || len(rest) != 1 {
		t.Fatalf("absent: json=%v rest=%v", j, rest)
	}
}

// TestMetadataEmitsValidControlJSON is the contract proof: the bytes that
// `metadata --json` would print must parse back into a control.Manifest with
// the right schema version.
func TestMetadataEmitsValidControlJSON(t *testing.T) {
	// Build the manifest the same way cmdMetadata does and check the wire shape.
	var m control.Manifest
	b, err := json.Marshal(control.NewManifest("instacrawl", "instacrawl", "instacrawl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != control.SchemaVersion {
		t.Fatalf("schema_version = %q", m.SchemaVersion)
	}
}
