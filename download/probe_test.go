package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		handler          http.HandlerFunc
		wantSize         int64
		wantAcceptRanges bool
		wantETag         string
		wantLastModified string
	}{
		{
			name: "206 with content-range captures total and metadata",
			handler: func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "bytes=0-0", r.Header.Get("Range"))
				w.Header().Set("Content-Range", "bytes 0-0/1000")
				w.Header().Set("ETag", `"abc"`)
				w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("x"))
			},
			wantSize:         1000,
			wantAcceptRanges: true,
			wantETag:         `"abc"`,
			wantLastModified: "Wed, 21 Oct 2015 07:28:00 GMT",
		},
		{
			name: "200 with content-length, no ranges",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "1000")
				w.WriteHeader(http.StatusOK)
			},
			wantSize:         1000,
			wantAcceptRanges: false,
		},
		{
			name: "405 falls back to HEAD with accept-ranges bytes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.Header().Set("Content-Length", "2048")
					w.Header().Set("Accept-Ranges", "bytes")
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusMethodNotAllowed)
			},
			wantSize:         2048,
			wantAcceptRanges: true,
		},
		{
			name: "405 HEAD fallback without accept-ranges",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.Header().Set("Content-Length", "2048")
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusMethodNotAllowed)
			},
			wantSize:         2048,
			wantAcceptRanges: false,
		},
		{
			name: "no content-length anywhere yields unknown size, no error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusOK)
					return
				}
				w.WriteHeader(http.StatusMethodNotAllowed)
			},
			wantSize:         -1,
			wantAcceptRanges: false,
		},
		{
			name: "200 with chunked body (no content-length) is unknown, no hard fail",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// Force chunked transfer so no Content-Length is emitted.
				w.Header().Set("Transfer-Encoding", "chunked")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("hello"))
			},
			wantSize:         -1,
			wantAcceptRanges: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			info, err := probe(context.Background(), srv.Client(), srv.URL, nil, nil)
			require.NoError(t, err)
			assert.Equal(t, tt.wantSize, info.size)
			assert.Equal(t, tt.wantAcceptRanges, info.acceptRanges)
			if tt.wantETag != "" {
				assert.Equal(t, tt.wantETag, info.etag)
			}
			if tt.wantLastModified != "" {
				assert.Equal(t, tt.wantLastModified, info.lastModified)
			}
		})
	}
}

func TestProbeCapturesContentDispositionAndFinalURL(t *testing.T) {
	t.Parallel()

	t.Run("non-redirect 206 captures cd and finalURL", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 0-0/1000")
			w.Header().Set("Content-Disposition", `attachment; filename="ubuntu.iso"`)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("x"))
		}))
		defer srv.Close()

		info, err := probe(context.Background(), srv.Client(), srv.URL, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, `attachment; filename="ubuntu.iso"`, info.contentDisposition)
		assert.Equal(t, srv.URL, info.finalURL)
	})

	t.Run("302 redirect finalURL is post-redirect", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/start":
				http.Redirect(w, r, "/real/ubuntu.iso", http.StatusFound)
			case "/real/ubuntu.iso":
				w.Header().Set("Content-Range", "bytes 0-0/1000")
				w.Header().Set("Content-Disposition", `attachment; filename="server.iso"`)
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("x"))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		info, err := probe(context.Background(), srv.Client(), srv.URL+"/start", nil, nil)
		require.NoError(t, err)
		assert.True(t, strings.HasSuffix(info.finalURL, "/real/ubuntu.iso"), "finalURL=%q", info.finalURL)
		assert.Equal(t, `attachment; filename="server.iso"`, info.contentDisposition)
	})

	t.Run("HEAD fallback carries cd and sets finalURL", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.Header().Set("Content-Length", "2048")
				w.Header().Set("Content-Disposition", `attachment; filename="head.bin"`)
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer srv.Close()

		info, err := probe(context.Background(), srv.Client(), srv.URL, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, `attachment; filename="head.bin"`, info.contentDisposition)
		assert.Equal(t, srv.URL, info.finalURL)
	})

	t.Run("GET-set cd survives a cd-less HEAD fallback", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				// HEAD provides size but no Content-Disposition.
				w.Header().Set("Content-Length", "2048")
				w.WriteHeader(http.StatusOK)
				return
			}
			// The GET (probe) sets a CD then returns a non-streamable status.
			w.Header().Set("Content-Disposition", `attachment; filename="get.bin"`)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))
		defer srv.Close()

		info, err := probe(context.Background(), srv.Client(), srv.URL, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, `attachment; filename="get.bin"`, info.contentDisposition)
		assert.NotEmpty(t, info.finalURL)
	})
}

func TestProbeTransportErrorPropagates(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // immediately closed => connection refused

	_, err := probe(context.Background(), srv.Client(), srv.URL, nil, nil)
	require.Error(t, err)
}

func TestParseContentRangeTotal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		in        string
		wantTotal int64
		wantOK    bool
	}{
		{"valid", "bytes 0-0/1000", 1000, true},
		{"valid with spaces", "  bytes 0-0/1000  ", 1000, true},
		{"unknown total star", "bytes 0-0/*", 0, false},
		{"malformed missing slash", "bytes 0-0", 0, false},
		{"empty", "", 0, false},
		{"non numeric total", "bytes 0-0/abc", 0, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			total, ok := parseContentRangeTotal(tt.in)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantTotal, total)
		})
	}
}

func TestRemoteInfoStreamable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info remoteInfo
		want bool
	}{
		{"ranges and size", remoteInfo{size: 100, acceptRanges: true}, true},
		{"ranges but unknown size", remoteInfo{size: -1, acceptRanges: true}, false},
		{"ranges but zero size", remoteInfo{size: 0, acceptRanges: true}, false},
		{"size but no ranges", remoteInfo{size: 100, acceptRanges: false}, false},
		{"neither", remoteInfo{size: -1, acceptRanges: false}, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.info.streamable())
		})
	}
}

func TestNewRequest(t *testing.T) {
	t.Parallel()

	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer tok")
	hdr.Add("X-Multi", "a")
	hdr.Add("X-Multi", "b")

	t.Run("sets range when both bounds non-negative", func(t *testing.T) {
		t.Parallel()
		req, err := newRequest(context.Background(), "http://example.com", hdr, 10, 20, nil)
		require.NoError(t, err)
		assert.Equal(t, "bytes=10-20", req.Header.Get("Range"))
		assert.Equal(t, "Bearer tok", req.Header.Get("Authorization"))
		assert.Equal(t, []string{"a", "b"}, req.Header.Values("X-Multi"))
	})

	t.Run("no range header when start negative", func(t *testing.T) {
		t.Parallel()
		req, err := newRequest(context.Background(), "http://example.com", hdr, -1, 20, nil)
		require.NoError(t, err)
		assert.Empty(t, req.Header.Get("Range"))
		assert.Equal(t, "Bearer tok", req.Header.Get("Authorization"))
	})

	t.Run("no range header when end negative", func(t *testing.T) {
		t.Parallel()
		req, err := newRequest(context.Background(), "http://example.com", hdr, 0, -1, nil)
		require.NoError(t, err)
		assert.Empty(t, req.Header.Get("Range"))
	})

	t.Run("nil headers ok", func(t *testing.T) {
		t.Parallel()
		req, err := newRequest(context.Background(), "http://example.com", nil, 0, 0, nil)
		require.NoError(t, err)
		assert.Equal(t, "bytes=0-0", req.Header.Get("Range"))
	})
}
