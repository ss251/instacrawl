package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ss251/instacrawl/internal/appmeta"
	"github.com/ss251/instacrawl/internal/store"
)

// reparseStoreFn is the injectable store-open seam (tests point it at a temp DB).
var reparseStoreFn = store.Open

// cmdReparse re-decodes messages archived with an unrecognized item_type from
// their retained raw JSON through the current parser, upgrading any the parser
// now understands — with NO network access (DECISIONS §4B). Run it after a
// version that adds support for a message type the drift signal surfaced.
func cmdReparse(args []string) error {
	fs := newAuthFlags("reparse")
	if err := fs.Parse(args); err != nil {
		return usageError{msg: err.Error()}
	}
	if fs.NArg() > 0 {
		return usageError{msg: fmt.Sprintf("reparse takes no arguments, got %v", fs.Args())}
	}

	dbPath := appmeta.DatabasePath()
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "instacrawl: no archive yet — run `instacrawl sync` first")
		return errReported
	}

	ctx := context.Background()
	st, err := reparseStoreFn(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = st.Close() }()

	upgraded, eligible, err := st.ReparseUnknownMessages(ctx)
	if err != nil {
		return fmt.Errorf("reparse: %w", err)
	}
	if eligible == 0 {
		fmt.Println("Nothing to re-parse — every archived message type is recognized.")
		return nil
	}
	fmt.Printf("Re-parsed %d of %d previously-unrecognized message(s).\n", upgraded, eligible)
	if remaining := eligible - upgraded; remaining > 0 {
		fmt.Printf("%d still use item types this build doesn't recognize (their raw JSON is kept).\n", remaining)
	}
	return nil
}
