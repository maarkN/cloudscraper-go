# cloudscraper-go

[![ci](https://github.com/maarkN/cloudscraper-go/actions/workflows/ci.yml/badge.svg)](https://github.com/maarkN/cloudscraper-go/actions/workflows/ci.yml)
![go](https://img.shields.io/badge/go-%3E%3D1.25-00ADD8)
![license](https://img.shields.io/badge/license-MIT-blue)

A native **Go** library and CLI to fetch anti-bot–protected pages — **TLS/JA3
fingerprint spoofing via [uTLS](https://github.com/refraction-networking/utls)**,
no Python, no headless browser for the common cases. A single static binary.

> **Status:** early. **Milestone M1 (browser TLS fingerprint) is done and
> demonstrable** — the `fingerprint` command below proves a Chrome-matching
> JA3/JA4 over HTTP/2. Sessions, a concurrent crawler and a server/MCP bridge are
> on the roadmap.

## Problem

Cloudflare, Akamai and friends increasingly block by **how you connect**, not who
you say you are. Go's `net/http` has a distinctive TLS ClientHello (JA3/JA4) that
these systems flag instantly — so a plain `http.Get` gets `403 / Access Denied`
on sites a real browser loads fine. cloudscraper-go makes the Go client's TLS
handshake indistinguishable from a real browser's, which clears the most common
"blocked by fingerprint" cases without spinning up Chrome.

## How it works

The core is a custom `http.RoundTripper` that performs the TLS handshake with a
browser's ClientHello (via uTLS), then speaks HTTP/2 or HTTP/1.1 over that
connection. Cookies and redirects are handled by the standard `net/http.Client`.

```mermaid
flowchart LR
  App["Your code / CLI"] -->|http.Client| RT["uTLS RoundTripper"]
  RT -->|"browser ClientHello (JA3/JA4)"| TLS["TLS handshake"]
  TLS -->|ALPN| H{h2?}
  H -->|yes| H2["golang.org/x/net/http2"]
  H -->|no| H1["HTTP/1.1"]
  H2 --> Site[(Protected site)]
  H1 --> Site
```

## Quick start

### CLI

```bash
go install github.com/maarkN/cloudscraper-go/cmd/cloudscraper@latest

# Fetch a page with a Chrome fingerprint (body -> stdout, status/headers -> stderr)
cloudscraper fetch https://example.com --dump-headers

# Prove the fingerprint: what JA3/JA4 does the server actually see?
cloudscraper fingerprint --profile chrome
```

`fingerprint` output (real):

```
profile:       chrome
http_version:  h2
ja3_hash:      96f0044b9649e05e8e079d698cc65a77
ja4:           t13d1516h2_8daaf6152771_d8a2da3f94cd
user_agent:    Mozilla/5.0 (...) Chrome/131.0.0.0 Safari/537.36
```

Switch `--profile firefox` and the JA3/JA4 changes to Firefox's — the cipher and
extension ordering is genuinely different, not just the User-Agent.

### Library

```go
package main

import (
	"context"
	"fmt"

	"github.com/maarkN/cloudscraper-go/pkg/cloudscraper"
)

func main() {
	client, _ := cloudscraper.New(cloudscraper.WithProfile("chrome"))
	resp, err := client.Get(context.Background(), "https://example.com")
	if err != nil {
		panic(err)
	}
	fmt.Println(resp.Proto, resp.StatusCode, len(resp.Body))
}
```

Options: `WithProfile`, `WithTimeout`, `WithoutRedirects`, `WithHeader`,
`WithProxy`, `WithRetries`.

### Sessions, retries & proxy (M2)

A `Client` is a hot session: its cookie jar persists across calls, so a login or
challenge cookie set on one request rides along on the next.

```go
client, _ := cloudscraper.New(
	cloudscraper.WithProfile("chrome"),
	cloudscraper.WithRetries(3),                       // network / 429 / 5xx, exp. backoff + jitter
	cloudscraper.WithProxy("http://user:pass@host:8080"), // http CONNECT or socks5://
)
a, _ := client.Get(ctx, "https://site/login")   // sets a cookie
b, _ := client.Get(ctx, "https://site/account") // cookie is reused automatically
```

Retries honour `Retry-After` and the request context; they only replay requests
whose body can be rewound. From the CLI: `--retries` and `--proxy`.

## Scope & limitations (read this)

Being explicit about what pure Go can and can't do is a feature, not a caveat.

| Protection layer | Handled in pure Go? |
|---|---|
| TLS ClientHello fingerprint (JA3 / JA4) | ✅ Yes — via uTLS. **Done (M1).** |
| Browser-like request headers & User-Agent | ✅ Values yes; exact **on-wire header order** not yet (see below). |
| HTTP/2 frame fingerprint (Akamai h2 hash) | ⚠️ Not yet — currently Go's `x/net/http2` SETTINGS, not the browser's. |
| Legacy JS challenge (IUAM) | ⚠️ Partial — needs a JS engine (`goja`); planned via a pluggable solver. |
| Turnstile / modern Managed Challenge | ❌ Needs a headless browser or a solving service (planned pluggable fallback). |

**What this means today:** the JA3/JA4 layer matches a real browser, which clears
the common "blocked by IP/TLS fingerprint" cases. Two known gaps are documented
rather than hidden:

- **HTTP/2 frame fingerprint** — `cloudscraper fingerprint` still reports Go's
  `akamai_fingerprint`, because we use the standard `x/net/http2`. Matching
  Chrome's h2 SETTINGS/frame order needs a customized HTTP/2 layer (roadmap).
- **Header order** — `net/http` reorders headers on the wire, so the byte-exact
  browser header order isn't reproduced yet.

For heavy JS challenges, the plan is a **pluggable fallback** (headless via
`chromedp`, or an external solver) — deliberately out of the fast path.

## Design decisions & trade-offs

- **uTLS over a headless browser.** For the fingerprint layer, uTLS is orders of
  magnitude cheaper than driving Chrome, and ships as one static binary. Headless
  is reserved as an opt-in fallback for challenges Go can't evaluate.
- **Compose `http.RoundTripper`, don't fork `net/http`.** Cookies, redirects and
  the `Client` API come for free; we only replace the transport. The cost is the
  two gaps above (h2 frames, header order), which a fork would fix — a considered
  trade-off for M1, revisited later.
- **Dial per request (for now).** Simple and correct; session reuse / pooling is
  M2/M3, where it belongs alongside rate limiting and backpressure.
- **Fail honestly.** `https`-only today; non-`https` returns a clear error rather
  than pretending.

## Benchmarks

`go test -bench . ./internal/transport` (network). Numbers versus the Python
`cloudscraper` approach will be published once M2 (session reuse) lands — a hot
session is where the Go rewrite should pull clearly ahead.

## Roadmap

- [x] **M1 — uTLS transport:** Chrome/Firefox ClientHello, verified against
  `tls.peet.ws`. CLI `fetch` + `fingerprint`.
- [x] **M2 — Sessions:** cookie reuse across requests, retries with exponential
  backoff + jitter (honouring `Retry-After`), and http/socks5 proxy support.
- [ ] **M3 — Concurrent crawler:** worker pool (`errgroup` + semaphore), per-host
  rate limiting, cancellable `context`, Prometheus metrics.
- [ ] **M4 — Server mode:** HTTP daemon that keeps sessions hot (seed in
  `cmd/server`).
- [ ] **M5 — Agent bridge:** expose the server as an MCP tool — the native Go
  backend for [`cloudscraper.js`](https://github.com/maarkN/cloudscraper.js)'s
  agent story.

## Development

```bash
make build       # build both binaries into ./bin
make test        # go test -race -short ./...   (offline, deterministic)
make test-net    # full suite incl. network fingerprint tests
make lint        # golangci-lint
make bench       # network benchmark
make fingerprint # build + run `cloudscraper fingerprint`
```

## License

MIT — see [LICENSE](./LICENSE).

## Credits

TLS fingerprinting by [refraction-networking/utls](https://github.com/refraction-networking/utls).
Inspired by the Python [cloudscraper](https://github.com/VeNoMouS/cloudscraper)
and the author's [cloudscraper.js](https://github.com/maarkN/cloudscraper.js).
