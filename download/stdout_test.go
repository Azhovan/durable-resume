package download

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// quietOpts returns a base Options for stdout-mode tests: Output="-", a captured
// data sink, and Quiet so no progress goroutine touches a real TTY. The Client is
// taken from the caller's httptest server.
func stdoutOpts(client *http.Client, data *bytes.Buffer) Options {
	return Options{
		URL:    "", // set per test
		Output: stdoutDash,
		Data:   data,
		Quiet:  true,
		Client: client,
	}
}

// TestRun_Stdout_FullBody streams the full body to the injected data sink and
// asserts byte-for-byte equality with the server payload.
func TestRun_Stdout_FullBody(t *testing.T) {
	t.Parallel()
	payload := []byte(strings.Repeat("durable-resume payload ", 5000))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	var data bytes.Buffer
	opts := stdoutOpts(srv.Client(), &data)
	opts.URL = srv.URL + "/file.bin"

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, payload, data.Bytes())
}

// TestRun_Stdout_DataSinkHasOnlyPayload routes the MESSAGE sink (Out) to a
// non-TTY temp *os.File and verifies the DATA sink contains ONLY the payload: no
// "dr: saved to", no verbose "dr:" lines, no carriage-return progress bytes.
func TestRun_Stdout_DataSinkHasOnlyPayload(t *testing.T) {
	t.Parallel()
	payload := []byte("only-the-payload-bytes")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	msgFile, err := os.CreateTemp(t.TempDir(), "msg-*.log")
	require.NoError(t, err)
	defer msgFile.Close()

	var data bytes.Buffer
	opts := Options{
		URL:     srv.URL + "/file.bin",
		Output:  stdoutDash,
		Data:    &data,
		Out:     msgFile, // non-TTY *os.File: progress never renders, but vlogf/savedf would write here
		Verbose: true,
		Client:  srv.Client(),
	}

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, payload, data.Bytes())
	assert.NotContains(t, data.String(), "dr:")
	assert.NotContains(t, data.String(), "\r")

	// Bidirectional decoupling: the diagnostics that were kept OUT of the data sink
	// must have actually landed IN the message sink. Read msgFile back and assert it
	// carries the verbose "dr:" stdout-strategy line (download.go), proving the
	// message sink is live and not silently dropped.
	require.NoError(t, msgFile.Sync())
	msgBytes, err := os.ReadFile(msgFile.Name())
	require.NoError(t, err)
	assert.Contains(t, string(msgBytes), "dr:", "verbose diagnostics must reach the message sink")
	assert.Contains(t, string(msgBytes), "single sequential stream to stdout",
		"the stdout-strategy vlogf line must reach the message sink")
}

// TestRun_Stdout_RangedServerSingleStream proves stdout mode forces a single
// non-ranged stream even when the server supports ranges (info.streamable()==true):
// the body request carries NO Range header, the stream is complete, and NO .part or
// .dr.json file is created in the working directory.
func TestRun_Stdout_RangedServerSingleStream(t *testing.T) {
	payload := []byte(strings.Repeat("ranged-but-streamed ", 2000))

	var bodyReqHadRange bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		w.Header().Set("Accept-Ranges", "bytes")
		switch rng {
		case "bytes=0-0":
			// Probe: advertise ranges + total size so info.streamable() is true.
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(payload)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:1])
		default:
			// Full-body request from copyToStream must carry NO Range header.
			if rng != "" {
				bodyReqHadRange = true
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		}
	}))
	defer srv.Close()

	// Run in a temp dir so we can assert no sidecar/.part artifacts appear.
	dir := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(cwd)

	var data bytes.Buffer
	opts := stdoutOpts(srv.Client(), &data)
	opts.URL = srv.URL + "/file.bin"

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, payload, data.Bytes())
	assert.False(t, bodyReqHadRange, "stdout body request must not send a Range header")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		name := e.Name()
		assert.NotContains(t, name, ".part", "stdout mode must not create a .part file")
		assert.NotContains(t, name, ".dr.json", "stdout mode must not create a resume sidecar")
	}
}

