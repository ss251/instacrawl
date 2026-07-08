package carve

import "testing"

func TestBuildIndex(t *testing.T) {
	const a = "111111111_17862345678901234_n.jpg"
	const b = "222222222_17862345678901299_n.mp4"
	refs := []FileRef{
		{MediaPK: "m1", Kind: "image", Position: 0, OriginalURL: "https://cdn/v/t51/e35/s1080x1080/" + a + "?oe=1"},
		{MediaPK: "m2", Kind: "image", Position: 0, OriginalURL: "https://cdn/v/t51/" + a + "?oe=2"}, // same content, different ref
		{MediaPK: "m3", Kind: "video", Position: 0, OriginalURL: "https://cdn/v/" + b},
		{MediaPK: "m4", Kind: "image", Position: 0, OriginalURL: "https://cdn/image.jpg"}, // unattributable → dropped
		{MediaPK: "m5", Kind: "image", Position: 0, OriginalURL: ""},                      // no url → dropped
	}
	idx := BuildIndex(refs)

	if len(idx) != 2 {
		t.Fatalf("expected 2 distinct keys, got %d: %v", len(idx), keys(idx))
	}
	if got := len(idx[a]); got != 2 {
		t.Fatalf("the shared content key should map to 2 refs, got %d", got)
	}
	if got := len(idx[b]); got != 1 {
		t.Fatalf("the video key should map to 1 ref, got %d", got)
	}
	if _, ok := idx["image.jpg"]; ok {
		t.Fatal("unattributable ref must not be indexed")
	}
}

func keys(m map[string][]FileRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
