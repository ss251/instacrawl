// Package jsonx provides lenient, big-int-safe JSON scalar types shared across
// the ig/dm/feed domain packages. Instagram's numeric ids (pk, user_id, ...)
// are sometimes emitted as a JSON number and sometimes as a quoted string, and
// they can exceed float64's exact-integer range — so these types decode
// straight from the raw bytes, NEVER through float64, and degrade a
// malformed/renamed field to a zero value rather than failing the whole
// batch. This is the same lenient-parsing philosophy used throughout the
// codebase: a bad field should never sink an otherwise-good page.
package jsonx

import (
	"strconv"
	"strings"
)

// Str accepts a JSON number or string and yields the string form (big-int
// safe: never routes an id through float64).
type Str string

// UnmarshalJSON implements json.Unmarshaler. It never returns an error —
// malformed input degrades to the empty string.
func (j *Str) UnmarshalJSON(b []byte) error {
	*j = Str(strings.Trim(strings.TrimSpace(string(b)), `"`))
	return nil
}

// I64 accepts a JSON number or numeric string and yields int64.
type I64 int64

// UnmarshalJSON implements json.Unmarshaler. It never returns an error —
// malformed input degrades to 0 rather than failing the batch.
func (j *I64) UnmarshalJSON(b []byte) error {
	*j = 0
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	// Lenient: a malformed number degrades to 0 rather than failing the batch.
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*j = I64(v)
	}
	return nil
}