// TestRun_Stdout_ShortBody verifies that when the probe learns a size N but the
// body delivers cleanly fewer than N bytes, the count-based verify reports
// ErrSizeMismatch. The probe advertises the larger size via Content-Range; the
// full-body request returns a clean (chunked, no Content-Length) shorter body so
// the client sees a normal EOF rather than a transport error.
func TestRun_Stdout_ShortBody(t *testing.T) {
	t.Parallel()
	const declared = 100
	short := []byte("short body, fewer than declared")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			// Probe: advertise the (larger) total size so info.size == declared.
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(declared))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte{0})
			return
		}
		// Full body: no Content-Length (chunked) so the client gets a clean EOF
		// after the short payload, exercising the count-vs-size verify path.
		_, _ = w.Write(short)
	}))
	defer srv.Close()

	var data bytes.Buffer
	opts := stdoutOpts(srv.Client(), &data)
	opts.URL = srv.URL + "/file.bin"

	err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSizeMismatch)
	assert.Equal(t, short, data.Bytes(), "partial bytes still reach the data sink")
}

// TestRun_Stdout_UnknownSize verifies that with no Content-Length the size verify
// is skipped, the full body is streamed, and no error is returned.
func TestRun_Stdout_UnknownSize(t *testing.T) {
	t.Parallel()
	payload := []byte("unknown-size-body")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Content-Length: chunked transfer; probe sees size unknown.
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	var data bytes.Buffer
	opts := stdoutOpts(srv.Client(), &data)
	opts.URL = srv.URL + "/file.bin"

	require.NoError(t, Run(context.Background(), opts))
	assert.Equal(t, payload, data.Bytes())
}

// TestRun_Stdout_MidStreamResetNoReEmit verifies that a server which writes some
// bytes then resets mid-body causes runStdoutStream to return an error, and the data
// sink contains exactly one partial read's worth of bytes with NO duplicated prefix
// (no re-emit). The declared size exceeds what is delivered, so this also exercises
// the no-retry guarantee.
func TestRun_Stdout_MidStreamResetNoReEmit(t *testing.T) {
	t.Parallel()
	// Declare a large size but reset after a smaller, fixed amount.
	const declared = 1 << 20
	// Use a non-uniform, position-dependent payload so a re-emit/duplication is
	// detectable by CONTENT, not merely by length: each byte encodes its index, so
	// any repeated run would break the strict-prefix relationship below.
	prefix := make([]byte, 4096)
	for i := range prefix {
		prefix[i] = byte(i)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(declared))
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			_, _ = w.Write(prefix)
			fl.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.SetLinger(0) // force RST instead of graceful FIN
				}
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	var data bytes.Buffer
	opts := stdoutOpts(srv.Client(), &data)
	opts.URL = srv.URL + "/file.bin"
	opts.MaxRetries = 5 // ignored by the stdout body path; proves no re-emit on retry

	err := Run(context.Background(), opts)
	require.Error(t, err)
	// No re-emit: the data sink holds at most a single contiguous attempt's bytes,
	// never a duplicated prefix from a retry. With the position-dependent payload, the
	// sink must be EXACTLY a strict prefix of `prefix` (i.e. prefix[:data.Len()]); any
	// re-emit would either exceed prefix's length or repeat the index pattern, breaking
	// this equality by content rather than relying on length alone.
	require.LessOrEqual(t, data.Len(), len(prefix),
		"data sink longer than one attempt's bytes implies a re-emit/duplication")
	assert.Equal(t, prefix[:data.Len()], data.Bytes(),
		"data sink must contain only a single contiguous partial prefix, no duplication")
}

// TestRun_Stdout_NoArtifacts confirms a streamable server with Output="-" creates
// no sidecar/.part/rename artifacts in the working directory.
func TestRun_Stdout_NoArtifacts(t *testing.T) {
	payload := []byte(strings.Repeat("x", 10000))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	// Run from inside dir so any leaked staging artifact (partPath("-")=="-.part",
	// or its "-.part.dr.json" sidecar) — created as a RELATIVE path in the working
	// directory if stdout mode regressed to the file path — would actually land in
	// the asserted dir. Not parallel: it chdir's. (cf. TestRun_Stdout_RangedServerSingleStream.)
	dir := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(cwd)

	var data bytes.Buffer
	opts := stdoutOpts(srv.Client(), &data)
	opts.URL = srv.URL + "/file.bin"

	require.NoError(t, Run(context.Background(), opts))

	// Nothing in dir should reference any output path; assert dir is empty.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		t.Errorf("unexpected artifact created in stdout mode: %s", e.Name())
	}
}
