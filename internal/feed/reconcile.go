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
//  2. tie → the more recent capture (caller passes incomingNewer),
//  3. tie → lane order web > mobile > carve.
//
// It is pure and testable; the store applies it (with fetched_at as the
// recency signal) inside its upsert.
func MediaTier(existing, incoming Media, incomingNewer bool) Media {
	existingCount := MediaFieldCount(existing.Code, existing.CaptionText, len(existing.Files) > 0)
	incomingCount := MediaFieldCount(incoming.Code, incoming.CaptionText, len(incoming.Files) > 0)
	switch {
	case incomingCount > existingCount:
		return incoming
	case incomingCount < existingCount:
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
