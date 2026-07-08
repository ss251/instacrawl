package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ss251/instacrawl/internal/ig"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestUpsertAccountCommitsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	fixed := func() time.Time { return time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC) }
	a := &ig.Account{PK: "17841400000000000", Username: "testuser", FullName: "Test User", IsPrivate: false, RawJSON: `{"ok":true}`}

	// First commit — proves the OK(1) → commit path.
	if err := st.UpsertAccount(ctx, a, "web", fixed); err != nil {
		t.Fatal(err)
	}
	got, found, err := st.GetAccount(ctx, a.PK)
	if err != nil || !found {
		t.Fatalf("account not found after upsert: found=%v err=%v", found, err)
	}
	if got.Username != "testuser" || got.RawJSON != `{"ok":true}` {
		t.Fatalf("row mismatch: %+v", got)
	}

	// Second upsert of the same pk must NOT create a duplicate.
	if err := st.UpsertAccount(ctx, a, "web", fixed); err != nil {
		t.Fatal(err)
	}
	n, err := st.CountAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 account row after two upserts, got %d", n)
	}
}

func TestUpsertAccountUpdatesChangedFields(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	clk := func() time.Time { return time.Unix(0, 0).UTC() }

	a := &ig.Account{PK: "1", Username: "old", IsPrivate: false}
	if err := st.UpsertAccount(ctx, a, "web", clk); err != nil {
		t.Fatal(err)
	}
	a2 := &ig.Account{PK: "1", Username: "new", IsPrivate: true}
	if err := st.UpsertAccount(ctx, a2, "web", clk); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.GetAccount(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "new" || !got.IsPrivate {
		t.Fatalf("update not applied: %+v", got)
	}
}

func TestGetAccountAbsent(t *testing.T) {
	st := openTemp(t)
	_, found, err := st.GetAccount(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected not found")
	}
}
