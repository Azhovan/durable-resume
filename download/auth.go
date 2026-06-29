package download

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ErrInvalidUser is returned by NewAuth/parseUser when --user has an empty
// username. Its message NEVER echoes the raw value (no credential leak). cmd
// and tests branch on it via errors.Is.
var ErrInvalidUser = errors.New("download: invalid --user value (empty username)")

// basicCred is a resolved Basic user/password pair. pass is NEVER logged or
// marshalled anywhere.
type basicCred struct {
	user string
	pass string
}

// credentialSource resolves a Basic credential for a given request host. It is
// host-aware BY CONSTRUCTION: implementations key off host, so a credential for
// hostA is never returned for hostB. A nil credentialSource is a valid value
// meaning "no auth" (the byte-for-byte-unchanged no-auth path). Implementations
// are immutable after construction => safe for concurrent use across chunk
// workers, no lock.
type credentialSource interface {
	credentialFor(host string) (basicCred, bool)
}

// authResolver layers --user (scoped to user-listed hosts) over the .netrc
// table. URL userinfo is handled separately in applyAuth. A nil *authResolver
// is the no-auth path.
type authResolver struct {
	user      *basicCred           // --user credential, or nil
	userHosts map[string]struct{}  // canonical hosts the user explicitly listed
	netrc     map[string]basicCred // canonical host -> cred; "default" under ""
}

func (a *authResolver) credentialFor(host string) (basicCred, bool) {
	if a == nil {
		return basicCred{}, false
	}
	host = canonicalHost(host)
	if a.user != nil {
		if _, listed := a.userHosts[host]; listed {
			return *a.user, true
		}
	}
	if a.netrc != nil {
		if c, ok := a.netrc[host]; ok {
			return c, true
		}
		if c, ok := a.netrc[""]; ok { // .netrc "default" entry
			return c, true
		}
	}
	return basicCred{}, false
}

// canonicalHost lowercases, trims, and strips any :port (and [ipv6] brackets)
// so lookups are host-only and stable across primary/mirror/probe URLs. It is
// robust to BOTH the bracketed form ("[2001:db8::1]:8443" from url.URL.Host) and
// the bracket-stripped form ("2001:db8::1" from url.URL.Hostname()): a value
// with more than one ':' and no brackets is treated as a bare IPv6 literal and
// returned unchanged, so distinct IPv6 hosts never collapse to one key (which
// would mis-scope a credential to a different host).
func canonicalHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(host, "[") { // [ipv6] or [ipv6]:port
		if end := strings.IndexByte(host, ']'); end >= 0 {
			return host[1:end]
		}
	}
	// A bare IPv6 literal (no brackets) contains multiple ':' and carries no
	// port here (url.Hostname() strips it), so leave it intact.
	if strings.Count(host, ":") > 1 {
		return host
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host
}

// applyAuth adds "Authorization: Basic <b64>" to req IFF no higher-precedence
// source already covers it. PRECEDENCE (highest first):
//  1. Authorization already present (from -H via copyHeaders): leave untouched.
//  2. URL userinfo (https://user:pass@host): honor for THIS request only.
//  3. src.credentialFor(req host): --user (scoped) then .netrc machine/default.
//
// No-op when src==nil, no userinfo, and no -H Authorization => the produced
// *http.Request is byte-for-byte identical to today.
func applyAuth(req *http.Request, src credentialSource) {
	if req.Header.Get("Authorization") != "" {
		return // (1) explicit -H wins; never override.
	}
	if u := req.URL.User; u != nil && u.Username() != "" { // (2) URL userinfo
		pass, _ := u.Password()
		setBasic(req, u.Username(), pass)
		return
	}
	if src == nil {
		return
	}
	if c, ok := src.credentialFor(req.URL.Hostname()); ok { // (3) resolver
		setBasic(req, c.user, c.pass)
	}
}

// setBasic is the SINGLE audited Authorization-write site.
func setBasic(req *http.Request, user, pass string) {
	tok := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	req.Header.Set("Authorization", "Basic "+tok)
}

// parseUser parses "user:password" into *basicCred. "" => (nil,nil). The FIRST
// ':' splits (passwords may contain ':'); a missing ':' => empty password
// (curl-compatible). An empty username => ErrInvalidUser. The error NEVER
// includes the raw value. Pure + table-testable.
func parseUser(s string) (*basicCred, error) {
	if s == "" {
		return nil, nil
	}
	user, pass, _ := strings.Cut(s, ":")
	if user == "" {
		return nil, ErrInvalidUser
	}
	return &basicCred{user: user, pass: pass}, nil
}

// NewAuth is the EXPORTED builder cmd calls to fill Options.Auth. Returns nil
// (the no-auth path, byte-for-byte unchanged) when neither --user nor
// --netrc/--netrc-file is in play. hosts are the hosts the user EXPLICITLY
// listed (primary + mirrors); --user is scoped to them so it never leaks to a
// redirect/other host. useNetrc OR a non-empty netrcFile triggers a netrc read.
func NewAuth(userSpec string, useNetrc bool, netrcFile string, hosts []string) (credentialSource, error) {
	cred, err := parseUser(userSpec)
	if err != nil {
		return nil, err
	}
	var netrcMap map[string]basicCred
	if useNetrc || netrcFile != "" {
		m, lerr := loadNetrc(netrcFile)
		if lerr != nil {
			return nil, lerr // already wrapped; carries only the path
		}
		netrcMap = m
	}
	if cred == nil && netrcMap == nil {
		return nil, nil // no-auth path
	}
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		set[canonicalHost(h)] = struct{}{}
	}
	return &authResolver{user: cred, userHosts: set, netrc: netrcMap}, nil
}

// redactURL rewrites any userinfo to "redacted@": https://user:pass@host/p ->
// https://redacted@host/p. Used at EVERY URL echo (vlogf, runSources wraps,
// Result.URL/Source, cmd --json). A parse failure returns "<redacted-url>"
// rather than risk leaking.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<redacted-url>"
	}
	if u.User != nil {
		u.User = url.User("redacted")
	}
	return u.String()
}

// RedactURL is the exported wrapper cmd uses on its --json record URLs.
func RedactURL(raw string) string { return redactURL(raw) }
