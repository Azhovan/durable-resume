package download

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type netrcEntry struct {
	login    string
	password string
}

// parseNetrc tokenizes the classic netrc grammar and returns canonical host ->
// entry, with the "default" entry under "". PURE (no filesystem). Rules:
// whitespace/newline-separated tokens; keywords machine <host>, login <user>,
// password <pass>, default (no arg), macdef <name> (body dropped to next BLANK
// line by netrcTokens); '#' begins a comment to end of line; an unknown
// keyword's single value token is consumed and ignored; a later machine for the
// same host overrides. Only a scanner read error propagates.
func parseNetrc(r io.Reader) (map[string]netrcEntry, error) {
	out := map[string]netrcEntry{}
	toks, err := netrcTokens(r)
	if err != nil {
		return nil, err
	}
	var cur netrcEntry
	inBlock := false
	host := ""
	flush := func() {
		if inBlock {
			out[host] = cur
		}
	}
	for i := 0; i < len(toks); i++ {
		switch toks[i] {
		case "machine":
			flush()
			cur, inBlock = netrcEntry{}, true
			i++
			if i < len(toks) {
				host = canonicalHost(toks[i])
			}
		case "default":
			flush()
			cur, inBlock, host = netrcEntry{}, true, "" // "" == default key
		case "login":
			i++
			if i < len(toks) {
				cur.login = toks[i]
			}
		case "password":
			i++
			if i < len(toks) {
				cur.password = toks[i]
			}
		case "macdef":
			i++ // consume macro name; body already dropped by netrcTokens
		default:
			i++ // unknown keyword: consume its value token
		}
	}
	flush()
	return out, nil
}

// netrcTokens splits r into tokens, honoring "#" comments and DROPPING macdef
// bodies (everything after a `macdef <name>` token up to the next blank line).
// '#' begins a comment only at a TOKEN BOUNDARY (the start of a whitespace-
// delimited word), so a value like "pa#ss" is preserved verbatim. Double-quoted
// tokens are supported (curl extension): "two words" yields a single token with
// the quotes stripped and \" / \\ unescaped, so a credential may contain spaces.
func netrcTokens(r io.Reader) ([]string, error) {
	var toks []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	skip := false
	for sc.Scan() {
		line := sc.Text()
		if skip {
			if strings.TrimSpace(line) == "" {
				skip = false
			}
			continue
		}
		fields := tokenizeLine(line)
		for j, f := range fields {
			toks = append(toks, f)
			if f == "macdef" {
				// Keep the macro NAME token (the next field on this line, if
				// present) so parseNetrc's i++ consumes it, then drop the macro
				// BODY (all following lines) until the next blank line.
				if j+1 < len(fields) {
					toks = append(toks, fields[j+1])
				}
				skip = true
				break
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return toks, nil
}

// tokenizeLine splits a single netrc line into tokens. Tokens are separated by
// whitespace; a token starting with '#' (and the remainder of the line) is a
// comment and dropped; a token starting with '"' is read through its closing
// '"', supporting \" and \\ escapes, so a value may contain spaces or a '#'.
func tokenizeLine(line string) []string {
	var fields []string
	i, n := 0, len(line)
	for i < n {
		// Skip leading whitespace.
		for i < n && (line[i] == ' ' || line[i] == '\t' || line[i] == '\r') {
			i++
		}
		if i >= n {
			break
		}
		if line[i] == '#' { // comment to end of line at a token boundary
			break
		}
		if line[i] == '"' { // quoted token: consume through closing quote
			i++
			var b strings.Builder
			for i < n && line[i] != '"' {
				if line[i] == '\\' && i+1 < n {
					i++ // skip the backslash, take the next byte literally
				}
				b.WriteByte(line[i])
				i++
			}
			if i < n { // skip the closing quote
				i++
			}
			fields = append(fields, b.String())
			continue
		}
		// Bare token: read until whitespace.
		start := i
		for i < n && line[i] != ' ' && line[i] != '\t' && line[i] != '\r' {
			i++
		}
		fields = append(fields, line[start:i])
	}
	return fields
}

// netrcPath: --netrc-file > $NETRC > ~/.netrc (os.UserHomeDir). Returns ("",nil)
// when no override and no home resolvable (caller => no creds).
func netrcPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p := os.Getenv("NETRC"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil // no home => no creds, not an error
	}
	return filepath.Join(home, ".netrc"), nil
}

// loadNetrc resolves+parses the netrc into canonical host->cred ("" == default).
// A MISSING file is NOT an error => (nil,nil). A real read error is wrapped with
// %w and carries ONLY the path (never file contents). Entries with BOTH login
// and password empty are dropped; an all-empty result => (nil,nil).
func loadNetrc(explicit string) (map[string]basicCred, error) {
	path, err := netrcPath(explicit)
	if err != nil || path == "" {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("download: read netrc %q: %w", path, err)
	}
	defer f.Close()
	entries, err := parseNetrc(f)
	if err != nil {
		return nil, fmt.Errorf("download: parse netrc %q: %w", path, err)
	}
	out := map[string]basicCred{}
	for host, e := range entries {
		if e.login == "" && e.password == "" {
			continue
		}
		out[host] = basicCred{user: e.login, pass: e.password}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
