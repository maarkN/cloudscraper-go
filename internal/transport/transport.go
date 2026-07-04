// Package transport implements an http.RoundTripper that performs the TLS
// handshake with a real browser's ClientHello (via uTLS), so the connection's
// JA3/JA4 fingerprint matches that browser instead of Go's default net/http
// fingerprint — which anti-bot systems (Cloudflare, Akamai) flag on sight.
//
// A fresh connection is dialed per request; connection pooling and hot-session
// reuse are later milestones (M2/M3).
package transport

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// Transport is a uTLS-backed http.RoundTripper. The zero value is not usable;
// construct it with New.
type Transport struct {
	// ClientHelloID selects the browser ClientHello to mimic.
	ClientHelloID utls.ClientHelloID
	// DialTimeout bounds the TCP dial. The overall deadline still comes from the
	// request's context.
	DialTimeout time.Duration
	// InsecureSkipVerify disables certificate verification. Testing only.
	InsecureSkipVerify bool
}

// New returns a Transport that mimics the given browser ClientHello.
func New(id utls.ClientHelloID) *Transport {
	return &Transport{ClientHelloID: id, DialTimeout: 30 * time.Second}
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("cloudscraper: only https is supported, got scheme %q", req.URL.Scheme)
	}

	ctx := req.Context()
	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}

	dialer := &net.Dialer{Timeout: t.DialTimeout}
	tcpConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", host, err)
	}

	cfg := &utls.Config{ServerName: host, InsecureSkipVerify: t.InsecureSkipVerify}
	uconn := utls.UClient(tcpConn, cfg, t.ClientHelloID)
	if err := uconn.HandshakeContext(ctx); err != nil {
		_ = tcpConn.Close()
		return nil, fmt.Errorf("tls handshake %s: %w", host, err)
	}

	var resp *http.Response
	switch uconn.ConnectionState().NegotiatedProtocol {
	case "h2":
		resp, err = roundTripHTTP2(uconn, req)
	default:
		resp, err = roundTripHTTP1(uconn, req)
	}
	if err != nil {
		_ = uconn.Close()
		return nil, err
	}

	// Transparently decompress, then release the connection when the caller
	// closes the body.
	resp.Body = decompress(resp)
	resp.Body = &connReleasingBody{ReadCloser: resp.Body, conn: uconn}
	return resp, nil
}

func roundTripHTTP2(conn net.Conn, req *http.Request) (*http.Response, error) {
	tr := &http2.Transport{}
	cc, err := tr.NewClientConn(conn)
	if err != nil {
		return nil, fmt.Errorf("http2 client conn: %w", err)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("http2 roundtrip: %w", err)
	}
	return resp, nil
}

func roundTripHTTP1(conn net.Conn, req *http.Request) (*http.Response, error) {
	if err := req.Write(conn); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return resp, nil
}

// decompress wraps resp.Body to inflate gzip/deflate payloads and strips the
// now-inaccurate Content-Encoding / Content-Length headers. Other encodings
// (br, zstd) are passed through untouched — see the README "Limitations".
func decompress(resp *http.Response) io.ReadCloser {
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "gzip":
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		return &gzipReader{body: resp.Body}
	case "deflate":
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
		resp.ContentLength = -1
		return &wrappedReadCloser{r: flate.NewReader(resp.Body), body: resp.Body}
	default:
		return resp.Body
	}
}

// gzipReader lazily initialises the gzip.Reader on first Read, so an empty or
// error body does not fail construction.
type gzipReader struct {
	body io.ReadCloser
	zr   *gzip.Reader
	err  error
}

func (g *gzipReader) Read(p []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	if g.zr == nil {
		zr, err := gzip.NewReader(g.body)
		if err != nil {
			g.err = err
			return 0, err
		}
		g.zr = zr
	}
	return g.zr.Read(p)
}

func (g *gzipReader) Close() error {
	if g.zr != nil {
		_ = g.zr.Close()
	}
	return g.body.Close()
}

// wrappedReadCloser reads from r but closes both r and the underlying body.
type wrappedReadCloser struct {
	r    io.ReadCloser
	body io.ReadCloser
}

func (w *wrappedReadCloser) Read(p []byte) (int, error) { return w.r.Read(p) }
func (w *wrappedReadCloser) Close() error {
	_ = w.r.Close()
	return w.body.Close()
}

// connReleasingBody closes the underlying TLS connection once (and only once)
// when the response body is closed, since we dial per request.
type connReleasingBody struct {
	io.ReadCloser
	conn net.Conn
	once sync.Once
}

func (c *connReleasingBody) Close() error {
	err := c.ReadCloser.Close()
	c.once.Do(func() { _ = c.conn.Close() })
	return err
}
