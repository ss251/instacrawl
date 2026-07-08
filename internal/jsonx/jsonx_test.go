package jsonx

import (
	"encoding/json"
	"testing"
)

func TestStrUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"number", `123456789012345`, "123456789012345"},
		{"quoted number", `"123456789012345"`, "123456789012345"},
		{"quoted string", `"abc"`, "abc"},
		{"null", `null`, "null"},
		{"empty string", `""`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s Str
			if err := json.Unmarshal([]byte(c.in), &s); err != nil {
				t.Fatalf("unmarshal %q: %v", c.in, err)
			}
			if string(s) != c.want {
				t.Fatalf("got %q, want %q", string(s), c.want)
			}
		})
	}
}

func TestI64Unmarshal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
	}{
		{"number", `123456789012345`, 123456789012345},
		{"quoted number", `"123456789012345"`, 123456789012345},
		{"negative", `-42`, -42},
		{"null", `null`, 0},
		{"empty string", `""`, 0},
		{"malformed", `"not-a-number"`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v I64
			if err := json.Unmarshal([]byte(c.in), &v); err != nil {
				t.Fatalf("unmarshal %q: %v", c.in, err)
			}
			if int64(v) != c.want {
				t.Fatalf("got %d, want %d", int64(v), c.want)
			}
		})
	}
}

// TestI64BigIntSafe confirms a big pk-shaped id round-trips exactly — the
// entire reason I64 exists instead of routing through float64, which loses
// precision above 2^53.
func TestI64BigIntSafe(t *testing.T) {
	const big = `9007199254740993` // 2^53 + 1, not exactly representable as float64
	var v I64
	if err := json.Unmarshal([]byte(big), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if int64(v) != 9007199254740993 {
		t.Fatalf("big int lost precision: got %d", int64(v))
	}
}

// TestStructUsage confirms both types work as struct fields decoded via
// encoding/json, which is how every caller actually uses them.
func TestStructUsage(t *testing.T) {
	var payload struct {
		PK  Str `json:"pk"`
		Cnt I64 `json:"cnt"`
	}
	body := []byte(`{"pk":"123","cnt":7}`)
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PK != "123" || payload.Cnt != 7 {
		t.Fatalf("got %+v", payload)
	}
}
