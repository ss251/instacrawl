package carve

import "testing"

func TestSniffKind(t *testing.T) {
	// header builds a byte slice from a magic prefix, padded to SniffPrefixLen.
	header := func(prefix ...byte) []byte {
		b := make([]byte, SniffPrefixLen)
		copy(b, prefix)
		return b
	}
	ftyp := func(brand string) []byte {
		return header(0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', brand[0], brand[1], brand[2], brand[3])
	}
	cases := []struct {
		desc string
		in   []byte
		want string
	}{
		{"jpeg", header(0xFF, 0xD8, 0xFF, 0xE0), KindImage},
		{"png", header(0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A), KindImage},
		{"gif87a", header('G', 'I', 'F', '8', '7', 'a'), KindImage},
		{"gif89a", header('G', 'I', 'F', '8', '9', 'a'), KindImage},
		{"webp", header('R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'), KindImage},
		{"heic", ftyp("heic"), KindImage},
		{"heif mif1", ftyp("mif1"), KindImage},
		{"avif", ftyp("avif"), KindImage},
		{"mp4 isom", ftyp("isom"), KindVideo},
		{"mp4 mp42", ftyp("mp42"), KindVideo},
		{"quicktime", ftyp("qt  "), KindVideo},
		{"unknown", header(0x00, 0x01, 0x02, 0x03), ""},
		{"empty", nil, ""},
		{"too short jpeg", []byte{0xFF, 0xD8}, ""},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := SniffKind(c.in); got != c.want {
				t.Fatalf("SniffKind(%s) = %q, want %q", c.desc, got, c.want)
			}
		})
	}
}
