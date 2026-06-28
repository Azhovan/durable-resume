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

// alreadyComplete reports whether the FINAL resolved path opts.Output already
// holds a verifiably-complete download, so Run may skip without fetching the body.
// It is called AFTER probe and AFTER resolveOutputPath, so opts.Output is the
// final path and info.size is the probed size (-1 when unknown). It NEVER returns
// a fatal error: anything that prevents proving completeness (force, missing file,
// non-regular file, stat/read error, checksum mismatch, size mismatch, unknown
// size with no checksum) yields false so the caller downloads normally. A matching
// checksum is authoritative and takes precedence over size, so a file that matches
// the checksum skips even when the probed size is unknown or differs — this is
// intended; do not add a size cross-check to the checksum branch.
func alreadyComplete(opts Options, info remoteInfo) (bool, string) {
	if opts.Force {
		return false, "force"
	}
	fi, err := os.Stat(opts.Output)
	if err != nil {
		// Not-exists or any stat error => cannot prove complete => download.
		return false, "absent"
	}
	if !fi.Mode().IsRegular() {
		return false, "not a regular file"
	}
	switch {
	case !opts.Checksum.Empty():
		// Strongest signal. A mismatch or read error => not complete => download
		// (never fatal). verifyChecksum opens/streams the existing final file and
		// returns nil only on a true match.
		if cErr := verifyChecksum(opts.Output, opts.Checksum); cErr != nil {
			return false, fmt.Sprintf("checksum not satisfied: %v", cErr)
		}
		return true, "checksum matches"
	case info.size > 0:
		// Known size, no checksum: on-disk size must equal the probed size.
		if fi.Size() != info.size {
			return false, fmt.Sprintf("size %d != probed %d", fi.Size(), info.size)
		}
		return true, "size matches"
	default:
		// Unknown size (info.size <= 0) AND no checksum: cannot prove complete.
		return false, "size unknown and no checksum; cannot prove complete"
	}
}
