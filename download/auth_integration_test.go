package download

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// basicServer serves body to requests carrying the expected Basic credential and
// 401s otherwise. It records the last Authorization header it observed.
func basicServer(t *testing.T, wantUser, wantPass string, body []byte) (*httptest.Server, *string) {
	t.Helper()
	var mu sync.Mutex
	var lastAuth string
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(wantUser+":"+wantPass))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastAuth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Header.Get("Authorization") != want {
			w.Header().Set("WWW-Authenticate", "Basic")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		serveRanged(w, r, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastAuth
}

// serveRanged answers GET with byte-range support (so the segmented strategy runs).
func serveRanged(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Accept-Ranges", "bytes")
	rng := r.Header.Get("Range")
	if rng == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	start, end, ok := parseBytesRange(rng, int64(len(body)))
	if !ok {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range",
		"bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(body[start : end+1])
}

// parseBytesRange parses "bytes=start-end" (both present, as fetchChunk sends).
func parseBytesRange(h string, size int64) (start, end int64, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found {
		return 0, 0, false
	}
	lo, hi, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, false
	}
	s, err1 := strconv.ParseInt(lo, 10, 64)
	e, err2 := strconv.ParseInt(hi, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if e >= size {
		e = size - 1
	}
	return s, e, true
}

func downloadOpts(t *testing.T, srv *httptest.Server, out string) Options {
	t.Helper()
	return Options{
		Output:      out,
		Concurrency: 2,
		Resume:      true,
		MaxRetries:  2,
		Client:      srv.Client(),
		Out:         nil,
		Quiet:       true,
	}
}

func TestIntegrationUserAuthSucceeds(t *testing.T) {
	body := []byte("hello world payload for basic auth download test")
	srv, _ := basicServer(t, "alice", "secret", body)

	out := filepath.Join(t.TempDir(), "out.bin")
	opts := downloadOpts(t, srv, out)
	opts.URL = srv.URL + "/file.bin"
	auth, err := NewAuth("alice:secret", false, "", hostsFromURLs(opts.URL))
	require.NoError(t, err)
	opts.Auth = auth

	require.NoError(t, Run(context.Background(), opts))
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestIntegrationNetrcApplies(t *testing.T) {
	body := []byte("netrc-authed body content here for the test okay")
	srv, _ := basicServer(t, "nu", "np", body)
	host := mustHost(srv.URL)

	dir := t.TempDir()
	netrcPath := filepath.Join(dir, ".netrc")
	require.NoError(t, os.WriteFile(netrcPath,
		[]byte("machine "+host+" login nu password np\n"), 0o600))

	out := filepath.Join(dir, "out.bin")
	opts := downloadOpts(t, srv, out)
	opts.URL = srv.URL + "/file.bin"
	auth, err := NewAuth("", true, netrcPath, hostsFromURLs(opts.URL))
	require.NoError(t, err)
	opts.Auth = auth

	require.NoError(t, Run(context.Background(), opts))
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestIntegrationNetrcDefaultFallback(t *testing.T) {
	body := []byte("default entry fallback body content for testing!")
	srv, _ := basicServer(t, "du", "dp", body)

	dir := t.TempDir()
	netrcPath := filepath.Join(dir, ".netrc")
	// No machine entry for the server host: must fall to default.
	require.NoError(t, os.WriteFile(netrcPath,
		[]byte("machine other.invalid login x password y\ndefault login du password dp\n"), 0o600))

	out := filepath.Join(dir, "out.bin")
	opts := downloadOpts(t, srv, out)
	opts.URL = srv.URL + "/file.bin"
	auth, err := NewAuth("", true, netrcPath, hostsFromURLs(opts.URL))
	require.NoError(t, err)
	opts.Auth = auth

	require.NoError(t, Run(context.Background(), opts))
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

// TestIntegrationNoCrossHostLeak is the critical no-leak test: the primary host
// requires creds; a mirror on a DIFFERENT host records the Authorization it sees.
// --user is scoped to the primary host only; when the primary 500s and failover
// hits the mirror, the mirror must observe NO (or a different) Authorization.
func TestIntegrationNoCrossHostLeak(t *testing.T) {
	body := []byte("mirror body delivered without primary credentials!!")

	// Primary: always 500 to force failover (records nothing meaningful).
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)

	var mu sync.Mutex
	var mirrorAuth string
	authSeen := false
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if a := r.Header.Get("Authorization"); a != "" {
			mirrorAuth = a
			authSeen = true
		}
		mu.Unlock()
		serveRanged(w, r, body)
	}))
	t.Cleanup(mirror.Close)

	// httptest servers all listen on 127.0.0.1, so to exercise genuinely DIFFERENT
	// hosts we use fake hostnames in the URLs and a transport that dials the real
	// listener addresses. canonicalHost(hostA) != canonicalHost(hostB) now.
	primaryAddr := strings.TrimPrefix(primary.URL, "http://")
	mirrorAddr := strings.TrimPrefix(mirror.URL, "http://")
	client := &http.Client{Transport: &rewriteTransport{
		base: http.DefaultTransport,
		addrs: map[string]string{
			"hosta.invalid": primaryAddr,
			"hostb.invalid": mirrorAddr,
		},
	}}

	out := filepath.Join(t.TempDir(), "out.bin")
	opts := downloadOpts(t, mirror, out)
	opts.Client = client
	opts.URL = "http://hosta.invalid/file.bin"
	opts.Mirrors = []string{"http://hostb.invalid/file.bin"}

	// --user scoped to the PRIMARY host only (hosta). The mirror is hostb, which is
	// NOT in the user set, so even after failover the mirror must receive no creds.
	auth, err := NewAuth("alice:secret", false, "", hostsFromURLs(opts.URL))
	require.NoError(t, err)
	opts.Auth = auth

	require.NoError(t, Run(context.Background(), opts))
	got, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, body, got)

	mu.Lock()
	defer mu.Unlock()
	assert.False(t, authSeen, "mirror on a different host must NOT receive the primary's Authorization (got %q)", mirrorAuth)
}

