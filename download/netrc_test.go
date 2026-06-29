package download

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNetrc(t *testing.T) {
	t.Run("single machine entry", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader("machine example.com login alice password secret"))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "alice", password: "secret"}, m["example.com"])
	})

	t.Run("default stored under empty key", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader("default login d password dp"))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "d", password: "dp"}, m[""])
	})

	t.Run("comments ignored full-line and trailing", func(t *testing.T) {
		src := "# leading comment\nmachine h login u password p # trailing\n"
		m, err := parseNetrc(strings.NewReader(src))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "u", password: "p"}, m["h"])
	})

	t.Run("macdef body skipped and following machine parsed", func(t *testing.T) {
		src := "macdef init\n  put file\n  bye\n\nmachine h login u password p\n"
		m, err := parseNetrc(strings.NewReader(src))
		require.NoError(t, err)
		// "put", "file", "bye" must NOT become tokens/entries.
		assert.Equal(t, netrcEntry{login: "u", password: "p"}, m["h"])
		assert.Len(t, m, 1)
	})

	t.Run("later machine overrides earlier", func(t *testing.T) {
		src := "machine h login old password oldp\nmachine h login new password newp\n"
		m, err := parseNetrc(strings.NewReader(src))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "new", password: "newp"}, m["h"])
	})

	t.Run("login-only and password-only entries", func(t *testing.T) {
		src := "machine a login onlyuser\nmachine b password onlypass\n"
		m, err := parseNetrc(strings.NewReader(src))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "onlyuser"}, m["a"])
		assert.Equal(t, netrcEntry{password: "onlypass"}, m["b"])
	})

	t.Run("host canonicalized", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader("machine Example.COM login u password p"))
		require.NoError(t, err)
		_, ok := m["example.com"]
		assert.True(t, ok)
	})

	t.Run("unknown keyword value consumed", func(t *testing.T) {
		src := "machine h account foo login u password p\n"
		m, err := parseNetrc(strings.NewReader(src))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "u", password: "p"}, m["h"])
	})

	t.Run("empty reader = empty map", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader(""))
		require.NoError(t, err)
		assert.Empty(t, m)
	})

	t.Run("'#' inside a value is preserved (comment only at token boundary)", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader("machine h login u password pa#ss"))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "u", password: "pa#ss"}, m["h"])
	})

	t.Run("trailing token-boundary '#' still strips the comment", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader("machine h login u password p # note"))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "u", password: "p"}, m["h"])
	})

	t.Run("double-quoted token allows spaces", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader(`machine h login u password "two words"`))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "u", password: "two words"}, m["h"])
	})

	t.Run("quoted token honors backslash escapes", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader(`machine h login u password "a\"b\\c"`))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "u", password: `a"b\c`}, m["h"])
	})

	t.Run("quoted token may contain '#'", func(t *testing.T) {
		m, err := parseNetrc(strings.NewReader(`machine h login u password "p # q"`))
		require.NoError(t, err)
		assert.Equal(t, netrcEntry{login: "u", password: "p # q"}, m["h"])
	})
}

func TestNetrcTokensMacdefEdges(t *testing.T) {
	toks, err := netrcTokens(strings.NewReader("a # b c\nmacdef x\nbody line\n\nd"))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "macdef", "x", "d"}, toks)
}

func TestNetrcPath(t *testing.T) {
	t.Run("explicit wins", func(t *testing.T) {
		t.Setenv("NETRC", "/env/netrc")
		t.Setenv("HOME", "/home/x")
		p, err := netrcPath("/explicit/file")
		require.NoError(t, err)
		assert.Equal(t, "/explicit/file", p)
	})
	t.Run("NETRC env wins over home", func(t *testing.T) {
		t.Setenv("NETRC", "/env/netrc")
		t.Setenv("HOME", "/home/x")
		p, err := netrcPath("")
		require.NoError(t, err)
		assert.Equal(t, "/env/netrc", p)
	})
	t.Run("falls to home/.netrc", func(t *testing.T) {
		t.Setenv("NETRC", "")
		t.Setenv("HOME", "/home/x")
		p, err := netrcPath("")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join("/home/x", ".netrc"), p)
	})
}

func TestLoadNetrc(t *testing.T) {
	t.Run("missing file = nil nil", func(t *testing.T) {
		m, err := loadNetrc(filepath.Join(t.TempDir(), "does-not-exist"))
		require.NoError(t, err)
		assert.Nil(t, m)
	})

	t.Run("present file maps canonical host", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, ".netrc")
		require.NoError(t, os.WriteFile(p, []byte("machine H.Example login u password p\n"), 0o600))
		m, err := loadNetrc(p)
		require.NoError(t, err)
		assert.Equal(t, basicCred{user: "u", pass: "p"}, m["h.example"])
	})

	t.Run("both-empty entry dropped, all-empty = nil", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, ".netrc")
		require.NoError(t, os.WriteFile(p, []byte("machine empty\n"), 0o600))
		m, err := loadNetrc(p)
		require.NoError(t, err)
		assert.Nil(t, m)
	})

	t.Run("unreadable file => wrapped error carrying only the path", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("running as root: chmod 000 is bypassed")
		}
		dir := t.TempDir()
		p := filepath.Join(dir, ".netrc")
		require.NoError(t, os.WriteFile(p, []byte("machine h login u password supersecret\n"), 0o000))
		t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		_, err := loadNetrc(p)
		require.Error(t, err)
		assert.Contains(t, err.Error(), p)
		assert.NotContains(t, err.Error(), "supersecret")
	})
}
