package store

import (
	"strconv"
	"testing"
)

func gcdUint64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func TestAvatarCodeForID(t *testing.T) {
	if g := gcdUint64(avatarCodeMult, avatarCodeMod); g != 1 {
		t.Fatalf("gcd(mult, mod) = %d, want 1", g)
	}
	for _, bad := range []string{"", "0", "-1", "abc", "12a"} {
		if got := AvatarCodeForID(bad); got != "" {
			t.Errorf("AvatarCodeForID(%q) = %q, want empty", bad, got)
		}
	}
	seen := make(map[string]uint64, 50_000)
	for id := uint64(1); id <= 50_000; id++ {
		code := AvatarCodeForID(strconv.FormatUint(id, 10))
		if !IsAvatarCode(code) {
			t.Fatalf("id %d -> %q is not AA-DDDD-DDDD", id, code)
		}
		if prev, dup := seen[code]; dup {
			t.Fatalf("collision: id %d and %d both produced %q", prev, id, code)
		}
		seen[code] = id
	}
	// Deterministic and large-ID safe (no uint64 overflow).
	for _, aid := range []string{"10", "752", "6137", "100000", "18446744073709551615"} {
		a := AvatarCodeForID(aid)
		b := AvatarCodeForID(aid)
		if !IsAvatarCode(a) || a != b {
			t.Errorf("AvatarCodeForID(%q) = %q / %q", aid, a, b)
		}
	}
}

func TestNormalizeAvatarCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"DW-0345-1404", "DW03451404"},
		{"dw-0345-1404", "DW03451404"},
		{" dw 0345 1404 ", "DW03451404"},
		{"10-01", "1001"},
		{"-2", "2"},
	}
	for _, tc := range cases {
		if got := NormalizeAvatarCode(tc.in); got != tc.want {
			t.Errorf("NormalizeAvatarCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
