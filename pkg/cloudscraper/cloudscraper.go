// Package cloudscraper is the public API: an HTTP client that fetches
// anti-bot–protected pages using a real browser's TLS fingerprint (via uTLS),
// with cookie persistence and redirect handling provided by net/http.
//
//	c, _ := cloudscraper.New(cloudscraper.WithProfile("chrome"))
//	resp, _ := c.Get(context.Background(), "https://example.com")
//	fmt.Println(resp.StatusCode, len(resp.Body))
package cloudscraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/maarkN/cloudscraper-go/internal/fingerprint"
	"github.com/maarkN/cloudscraper-go/internal/transport"
)

// Client is a reusable, concurrency-safe HTTP client with a browser fingerprint.
type Client struct {
	httpClient *http.Client
	headers    []fingerprint.Header
}

type config struct {
	profileName     string
	timeout         time.Duration
	followRedirects bool
	insecure        bool
	extraHeaders    []fingerprint.Header
}

// Option configures a Client.
type Option func(*config)

// WithProfile selects the browser fingerprint profile (default "chrome").
func WithProfile(name string) Option {
	return func(c *config) {
		if name != "" {
			c.profileName = name
		}
	}
}

// WithTimeout sets the overall per-request timeout (default 30s).
func WithTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithoutRedirects disables automatic redirect following.
func WithoutRedirects() Option {
	return func(c *config) { c.followRedirects = false }
}

// WithHeader overrides or adds a default request header.
func WithHeader(name, value string) Option {
	return func(c *config) {
		c.extraHeaders = append(c.extraHeaders, fingerprint.Header{Name: name, Value: value})
	}
}

// WithInsecureSkipVerify disables TLS certificate verification. Testing only.
func WithInsecureSkipVerify() Option {
	return func(c *config) { c.insecure = true }
}

// New builds a Client from the given options.
func New(opts ...Option) (*Client, error) {
	cfg := config{
		profileName:     fingerprint.DefaultProfile,
		timeout:         30 * time.Second,
		followRedirects: true,
	}
	for _, o := range opts {
		o(&cfg)
	}

	profile, err := fingerprint.Get(cfg.profileName)
	if err != nil {
		return nil, err
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}

	tr := transport.New(profile.ClientHelloID)
	tr.InsecureSkipVerify = cfg.insecure

	hc := &http.Client{Transport: tr, Jar: jar, Timeout: cfg.timeout}
	if !cfg.followRedirects {
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}

	headers := append([]fingerprint.Header(nil), profile.Headers...)
	headers = append(headers, cfg.extraHeaders...)

	return &Client{httpClient: hc, headers: headers}, nil
}

// Response is a fully-read HTTP response.
type Response struct {
	StatusCode int
	Proto      string
	Header     http.Header
	Body       []byte
	// URL is the final URL after any redirects.
	URL string
}

// String returns the response body as a string.
func (r *Response) String() string { return string(r.Body) }

// Get fetches url with a GET request, reading the whole body into the Response.
func (c *Client) Get(ctx context.Context, url string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	return c.do(req)
}

// Do sends req (applying the fingerprint headers) and returns the raw response.
// The caller owns resp.Body and must close it. Prefer Get for one-shot fetches.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	c.applyHeaders(req)
	return c.httpClient.Do(req)
}

func (c *Client) do(req *http.Request) (*Response, error) {
	c.applyHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Proto:      resp.Proto,
		Header:     resp.Header,
		Body:       body,
		URL:        resp.Request.URL.String(),
	}, nil
}

// Cookies returns the cookies the jar holds for rawURL.
func (c *Client) Cookies(rawURL string) ([]*http.Cookie, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Jar.Cookies(u), nil
}

func (c *Client) applyHeaders(req *http.Request) {
	for _, h := range c.headers {
		req.Header.Set(h.Name, h.Value)
	}
}