// rewriteTransport rewrites the request URL host to a real listener address based
// on the fake hostname, so a test can use distinct hostnames over httptest
// servers that all bind 127.0.0.1. The Host header is preserved as the fake name.
type rewriteTransport struct {
	base  http.RoundTripper
	addrs map[string]string
}

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if addr, ok := rt.addrs[req.URL.Hostname()]; ok {
		clone := req.Clone(req.Context())
		clone.URL.Host = addr
		return rt.base.RoundTrip(clone)
	}
	return rt.base.RoundTrip(req)
}

// TestIntegrationUserSameHostMirrors: primary and mirror on the SAME host both
// receive the Basic header after failover (both hosts are in the user set).
func TestIntegrationUserSameHostMirrors(t *testing.T) {
	body := []byte("same host mirror body served on the /m path okay!")
	var mu sync.Mutex
	mPathAuth := ""
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/primary" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		mu.Lock()
		mPathAuth = r.Header.Get("Authorization")
		mu.Unlock()
		serveRanged(w, r, body)
	}))
	t.Cleanup(srv.Close)

	out := filepath.Join(t.TempDir(), "out.bin")
	opts := downloadOpts(t, srv, out)
	opts.URL = srv.URL + "/primary"
	opts.Mirrors = []string{srv.URL + "/m"}
	auth, err := NewAuth("alice:secret", false, "", hostsFromURLs(opts.URL, opts.Mirrors[0]))
	require.NoError(t, err)
	opts.Auth = auth

	require.NoError(t, Run(context.Background(), opts))
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, want, mPathAuth)
}

// TestIntegrationPrecedence: -H Authorization beats --user beats .netrc.
func TestIntegrationPrecedence(t *testing.T) {
	body := []byte("precedence body payload for the layered auth test!")
	host := ""
	var mu sync.Mutex
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Get("Authorization")
		mu.Unlock()
		serveRanged(w, r, body)
	}))
	t.Cleanup(srv.Close)
	host = mustHost(srv.URL)

	dir := t.TempDir()
	netrcPath := filepath.Join(dir, ".netrc")
	require.NoError(t, os.WriteFile(netrcPath,
		[]byte("machine "+host+" login nuser password npass\n"), 0o600))

	netrcBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("nuser:npass"))
	userBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("uuser:upass"))

	run := func(opts Options) string {
		out := filepath.Join(t.TempDir(), "o.bin")
		opts.Output = out
		require.NoError(t, Run(context.Background(), opts))
		mu.Lock()
		defer mu.Unlock()
		return seen
	}

	base := downloadOpts(t, srv, "")
	base.URL = srv.URL + "/file.bin"

	t.Run("-H wins", func(t *testing.T) {
		opts := base
		opts.Header = http.Header{"Authorization": {"Basic PRESET"}}
		auth, _ := NewAuth("uuser:upass", true, netrcPath, hostsFromURLs(opts.URL))
		opts.Auth = auth
		assert.Equal(t, "Basic PRESET", run(opts))
	})
	t.Run("--user beats netrc", func(t *testing.T) {
		opts := base
		auth, _ := NewAuth("uuser:upass", true, netrcPath, hostsFromURLs(opts.URL))
		opts.Auth = auth
		assert.Equal(t, userBasic, run(opts))
	})
	t.Run("netrc when no user", func(t *testing.T) {
		opts := base
		auth, _ := NewAuth("", true, netrcPath, hostsFromURLs(opts.URL))
		opts.Auth = auth
		assert.Equal(t, netrcBasic, run(opts))
	})
}

