package store

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"
)

// AvatarCode format in LINE PLAY is AA-DDDD-DDDD (two uppercase ASCII letters,
// hyphen, four digits, hyphen, four digits — e.g. DW-0345-1404).
// NaAddFriendSearchLayer::CheckCodeNameFormat (0x1c5c228) and
// NaAddFriendSearchLayer::RequestSearchFriend (0x1cd8fb0) strip hyphens,
// truncate to 10 characters, and insert hyphens at offsets 2 and 7.
//
// Cherry derives each avatar's code deterministically from its numeric aid
// using a bijective affine permutation modulo 26*26*10^8 (67,600,000,000),
// guaranteeing zero collisions across all valid aids without a schema change.
const (
	avatarCodeMod  uint64 = 26 * 26 * 100_000_000 // 67,600,000,000 = 2^8 * 5^8 * 13^2
	avatarCodeMult uint64 = 23_148_917_237        // coprime to 2, 5, 13 (gcd == 1)
	avatarCodeAdd  uint64 = 14_729_381_503        // offset so small aids spread uniformly
)

// AvatarCodeForID returns the deterministic AA-DDDD-DDDD Avatar Code for a
// positive decimal avatar id, or "" if aid is not a valid positive integer.
func AvatarCodeForID(aid string) string {
	id, err := strconv.ParseUint(aid, 10, 64)
	if err != nil || id == 0 {
		return ""
	}
	hi, lo := bits.Mul64(id%avatarCodeMod, avatarCodeMult)
	_, rem := bits.Div64(hi, lo, avatarCodeMod)
	v := (rem + avatarCodeAdd) % avatarCodeMod
	letters := v / 100_000_000
	digits := v % 100_000_000
	return fmt.Sprintf("%c%c-%04d-%04d", 'A'+byte(letters/26), 'A'+byte(letters%26), digits/10_000, digits%10_000)
}

// NormalizeAvatarCode strips hyphens and whitespace and uppercases ASCII
// letters so typed codes ("dw-0345-1404", "DW03451404", "10-01") compare cleanly.
func NormalizeAvatarCode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' || c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// IsAvatarCode reports whether s has the exact AA-DDDD-DDDD format.
func IsAvatarCode(s string) bool {
	if len(s) != 12 || s[2] != '-' || s[7] != '-' {
		return false
	}
	if s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' {
		return false
	}
	for _, i := range []int{3, 4, 5, 6, 8, 9, 10, 11} {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
