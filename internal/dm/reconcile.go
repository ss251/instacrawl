package dm

import "strings"

// laneRank orders the source lanes for the final reconciler tie-break. Higher
// wins. Phase 2 is web-only, so this only matters once mobile/carve land.
func laneRank(source string) int {
	switch source {
	case "web":
		return 3
	case "mobile":
		return 2
	case "carve":
		return 1
	default:
		return 0
	}
}

// LaneRank is the exported rank used by the store's tiering SQL/logic.
func LaneRank(source string) int { return laneRank(source) }

// FieldCount counts the non-empty identifying fields of a profile. A
// numeric-ID-only stub scores 0 and must never clobber a stored profile that
// carries a username/full name.
func FieldCount(username, fullName, profilePicURL string) int {
	n := 0
	if strings.TrimSpace(username) != "" {
		n++
	}
	if strings.TrimSpace(fullName) != "" {
		n++
	}
	if strings.TrimSpace(profilePicURL) != "" {
		n++
	}
	return n
}

// Tier picks the winning participant profile by the named tie-break:
//  1. more non-null identifying fields (FieldCount),
//  2. tie → the more recent capture (caller passes incomingNewer),
//  3. tie → lane order web > mobile > carve.
//
// It is pure and testable; the store applies it (with fetched_at as the
// recency signal) inside its upsert.
func Tier(existing, incoming Participant, incomingNewer bool) Participant {
	switch {
	case incoming.FieldCount > existing.FieldCount:
		return incoming
	case incoming.FieldCount < existing.FieldCount:
		return existing
	}
	// Equal field counts: prefer the more recent capture.
	if incomingNewer {
		return incoming
	}
	// Same recency: highest lane wins.
	if laneRank(incoming.Source) > laneRank(existing.Source) {
		return incoming
	}
	return existing
}
