package download

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"dot", ".", ""},
		{"dotdot", "..", ""},
		{"posix traversal", "../../etc/passwd", "passwd"},
		{"windows traversal", `..\..\etc\passwd`, "passwd"},
		{"absolute", "/etc/passwd", "passwd"},
		{"nested path", "a/b/c.txt", "c.txt"},
		{"nul byte", "name\x00.iso", ""},
		{"trimmed", "  ok.tar.gz  ", "ok.tar.gz"},
		{"normal", "normal.bin", "normal.bin"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeFilename(tt.in))
		})
	}
}

func TestFilenameFromContentDisposition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"plain filename", `attachment; filename="ubuntu-24.04.iso"`, "ubuntu-24.04.iso", true},
		{"rfc5987 utf8 naive", `attachment; filename*=UTF-8''na%C3%AFve%20file.txt`, "naïve file.txt", true},
		{"rfc5987 utf8 euro", `attachment; filename*=UTF-8''%e2%82%ac-rates.txt`, "€-rates.txt", true},
		{"filename star wins", `attachment; filename="ascii.txt"; filename*=UTF-8''na%C3%AFve.txt`, "naïve.txt", true},
		{"inline no name", `inline`, "", false},
		{"empty", "", "", false},
		{"malformed empty filename", `attachment; filename=`, "", false},
		{"raw traversal not sanitized", `attachment; filename="../../etc/passwd"`, "../../etc/passwd", true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := filenameFromContentDisposition(tt.in)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUrlBasename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"query path basename", "https://h/download?id=8412", "download"},
		{"artifact latest", "https://h/api/artifact/latest", "latest"},
		{"file with query", "https://h/dir/file.iso?token=x", "file.iso"},
		{"trailing slash", "https://h/path/", ""},
		{"root", "https://h/", ""},
		{"no path", "https://h", ""},
		{"encoded dotdot", "https://h/%2e%2e", ".."},
		{"nested encoded dotdot", "https://h/sub/%2e%2e", ".."},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, urlBasename(tt.in))
		})
	}
}

func TestDerivedName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		info        remoteInfo
		fallbackURL string
		want        string
	}{
		{
			name: "cd wins over url",
			info: remoteInfo{contentDisposition: `attachment; filename="ubuntu.iso"`, finalURL: "https://cdn/blob"},
			want: "ubuntu.iso",
		},
		{
			name: "final url basename when no cd",
			info: remoteInfo{finalURL: "https://cdn/ubuntu-24.04.iso?token=x"},
			want: "ubuntu-24.04.iso",
		},
		{
			name:        "fallback url when final empty",
			info:        remoteInfo{finalURL: ""},
			fallbackURL: "https://h/download?id=8412",
			want:        "download",
		},
		{
			name: "cd traversal reduces to basename and beats url",
			info: remoteInfo{contentDisposition: `attachment; filename="../../etc/passwd"`, finalURL: "https://h/x.iso"},
			want: "passwd",
		},
		{
			name:        "fallback name when nothing usable",
			info:        remoteInfo{contentDisposition: `inline`, finalURL: ""},
			fallbackURL: "https://h/",
			want:        "download",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, derivedName(tt.info, tt.fallbackURL))
		})
	}
}

func TestResolveOutputPath(t *testing.T) {
	t.Parallel()

	t.Run("explicit non-dir path verbatim ignores cd", func(t *testing.T) {
		t.Parallel()
		got, err := resolveOutputPath("out.bin", remoteInfo{contentDisposition: `attachment; filename="other.iso"`}, "https://h/x")
		assert.NoError(t, err)
		assert.Equal(t, "out.bin", got)
	})

	t.Run("relative path verbatim", func(t *testing.T) {
		t.Parallel()
		got, err := resolveOutputPath("../rel/path", remoteInfo{}, "https://h/x")
		assert.NoError(t, err)
		assert.Equal(t, "../rel/path", got)
	})

	t.Run("existing dir joins derived cd name", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got, err := resolveOutputPath(dir, remoteInfo{contentDisposition: `attachment; filename="real.iso"`}, "https://h/x")
		assert.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "real.iso"), got)
	})

	t.Run("existing dir cd traversal stays inside dir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		got, err := resolveOutputPath(dir, remoteInfo{contentDisposition: `attachment; filename="../../evil"`}, "https://h/x")
		assert.NoError(t, err)
		assert.Equal(t, dir, filepath.Dir(got))
		assert.Equal(t, "evil", filepath.Base(got))
	})

	t.Run("empty requested cd wins", func(t *testing.T) {
		t.Parallel()
		got, err := resolveOutputPath("", remoteInfo{contentDisposition: `attachment; filename="foo.bin"`, finalURL: "https://h/url.bin"}, "https://h/url.bin")
		assert.NoError(t, err)
		assert.Equal(t, "foo.bin", got)
	})

	t.Run("empty requested falls back to download", func(t *testing.T) {
		t.Parallel()
		got, err := resolveOutputPath("", remoteInfo{finalURL: "https://h/"}, "https://h/")
		assert.NoError(t, err)
		assert.Equal(t, "download", got)
	})
}
