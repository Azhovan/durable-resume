package download

import (
	"context"
	"net/http"
)

// remoteInfo is what a single probe learns about the resource.
type remoteInfo struct {
	size         int64  // -1 when unknown
	acceptRanges bool   // true only when a ranged GET returned 206 (or HEAD advertised bytes)
	etag         string // raw ETag header, may be ""
	lastModified string // raw Last-Modified header, may be ""
}

// streamable reports whether the segmented strategy is usable (ranges + known size).
func (r remoteInfo) streamable() bool {
	panic("not implemented")
}

// probe issues GET Range: bytes=0-0; interprets 206 (Content-Range total, ranges
// supported), 200 (Content-Length, no ranges), and 405/other via a HEAD fallback.
// Never hard-fails on a streamable non-206 response; only transport errors propagate.
func probe(ctx context.Context, c *http.Client, url string, hdr http.Header) (remoteInfo, error) {
	panic("not implemented")
}

// parseContentRangeTotal extracts the total from "bytes 0-0/12345".
// ok=false when the total is "*" or the header is malformed.
func parseContentRangeTotal(v string) (total int64, ok bool) {
	panic("not implemented")
}

// newRequest builds a GET request with caller headers and, when start>=0 && end>=0,
// a Range: bytes=start-end header. start<0 means no Range header.
func newRequest(ctx context.Context, url string, hdr http.Header, start, end int64) (*http.Request, error) {
	panic("not implemented")
}