// TestIntegrationNoAuthNoHeader: with no auth source the request carries no
// Authorization key at all (byte-for-byte unchanged path).
func TestIntegrationNoAuthNoHeader(t *testing.T) {
	body := []byte("plain unauthenticated body for the no-auth path test")
	var mu sync.Mutex
	present := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if _, ok := r.Header["Authorization"]; ok {
			present = true
		}
		mu.Unlock()
		serveRanged(w, r, body)
	}))
	t.Cleanup(srv.Close)

	out := filepath.Join(t.TempDir(), "out.bin")
	opts := downloadOpts(t, srv, out)
	opts.URL = srv.URL + "/file.bin"
	// opts.Auth left nil.

	require.NoError(t, Run(context.Background(), opts))
	mu.Lock()
	defer mu.Unlock()
	assert.False(t, present, "no-auth path must send no Authorization header")
}

// TestIntegrationResultRedactsUserinfo pins the engine-side Result redaction:
// when the served source URL carries userinfo, Result.URL and Result.Source must
// be redacted (no password, no "user:pass@"). Without redaction these assertions
// would fail, so they are load-bearing (unlike the userinfo-free Result tests).
func TestIntegrationResultRedactsUserinfo(t *testing.T) {
	body := []byte("result redaction body payload served for the test ok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveRanged(w, r, body)
	}))
	t.Cleanup(srv.Close)

	addr := strings.TrimPrefix(srv.URL, "http://")
	client := &http.Client{Transport: &rewriteTransport{
		base:  http.DefaultTransport,
		addrs: map[string]string{"hosta.invalid": addr},
	}}

	out := filepath.Join(t.TempDir(), "out.bin")
	var res Result
	opts := downloadOpts(t, srv, out)
	opts.Client = client
	opts.URL = "http://alice:supersecret@hosta.invalid/file.bin"
	opts.Result = &res

	require.NoError(t, Run(context.Background(), opts))

	assert.Contains(t, res.URL, "redacted@")
	assert.Contains(t, res.Source, "redacted@")
	for _, f := range []string{res.URL, res.Source} {
		assert.NotContains(t, f, "supersecret", "Result must not carry the password")
		assert.NotContains(t, f, "alice:supersecret", "Result must not carry userinfo")
	}
}

// TestIntegrationVerboseRedactsUserinfo pins that the --verbose diagnostic stream
// never echoes a userinfo password. The primary (with userinfo) 500s so failover
// vlogf lines fire, then the mirror serves the body. The captured Out must contain
// "redacted@" and NEITHER the password NOR "user:pass@".
func TestIntegrationVerboseRedactsUserinfo(t *testing.T) {
	body := []byte("verbose redaction mirror body content for the test!!")

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(primary.Close)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveRanged(w, r, body)
	}))
	t.Cleanup(mirror.Close)

	client := &http.Client{Transport: &rewriteTransport{
		base: http.DefaultTransport,
		addrs: map[string]string{
			"hosta.invalid": strings.TrimPrefix(primary.URL, "http://"),
			"hostb.invalid": strings.TrimPrefix(mirror.URL, "http://"),
		},
	}}

	logFile := filepath.Join(t.TempDir(), "log.txt")
	lf, err := os.Create(logFile)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lf.Close() })

	out := filepath.Join(t.TempDir(), "out.bin")
	opts := downloadOpts(t, mirror, out)
	opts.Client = client
	opts.Quiet = false
	opts.Verbose = true
	opts.Out = lf
	opts.URL = "http://alice:supersecret@hosta.invalid/file.bin"
	opts.Mirrors = []string{"http://bob:topsecret@hostb.invalid/file.bin"}

	require.NoError(t, Run(context.Background(), opts))
	require.NoError(t, lf.Close())

	logged, err := os.ReadFile(logFile)
	require.NoError(t, err)
	text := string(logged)
	assert.Contains(t, text, "redacted@", "verbose output should echo the redacted URL")
	assert.NotContains(t, text, "supersecret", "verbose output must not leak the primary password")
	assert.NotContains(t, text, "topsecret", "verbose output must not leak the mirror password")
	assert.NotContains(t, text, "alice:supersecret", "verbose output must not leak userinfo")
}

// helpers ------------------------------------------------------------------

func hostsFromURLs(urls ...string) []string {
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		out = append(out, mustHost(u))
	}
	return out
}

func mustHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
