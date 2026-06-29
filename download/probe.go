package download

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// remoteInfo is what a single probe learns about the resource.
type remoteInfo struct {
	size               int64  // -1 when unknown
	acceptRanges       bool   // true only when a ranged GET returned 206 (or HEAD advertised bytes)
	etag               string // raw ETag header, may be ""
	lastModified       string // raw Last-Modified header, may be ""
	contentDisposition string // raw Content-Disposition header value (untrusted), may be ""
	finalURL           string // resp.Request.URL.String() after redirects; falls back to requested URL
}

// streamable reports whether the segmented strategy is usable (ranges + known size).
func (r remoteInfo) streamable() bool {
	return r.acceptRanges && r.size > 0
}

// probe issues GET Range: bytes=0-0; interprets 206 (Content-Range total, ranges
// supported), 200 (Content-Length, no ranges), and 405/other via a HEAD fallback.
// Never hard-fails on a streamable non-206 response; only transport errors propagate.
func probe(ctx context.Context, c *http.Client, url string, hdr http.Header, auth credentialSource) (remoteInfo, error) {
	req, err := newRequest(ctx, url, hdr, 0, 0, auth)
	if err != nil {
		return remoteInfo{size: -1}, err
	}

	resp, err := c.Do(req)
	if err != nil {
		return remoteInfo{size: -1}, err
	}
	defer drainAndClose(resp.Body)

	info := remoteInfo{
		size:               -1,
		etag:               resp.Header.Get("ETag"),
		lastModified:       resp.Header.Get("Last-Modified"),
		contentDisposition: resp.Header.Get("Content-Disposition"),
		finalURL:           finalURLOf(resp, url),
	}

	switch {
	case resp.StatusCode == http.StatusPartialContent:
		// 206: the server honored the range. Total size comes from Content-Range.
		if total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range")); ok {
			info.size = total
			info.acceptRanges = true
		}
		return info, nil

	case resp.StatusCode == http.StatusOK:
		// 200: ranges not honored, full body would be returned. Size from Content-Length.
		info.size = parseContentLength(resp.Header.Get("Content-Length"))
		info.acceptRanges = false
		return info, nil

	default:
		// 405 / other non-streamable status (e.g. range not allowed): try a HEAD
		// fallback to learn size and whether ranges are advertised. Never hard-fail
		// here; only transport errors propagate.
		return headFallback(ctx, c, url, hdr, info, auth)
	}
}

// headFallback issues a HEAD request, learning size from Content-Length and
// acceptRanges from "Accept-Ranges: bytes". The base info (etag/lastModified)
// is carried over and overridden when the HEAD response provides values.
func headFallback(ctx context.Context, c *http.Client, url string, hdr http.Header, base remoteInfo, auth credentialSource) (remoteInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return base, err
	}
	copyHeaders(req, hdr)
	applyAuth(req, auth)

	resp, err := c.Do(req)
	if err != nil {
		return base, err
	}
	defer drainAndClose(resp.Body)

	info := base
	info.size = parseContentLength(resp.Header.Get("Content-Length"))
	info.acceptRanges = strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "bytes")
	if v := resp.Header.Get("ETag"); v != "" {
		info.etag = v
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		info.lastModified = v
	}
	// Content-Disposition: only override when the HEAD response provides one, so a
	// CD captured from the GET response survives a metadata-less HEAD.
	if v := resp.Header.Get("Content-Disposition"); v != "" {
		info.contentDisposition = v
	}
	// finalURL always reflects the HEAD response: it is the later post-redirect
	// request, and finalURLOf already falls back to the requested url.
	info.finalURL = finalURLOf(resp, url)
	return info, nil
}

// finalURLOf returns the post-redirect URL string, falling back to requested when
// the response carries no usable request URL (defensive; real responses always do).
func finalURLOf(resp *http.Response, requested string) string {
	if resp != nil && resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String()
	}
	return requested
}

// parseContentLength parses a Content-Length header value, returning -1 when
// missing or malformed.
func parseContentLength(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return -1
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// drainAndClose drains and closes a response body so the connection can be reused.
func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

// parseContentRangeTotal extracts the total from "bytes 0-0/12345".
// ok=false when the total is "*" or the header is malformed.
func parseContentRangeTotal(v string) (total int64, ok bool) {
	v = strings.TrimSpace(v)
	// Expected form: "bytes 0-0/12345". The total follows the final "/".
	idx := strings.LastIndex(v, "/")
	if idx < 0 {
		return 0, false
	}
	totalStr := strings.TrimSpace(v[idx+1:])
	if totalStr == "" || totalStr == "*" {
		return 0, false
	}
	n, err := strconv.ParseInt(totalStr, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// newRequest builds a GET request with caller headers and, when start>=0 && end>=0,
// a Range: bytes=start-end header. start<0 means no Range header.
func newRequest(ctx context.Context, url string, hdr http.Header, start, end int64, auth credentialSource) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	copyHeaders(req, hdr)
	if start >= 0 && end >= 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10))
	}
	applyAuth(req, auth) // host-aware; no-op when no creds apply (-H already wins)
	return req, nil
}

// copyHeaders forwards caller-supplied headers onto req without mutating hdr.
func copyHeaders(req *http.Request, hdr http.Header) {
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
}
