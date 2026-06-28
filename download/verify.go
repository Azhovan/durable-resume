package download

import "os"

// verifySize returns ErrSizeMismatch unless f's on-disk size equals expected.
// A non-positive expected (unknown size) is treated as a skip and returns nil.
func verifySize(f *os.File, expected int64) error {
	panic("not implemented")
}

// verifyChecksum streams sha256 over the file at path and compares to want.
// A no-op (nil) when want.Empty().
func verifyChecksum(path string, want Checksum) error {
	panic("not implemented")
}
