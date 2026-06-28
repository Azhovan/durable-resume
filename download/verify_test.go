package download

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTempFile writes content to a temp file and returns its path.
func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.bin")
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVerifySize(t *testing.T) {
	content := []byte("hello, durable resume")

	tests := []struct {
		name     string
		expected int64
		wantErr  error
	}{
		{name: "equal", expected: int64(len(content)), wantErr: nil},
		{name: "off by one", expected: int64(len(content)) + 1, wantErr: ErrSizeMismatch},
		{name: "expected zero skips", expected: 0, wantErr: nil},
		{name: "expected negative skips", expected: -1, wantErr: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, content)
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()

			err = verifySize(f, tt.expected)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestVerifyChecksum(t *testing.T) {
	content := []byte("the quick brown fox jumps over the lazy dog")
	good := sha256Hex(content)

	tests := []struct {
		name    string
		want    Checksum
		wantErr error
	}{
		{
			name:    "matching",
			want:    Checksum{Algo: "sha256", Hex: good},
			wantErr: nil,
		},
		{
			name:    "matching uppercase hex",
			want:    Checksum{Algo: "sha256", Hex: hexUpper(good)},
			wantErr: nil,
		},
		{
			name:    "mismatch",
			want:    Checksum{Algo: "sha256", Hex: sha256Hex([]byte("different"))},
			wantErr: ErrChecksumMismatch,
		},
		{
			name:    "empty skips",
			want:    Checksum{},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, content)

			err := verifyChecksum(path, tt.want)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func hexUpper(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'f' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
