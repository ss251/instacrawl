package carve

// BuildIndex groups bytes-less media_file refs by their CDN-basename attribution
// key, so a carved artifact can be attributed in O(1). Refs whose OriginalURL
// yields no plausible key are dropped (they are unattributable by filename and
// will simply stay unrecovered — never mis-attributed).
//
// A key can map to several refs: the same content is legitimately referenced by
// more than one media_file (e.g. an own post and its saved copy share a pk, or
// two size renditions resolve to the same stable name). carvesync fills every
// ref under a matched key, which is the byte-dedup working, not a collision.
func BuildIndex(refs []FileRef) map[string][]FileRef {
	idx := make(map[string][]FileRef, len(refs))
	for _, r := range refs {
		k := CanonicalKey(r.OriginalURL)
		if k == "" {
			continue
		}
		idx[k] = append(idx[k], r)
	}
	return idx
}
