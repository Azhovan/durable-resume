package cmd

import (
	"fmt"
	"strconv"
	"strings"
)

// parseRate parses a wget/curl-style --limit-rate value into a bytes-per-second
// cap. Grammar (case-insensitive, surrounding space trimmed, NO internal space):
//
//	<number><unit>
//	number : a non-negative decimal integer or fraction (e.g. "100000", "1.5")
//	unit   : ""  | "b"            => bytes
//	         "k" | "kib"          => * 1024            (KiB; binary, matches wget)
//	         "m" | "mib"          => * 1024*1024       (MiB)
//	         "g" | "gib"          => * 1024*1024*1024  (GiB)
//
// All multipliers are 1024-based (binary), matching wget --limit-rate and the
// project's binary formatBytes. The result is floored to int64 bytes/sec.
//
//	""       -> 0 (unset => unlimited)
//	"0"/"0k" -> 0 (unlimited)
//	"100000" -> 100000
//	"1024"   -> 1024
//	"1024B"  -> 1024
//	"500k"   -> 512000
//	"1M"     -> 1048576
//	"1.5M"   -> 1572864
//	"1MiB"   -> 1048576
//	"1KiB"   -> 1024
//	"1G"     -> 1073741824
//
// A negative number, a unit with no number ("k"), an unknown unit ("1x"), or
// non-numeric garbage ("abc", "1.2.3", "1 M") returns a non-nil error.
func parseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil // unset => unlimited
	}
	lower := strings.ToLower(s)

	// Split trailing alphabetic unit letters from the leading numeric part.
	i := len(lower)
	for i > 0 {
		c := lower[i-1]
		if (c >= '0' && c <= '9') || c == '.' {
			break
		}
		i--
	}
	numPart, unit := lower[:i], lower[i:]
	if numPart == "" {
		return 0, fmt.Errorf("invalid --limit-rate %q: missing number", s)
	}

	var mult float64
	switch unit {
	case "", "b":
		mult = 1
	case "k", "kib":
		mult = 1024
	case "m", "mib":
		mult = 1024 * 1024
	case "g", "gib":
		mult = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("invalid --limit-rate %q: unknown unit %q", s, unit)
	}

	val, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid --limit-rate %q: %w", s, err)
	}
	if val < 0 {
		return 0, fmt.Errorf("invalid --limit-rate %q: must not be negative", s)
	}
	bytes := val * mult
	return int64(bytes), nil // floors; "0"/"0k" => 0 (unlimited)
}
