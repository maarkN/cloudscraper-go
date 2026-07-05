// Package agent exposes cloudscraper-go to AI agents as an MCP server: tools an
// LLM can call to fetch anti-bot–protected pages (as clean Markdown) and read
// their cookies. Built on the official github.com/modelcontextprotocol/go-sdk.
//
// It depends only on a small Scraper interface, so the tools are unit-tested
// end-to-end over an in-memory MCP transport, with no network.
package agent

import (
	"context"
	"net/http"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Scraper is the capability the MCP tools need. *cloudscraper.Client satisfies it
// via a thin adapter (see cmd/cloudscraper).
type Scraper interface {
	Get(ctx context.Context, url string) (status int, header http.Header, body []byte, err error)
	Cookies(url string) ([]*http.Cookie, error)
}

const (
	serverName    = "cloudscraper-go"
	serverVersion = "v0.1.0"
)

// NewServer builds an MCP server exposing the cloudscraper tools backed by s.
func NewServer(s Scraper) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	h := &handlers{scraper: s}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "fetch_protected_url",
		Description: "Fetch a URL behind Cloudflare/anti-bot protection using a real browser TLS fingerprint, returning the page as clean Markdown (default) or raw HTML.",
	}, h.fetchProtectedURL)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_cookies",
		Description: "Solve the anti-bot challenge for a URL and return the resulting cookies (name/value pairs).",
	}, h.getCookies)
	return srv
}

type handlers struct{ scraper Scraper }

type fetchInput struct {
	URL    string `json:"url" jsonschema:"the absolute https URL to fetch"`
	Format string `json:"format,omitempty" jsonschema:"output format: markdown (default) or html"`
}

type fetchOutput struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	Format      string `json:"format"`
}

func (h *handlers) fetchProtectedURL(ctx context.Context, _ *mcp.CallToolRequest, in fetchInput) (*mcp.CallToolResult, fetchOutput, error) {
	if strings.TrimSpace(in.URL) == "" {
		return errorResult("url is required"), fetchOutput{}, nil
	}
	format := strings.ToLower(strings.TrimSpace(in.Format))
	if format == "" {
		format = "markdown"
	}

	status, header, body, err := h.scraper.Get(ctx, in.URL)
	if err != nil {
		return errorResult("fetch failed: " + err.Error()), fetchOutput{}, nil
	}

	rendered := string(body)
	if format == "markdown" {
		if md, mErr := htmltomarkdown.ConvertString(rendered); mErr == nil {
			rendered = strings.TrimSpace(md)
		}
	}

	out := fetchOutput{URL: in.URL, Status: status, ContentType: header.Get("Content-Type"), Format: format}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: rendered}}}, out, nil
}

type cookiesInput struct {
	URL string `json:"url" jsonschema:"the absolute https URL to solve and read cookies from"`
}

type cookiesOutput struct {
	URL     string            `json:"url"`
	Count   int               `json:"count"`
	Cookies map[string]string `json:"cookies"`
}

func (h *handlers) getCookies(ctx context.Context, _ *mcp.CallToolRequest, in cookiesInput) (*mcp.CallToolResult, cookiesOutput, error) {
	if strings.TrimSpace(in.URL) == "" {
		return errorResult("url is required"), cookiesOutput{}, nil
	}
	// Fetch once to solve the challenge / warm the session, then read cookies.
	if _, _, _, err := h.scraper.Get(ctx, in.URL); err != nil {
		return errorResult("fetch failed: " + err.Error()), cookiesOutput{}, nil
	}
	cookies, err := h.scraper.Cookies(in.URL)
	if err != nil {
		return errorResult("read cookies failed: " + err.Error()), cookiesOutput{}, nil
	}

	m := make(map[string]string, len(cookies))
	var b strings.Builder
	for i, c := range cookies {
		m[c.Name] = c.Value
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c.Name + "=" + c.Value)
	}
	out := cookiesOutput{URL: in.URL, Count: len(cookies), Cookies: m}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}}, out, nil
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}
