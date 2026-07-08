package carve

import "bytes"

// Media kinds, matching internal/feed's MediaFile.Kind strings so a sniffed kind
// can be compared directly against a stored media_file.kind.
const (
	KindImage = "image"
	KindVideo = "video"
)

// SniffPrefixLen is how many leading bytes SniffKind needs. A caller reads at
// most this many bytes before deciding; the ISO-BMFF (`ftyp`) brand check is the
// deepest probe, at 12 bytes.
const SniffPrefixLen = 32

// SniffKind classifies media bytes by magic number into KindImage, KindVideo,
// or "" (unrecognized). It reads only the header, never the whole file. The
// carve lane uses it to reject a filename collision — bytes whose sniffed kind
// disagrees with the stored media_file.kind are skipped, not stored.
func SniffKind(p []byte) string {
	switch {
	case len(p) >= 3 && p[0] == 0xFF && p[1] == 0xD8 && p[2] == 0xFF:
		return KindImage // JPEG
	case len(p) >= 8 && bytes.Equal(p[:8], pngMagic):
		return KindImage // PNG
	case len(p) >= 6 && (bytes.HasPrefix(p, gif87a) || bytes.HasPrefix(p, gif89a)):
		return KindImage // GIF
	case len(p) >= 12 && bytes.Equal(p[:4], riffMagic) && bytes.Equal(p[8:12], webpMagic):
		return KindImage // WEBP (RIFF....WEBP)
	case len(p) >= 12 && bytes.Equal(p[4:8], ftypMagic):
		// ISO base media format: image (HEIC/HEIF/AVIF) vs video (MP4/MOV/…),
		// discriminated by the major brand at bytes 8..12.
		if isImageBrand(p[8:12]) {
			return KindImage
		}
		return KindVideo
	default:
		return ""
	}
}

var (
	pngMagic  = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	gif87a    = []byte("GIF87a")
	gif89a    = []byte("GIF89a")
	riffMagic = []byte("RIFF")
	webpMagic = []byte("WEBP")
	ftypMagic = []byte("ftyp")
)

// isImageBrand reports whether an ISO-BMFF major brand denotes a still image
// (HEIC/HEIF/AVIF family) rather than video. Everything else with an `ftyp` box
// (isom, mp41/42, M4V, qt, dash, …) is treated as video.
func isImageBrand(brand []byte) bool {
	switch string(brand) {
	case "heic", "heix", "heim", "heis", "hevc", "hevx",
		"mif1", "msf1", "avif", "avis":
		return true
	default:
		return false
	}
}
