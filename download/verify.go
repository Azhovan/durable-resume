package download

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// verifySize returns ErrSizeMismatch unless f's on-disk size equals expected.
// A non-positive expected (unknown size) is treated as a skip and returns nil.
func verifySize(f *os.File, expected int64) error {
	if expected <= 0 {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("verify size: %w", err)
	}
	if info.Size() != expected {
		return fmt.Errorf("verify size: got %d, want %d: %w", info.Size(), expected, ErrSizeMismatch)
	}
	return nil
}

// verifyChecksum streams sha256 over the file at path and compares to want.
// A no-op (nil) when want.Empty().
func verifyChecksum(path string, want Checksum) error {
	if want.Empty() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	buf := make([]byte, copyBufferSize)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))

	if !strings.EqualFold(got, want.Hex) {
		return fmt.Errorf("verify checksum: got %s, want %s: %w", got, strings.ToLower(want.Hex), ErrChecksumMismatch)
	}
	return nil
}
