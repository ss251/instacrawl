package ig

// ==========================================================================
// THE FRAGILE FILE — the single place that encodes the live Instagram API
// surface. LIVE-VALIDATED 2026-07-09: /api/v1/users/{id}/info/ returns
// {user:{pk, username, full_name, is_private, profile_pic_url}, status} exactly
// as the classifier parses it. Everything else in this package is a pure state
// machine over RawResponse, validated offline against fixtures. If Instagram
// changes the shape, retune the URL + required-key contract HERE — the
// classifier and its tests do not change.
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
