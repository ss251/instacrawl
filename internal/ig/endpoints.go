package ig

// ==========================================================================
// THE FRAGILE FILE. This is the single place that encodes a live guess about
// Instagram's current API surface. Everything else in this package is a pure
// state machine over RawResponse and is validated offline against fixtures.
//
// When the live gate (1b) reveals the real shape, retune the URL and the
// required-key contract HERE — the classifier and its tests do not change.
// ==========================================================================

// WebHost is the web-lane host. The web session's cookies are scoped to
// instagram.com; using the www host (not i.instagram.com) keeps the request in
// the browser-session lane rather than the mobile-app lane.
const WebHost = "https://www.instagram.com"

// SelfProfilePath is the read-only "who am I" endpoint. users/{id}/info returns
// the account's own public profile — deliberately NOT accounts/edit/, which
// leaks email/phone into the body we persist as raw_json.
const SelfProfilePath = "/api/v1/users/%s/info/"

// SelfProfileURL builds the probe URL for a given ds_user_id.
func SelfProfileURL(dsUserID string) string {
	return WebHost + "/api/v1/users/" + dsUserID + "/info/"
}

// The required-key contract: a 200 whose JSON lacks a parseable user.pk is
// treated as SCHEMA·DRIFT, not silently accepted. Documented here (rather than
// scattered in classify.go) so the contract is auditable in one place.
//
//	{ "user": { "pk": <id>, "username": "...", ... }, "status": "ok" }
//
// Required: user.pk (identity) and user.username (proof the body is a profile).
