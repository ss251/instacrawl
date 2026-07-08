package main

import (
	"testing"

	"github.com/ss251/instacrawl/internal/appmeta"
)

// These carve invocations must all fail during argument validation — BEFORE the
// archive is opened — so they run hermetically without touching ~/.instacrawl.
// Exit code 2 is the usage-error contract (see report()).
func TestCarveUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no --dir", []string{"carve", "--dry-run"}},
		{"unknown --format", []string{"carve", "--dir", "/tmp/whatever", "--format", "leveldb"}},
		{"positional arg", []string{"carve", "--dir", "/tmp/x", "extra"}},
		{"refuses scanning own media store", []string{"carve", "--dir", appmeta.MediaDir()}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(c.args); got != 2 {
				t.Fatalf("run(%v) = %d, want 2 (usage error)", c.args, got)
			}
		})
	}
}
