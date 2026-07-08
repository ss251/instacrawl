package feed

import "strings"

// laneRank orders the source lanes for the final reconciler tie-break. Higher
// wins. Mirrors dm's lane semantics exactly — the same web/mobile/carve
// capture lanes apply across the whole crawler, not just DMs.
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

// MediaFieldCount counts the non-empty identifying/content fields of a media
// item — richness for the media upsert tie-break. A stub carrying only ids
// scores 0 and must never clobber a stored media row that already carries a
// code, caption, or downloaded files.
func MediaFieldCount(code, captionText string, hasFiles bool) int {
	n := 0
	if strings.TrimSpace(code) != "" {
		n++
	}
	if strings.TrimSpace(captionText) != "" {
		n++
	}
	if hasFiles {
		n++
	}
	return n
}

// MediaTier picks the winning Media by the named tie-break:
//  1. more non-empty identifying fields (MediaFieldCount),
//  2. tie → the more recent capture,
//  3. tie → lane order web > mobile > carve.
//
// recencyCmp is the sign of (incoming.fetched_at − existing.fetched_at): > 0
// when incoming was captured more recently, 0 when equal, < 0 when incoming is
// strictly older. Recency is applied BEFORE lane (step 2 before step 3), so a
// strictly-older incoming loses on recency even if its lane outranks — an older
// cache carve can never displace a fresher web row of equal richness.
//
// It is pure and testable; the store applies exactly this logic (with fetched_at
// as the recency signal) inside its upsert WHERE clause, and a cross-check test
// (TestMediaTierSQLMatchesGo) asserts the two never drift apart.
func MediaTier(existing, incoming Media, recencyCmp int) Media {
	existingCount := MediaFieldCount(existing.Code, existing.CaptionText, len(existing.Files) > 0)
	incomingCount := MediaFieldCount(incoming.Code, incoming.CaptionText, len(incoming.Files) > 0)
	switch {
	case incomingCount > existingCount:
		return incoming
	case incomingCount < existingCount:
		return existing
	}
	// Equal field counts: the more recent capture wins, before lane is consulted.
	switch {
	case recencyCmp > 0:
		return incoming
	case recencyCmp < 0:
		return existing
	}
	// Same recency: highest lane wins.
	if laneRank(incoming.Source) > laneRank(existing.Source) {
		return incoming
	}
	return existing
}
