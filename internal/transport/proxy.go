package transport

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"

	xproxy "golang.org/x/net/proxy"
)

// dialTarget opens a raw TCP connection to addr, routed through the configured
// proxy when one is set. The uTLS handshake then runs over the returned conn.
func (t *Transport) dialTarget(ctx context.Context, addr string) (net.Conn, error) {
	if t.Proxy == nil {
		d := &net.Dialer{Timeout: t.DialTimeout}
		return d.DialContext(ctx, "tcp", addr)
	}
	switch t.Proxy.Scheme {
	case "http", "":
		return t.dialViaHTTPConnect(ctx, addr)
	case "socks5", "socks5h":
		return t.dialViaSOCKS5(ctx, addr)
	default:
		return nil, fmt.Errorf("cloudscraper: unsupported proxy scheme %q", t.Proxy.Scheme)
	}
}

// dialViaHTTPConnect tunnels through an HTTP proxy using CONNECT.
func (t *Transport) dialViaHTTPConnect(ctx context.Context, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: t.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", t.Proxy.Host)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", t.Proxy.Host, err)
	}

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if u := t.Proxy.User; u != nil {
		pw, _ := u.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(u.Username() + ":" + pw))
		req.Header.Set("Proxy-Authorization", "Basic "+cred)
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write CONNECT: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy CONNECT to %s failed: %s", addr, resp.Status)
	}
	// Bytes the proxy already buffered belong to the tunnel — don't lose them.
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// dialViaSOCKS5 tunnels through a SOCKS5 proxy.
func (t *Transport) dialViaSOCKS5(ctx context.Context, addr string) (net.Conn, error) {
	var auth *xproxy.Auth
	if u := t.Proxy.User; u != nil {
		pw, _ := u.Password()
		auth = &xproxy.Auth{User: u.Username(), Password: pw}
	}
	d, err := xproxy.SOCKS5("tcp", t.Proxy.Host, auth, &net.Dialer{Timeout: t.DialTimeout})
	if err != nil {
		return nil, fmt.Errorf("socks5 dialer: %w", err)
	}
	if cd, ok := d.(xproxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", addr)
	}
	return d.Dial("tcp", addr)
}

// bufferedConn lets bytes buffered during the CONNECT handshake be consumed by
// the subsequent TLS handshake.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }
