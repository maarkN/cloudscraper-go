package transport_test

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	utls "github.com/refraction-networking/utls"

	"github.com/maarkN/cloudscraper-go/internal/transport"
)

// TestRoundTripViaHTTPConnectProxy runs the whole path — CONNECT tunnel, then a
// uTLS handshake over that tunnel to a local TLS backend — entirely offline.
func TestRoundTripViaHTTPConnectProxy(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "through-proxy-ok")
	}))
	defer backend.Close()

	var connects int32
	proxyAddr := startConnectProxy(t, &connects)

	proxyURL, _ := url.Parse("http://" + proxyAddr)
	tr := transport.New(utls.HelloChrome_Auto)
	tr.InsecureSkipVerify = true
	tr.Proxy = proxyURL

	client := &http.Client{Transport: tr}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, backend.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request via proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "through-proxy-ok" {
		t.Errorf("body = %q, want through-proxy-ok", body)
	}
	if atomic.LoadInt32(&connects) == 0 {
		t.Error("proxy was never used (no CONNECT observed)")
	}
}

// startConnectProxy runs a minimal HTTP CONNECT proxy on a random local port and
// returns its address. It increments *connects for each CONNECT it tunnels.
func startConnectProxy(t *testing.T, connects *int32) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go tunnelConnect(conn, connects)
		}
	}()
	return ln.Addr().String()
}

func tunnelConnect(client net.Conn, connects *int32) {
	defer func() { _ = client.Close() }()

	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(client, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
		return
	}
	atomic.AddInt32(connects, 1)

	upstream, err := net.Dial("tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer func() { _ = upstream.Close() }()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}
	// Pipe both ways; read the client side via br so buffered TLS bytes are kept.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
}
