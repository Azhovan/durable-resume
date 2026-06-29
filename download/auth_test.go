package download

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUser(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantNil  bool
		wantUser string
		wantPass string
		wantErr  bool
	}{
		{name: "user and pass", in: "alice:secret", wantUser: "alice", wantPass: "secret"},
		{name: "embedded colon in password", in: "alice:p:a:ss", wantUser: "alice", wantPass: "p:a:ss"},
		{name: "missing colon = empty pass", in: "alice", wantUser: "alice", wantPass: ""},
		{name: "empty = no cred", in: "", wantNil: true},
		{name: "empty username = error", in: ":secret", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUser(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidUser)
				assert.Nil(t, got)
				// The error must NOT leak the raw value (no "secret").
				assert.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			if tt.wantNil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantUser, got.user)
			assert.Equal(t, tt.wantPass, got.pass)
		})
	}
}

func TestCanonicalHost(t *testing.T) {
	tests := []struct{ in, want string }{
		{"HOST:8080", "host"},
		{"[::1]:443", "::1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{" Example.COM ", "example.com"},
		{"plainhost", "plainhost"},
		{"host:80", "host"},
		// Bare IPv6 literals (the form url.URL.Hostname() returns) must be kept
		// intact, NOT truncated at the last ':' (which would collapse distinct
		// hosts to one key and mis-scope a credential across hosts).
		{"2001:db8::1", "2001:db8::1"},
		{"2001:db8::2", "2001:db8::2"},
		{"::1", "::1"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, canonicalHost(tt.in), "in=%q", tt.in)
	}
}

// TestCanonicalHostIPv6NoCollapse pins the real call path: applyAuth feeds
// req.URL.Hostname() (bracket-stripped) to canonicalHost, so two distinct IPv6
// hosts sharing a prefix must produce DISTINCT keys; otherwise hostA's
// credential would resolve for hostB (a cross-host leak).
func TestCanonicalHostIPv6NoCollapse(t *testing.T) {
	hostOf := func(raw string) string {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return canonicalHost(u.Hostname())
	}
	a := hostOf("https://[2001:db8::1]:8443/x")
	b := hostOf("https://[2001:db8::2]:8443/x")
	assert.Equal(t, "2001:db8::1", a)
	assert.Equal(t, "2001:db8::2", b)
	assert.NotEqual(t, a, b, "distinct IPv6 hosts must not collapse to one key")
}

func TestApplyAuthPrecedence(t *testing.T) {
	resolver := &authResolver{
		user:      &basicCred{user: "ruser", pass: "rpass"},
		userHosts: map[string]struct{}{"host.example": {}},
	}

	t.Run("explicit -H Authorization wins", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://user:pass@host.example/x", nil)
		req.Header.Set("Authorization", "Basic PRESET")
		applyAuth(req, resolver)
		assert.Equal(t, "Basic PRESET", req.Header.Get("Authorization"))
	})

	t.Run("URL userinfo when no -H", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://uuser:upass@host.example/x", nil)
		applyAuth(req, resolver)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("uuser:upass"))
		assert.Equal(t, want, req.Header.Get("Authorization"))
	})

	t.Run("resolver when no -H and no userinfo", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://host.example/x", nil)
		applyAuth(req, resolver)
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ruser:rpass"))
		assert.Equal(t, want, req.Header.Get("Authorization"))
	})

	t.Run("nothing matches = no header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://other.example/x", nil)
		applyAuth(req, resolver)
		_, present := req.Header["Authorization"]
		assert.False(t, present)
	})

	t.Run("nil source and no userinfo = byte-for-byte no header", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://host.example/x", nil)
		applyAuth(req, nil)
		_, present := req.Header["Authorization"]
		assert.False(t, present)
	})
}

func TestAuthResolverCredentialFor(t *testing.T) {
	a := &authResolver{
		user:      &basicCred{user: "u", pass: "p"},
		userHosts: map[string]struct{}{"a.example": {}},
		netrc: map[string]basicCred{
			"n.example": {user: "nu", pass: "np"},
			"":          {user: "du", pass: "dp"}, // default
		},
	}

	t.Run("user host listed", func(t *testing.T) {
		c, ok := a.credentialFor("a.example")
		require.True(t, ok)
		assert.Equal(t, basicCred{user: "u", pass: "p"}, c)
	})
	t.Run("netrc machine match", func(t *testing.T) {
		c, ok := a.credentialFor("n.example")
		require.True(t, ok)
		assert.Equal(t, basicCred{user: "nu", pass: "np"}, c)
	})
	t.Run("default fallback for unknown host", func(t *testing.T) {
		c, ok := a.credentialFor("unknown.example")
		require.True(t, ok)
		assert.Equal(t, basicCred{user: "du", pass: "dp"}, c)
	})
	t.Run("port stripped in lookup", func(t *testing.T) {
		c, ok := a.credentialFor("a.example:8443")
		require.True(t, ok)
		assert.Equal(t, "u", c.user)
	})

	t.Run("no leak across hosts (no default)", func(t *testing.T) {
		b := &authResolver{
			user:      &basicCred{user: "u", pass: "p"},
			userHosts: map[string]struct{}{"hosta": {}},
		}
		_, ok := b.credentialFor("hostb")
		assert.False(t, ok, "hostA credential must not resolve for hostB")
	})

	t.Run("nil resolver", func(t *testing.T) {
		var n *authResolver
		_, ok := n.credentialFor("any")
		assert.False(t, ok)
	})
}

func TestRedactURL(t *testing.T) {
	assert.Equal(t, "https://redacted@host/p", redactURL("https://user:pass@host/p"))
	assert.Equal(t, "https://redacted@host/p", redactURL("https://user@host/p"))
	assert.Equal(t, "https://host/p", redactURL("https://host/p"))
	assert.Equal(t, "<redacted-url>", redactURL("://bad url with spaces \x7f"))
	// no password substring ever survives
	assert.NotContains(t, redactURL("https://user:supersecret@host/p"), "supersecret")
}

func TestNewAuth(t *testing.T) {
	t.Run("no auth flags = nil source", func(t *testing.T) {
		src, err := NewAuth("", false, "", []string{"https://host"})
		require.NoError(t, err)
		assert.Nil(t, src)
	})
	t.Run("invalid user surfaces ErrInvalidUser", func(t *testing.T) {
		src, err := NewAuth(":pass", false, "", nil)
		require.ErrorIs(t, err, ErrInvalidUser)
		assert.Nil(t, src)
	})
	t.Run("user populates resolver scoped to hosts", func(t *testing.T) {
		src, err := NewAuth("u:p", false, "", []string{"h.example"})
		require.NoError(t, err)
		require.NotNil(t, src)
		c, ok := src.credentialFor("h.example")
		require.True(t, ok)
		assert.Equal(t, "u", c.user)
		_, ok2 := src.credentialFor("other.example")
		assert.False(t, ok2)
	})
}

func TestSetBasic(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://h/x", nil)
	setBasic(req, "alice", "p:w")
	got := req.Header.Get("Authorization")
	require.True(t, strings.HasPrefix(got, "Basic "))
	dec, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(got, "Basic "))
	require.NoError(t, err)
	assert.Equal(t, "alice:p:w", string(dec))
}
