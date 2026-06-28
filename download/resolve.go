package download

import (
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// fallbackName is the last-resort filename when nothing else yields a safe element.
const fallbackName = "download"

// resolveOutputPath is the single source of truth for the final on-disk path.
// requested is the user's raw -o/--output value: "" (derive), an existing
// directory (place the derived name inside it), or a normal path (verbatim).
// fallbackURL is opts.URL (the originally-requested URL), used only when the probe
// captured no finalURL. The os.Stat is the sole side effect; the genuinely pure
// helpers below stay testable without the filesystem. Always returns a non-empty
// path; the error return is reserved for a future hard-fail and is currently nil.
func resolveOutputPath(requested string, info remoteInfo, fallbackURL string) (string, error) {
	if requested != "" {
		if fi, err := os.Stat(requested); err == nil && fi.IsDir() {
			// Existing directory: place the sanitized server/url name inside it.
			return filepath.Join(requested, derivedName(info, fallbackURL)), nil
		}
		// Normal path: verbatim. The user's own value is trusted, never sanitized.
		return requested, nil
	}
	return derivedName(info, fallbackURL), nil
}

// derivedName returns a single safe filename element from the server
// (Content-Disposition), else the post-redirect URL basename, else the requested
// URL basename, else fallbackName. Each candidate is funneled through
// sanitizeFilename; the first non-empty sanitized result wins. The result is always
// a safe single path element (never "", ".", "..", never contains a separator).
func derivedName(info remoteInfo, fallbackURL string) string {
	if name, ok := filenameFromContentDisposition(info.contentDisposition); ok {
		if s := sanitizeFilename(name); s != "" {
			return s
		}
	}
	if s := sanitizeFilename(urlBasename(info.finalURL)); s != "" {
		return s
	}
	if s := sanitizeFilename(urlBasename(fallbackURL)); s != "" {
		return s
	}
	return fallbackName
}

// filenameFromContentDisposition extracts the filename from an RFC 6266
// Content-Disposition header via mime.ParseMediaType, which decodes both
// `filename=` and the RFC 5987/2231 extended `filename*=` UTF-8 form into the
// "filename" param (filename* preferred when both present). Returns ok=false when
// the header is empty, unparseable, or carries no filename param. A ParseMediaType
// error is treated as ok=false (fall through), never propagated. The returned name
// is NOT yet sanitized (caller sanitizes).
func filenameFromContentDisposition(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	_, params, err := mime.ParseMediaType(v)
	if err != nil {
		return "", false
	}
	name, ok := params["filename"]
	if !ok || strings.TrimSpace(name) == "" {
		return "", false
	}
	return name, true
}

// urlBasename returns the percent-decoded basename of a URL's path, or "" on parse
// failure or an empty / slash-terminated path. net/url already percent-decodes
// u.Path, so path.Base operates on the decoded path. NOT yet sanitized.
func urlBasename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Path == "" || strings.HasSuffix(u.Path, "/") {
		return ""
	}
	return path.Base(u.Path)
}

// sanitizeFilename reduces an untrusted name to a single safe path element, or ""
// when nothing safe remains (caller falls through). It rejects NUL outright,
// normalizes both separator styles, takes the basename, and rejects "", ".", "..".
// A traversal string like "../../etc/passwd" therefore yields "passwd" (its safe
// basename), while a pure-dotdot or pure-separator string yields "". The result, if
// non-empty, never contains a separator and is safe to filepath.Join under any dir.
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsRune(name, 0) {
		return ""
	}
	// Neutralize Windows-style traversal even on POSIX hosts: the bytes are from
	// an untrusted header, so collapse "\\" before taking the basename.
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)     // "a/b/c" -> "c", "/" -> "/", "" -> "."
	name = filepath.Base(name) // belt-and-suspenders on the host separator
	switch name {
	case "", ".", "..", "/":
		return ""
	}
	// On hosts where filepath.Separator is not '/', also reject a bare separator.
	if name == string(filepath.Separator) {
		return ""
	}
	return name
}
