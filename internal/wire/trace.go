package wire

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// tracing is on when ANY_TRACE is set: every HTTP round trip is logged to
// stderr with its pkt-line request body and timing.
var tracing = os.Getenv("ANY_TRACE") != ""

type traceTransport struct{ next http.RoundTripper }

func (t traceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	status := "error"
	if resp != nil {
		status = resp.Status
	}
	fmt.Fprintf(os.Stderr, "trace: %s %s -> %s (%s to headers)\n", req.Method, req.URL.Path, status, time.Since(start).Round(time.Millisecond))
	for _, l := range pktLines(body) {
		fmt.Fprintf(os.Stderr, "trace:   > %s\n", l)
	}
	if resp != nil {
		resp.Body = &timedBody{ReadCloser: resp.Body, start: start, path: req.URL.Path}
	}
	return resp, err
}

type timedBody struct {
	io.ReadCloser
	start time.Time
	path  string
	n     int64
}

func (b *timedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	return n, err
}

func (b *timedBody) Close() error {
	fmt.Fprintf(os.Stderr, "trace: %s body %d bytes, %s total\n", b.path, b.n, time.Since(b.start).Round(time.Millisecond))
	return b.ReadCloser.Close()
}

// pktLines decodes a pkt-line stream for display; it stops at anything odd
// (such as a packfile) and caps output.
func pktLines(b []byte) []string {
	var out []string
	for len(b) >= 4 && len(out) < 40 {
		var n int
		if _, err := fmt.Sscanf(string(b[:4]), "%04x", &n); err != nil {
			break
		}
		switch {
		case n < 4:
			out = append(out, fmt.Sprintf("%04d", n))
			b = b[4:]
			continue
		case n > len(b):
			out = append(out, fmt.Sprintf("<%d bytes>", len(b)))
			return out
		}
		out = append(out, strings.TrimSuffix(string(b[4:n]), "\n"))
		b = b[n:]
	}
	if len(b) > 0 {
		out = append(out, fmt.Sprintf("<%d more bytes>", len(b)))
	}
	return out
}
