//go:build manual

package browsercookie

import (
	"context"
	"testing"
)

// TestLiveFindReal exercises the REAL kooky path against the owner's actual
// browser cookie stores and OS keychain. It is excluded from normal builds and
// CI by the `manual` build tag; run it explicitly:
//
//	go test -tags manual ./internal/browsercookie/ -run TestLiveFindReal -v
//
// A macOS keychain consent prompt may appear the first time — click Allow. It
// prints only NON-SECRET metadata (browser, profile, ds_user_id, cookie count);
// it never prints a cookie value. This is the live acceptance check for the
// decryption path that unit tests (which use a fake Source) cannot cover.
func TestLiveFindReal(t *testing.T) {
	cands, err := NewKookySource().Find(context.Background(), "auto")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(cands) == 0 {
		t.Log("no logged-in instagram.com session found in any supported browser")
		return
	}
	for _, c := range cands {
		t.Logf("found session: browser=%s profile=%q ds_user_id=%s cookies=%d",
			c.Browser, c.Profile, c.DSUserID, len(c.Cookies))
	}
}
