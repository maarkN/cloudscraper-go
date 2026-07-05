package agent_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/maarkN/cloudscraper-go/internal/agent"
)

type fakeScraper struct {
	body    []byte
	cookies []*http.Cookie
	getErr  error
}

func (f *fakeScraper) Get(context.Context, string) (int, http.Header, []byte, error) {
	if f.getErr != nil {
		return 0, nil, nil, f.getErr
	}
	h := http.Header{}
	h.Set("Content-Type", "text/html")
	return 200, h, f.body, nil
}

func (f *fakeScraper) Cookies(string) ([]*http.Cookie, error) { return f.cookies, nil }

// connect wires the agent server to a client over an in-memory transport and
// returns the client session — a full MCP round-trip with no network.
func connect(t *testing.T, s agent.Scraper) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()

	serverT, clientT := mcp.NewInMemoryTransports()
	if _, err := agent.NewServer(s).Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return res, b.String()
}

func TestListTools(t *testing.T) {
	cs := connect(t, &fakeScraper{body: []byte("<h1>Hi</h1>")})
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tl := range res.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"fetch_protected_url", "get_cookies"} {
		if !got[want] {
			t.Errorf("tool %q not advertised", want)
		}
	}
}

func TestFetchReturnsMarkdown(t *testing.T) {
	cs := connect(t, &fakeScraper{body: []byte("<h1>Title</h1><p>Hello <b>world</b></p>")})
	res, text := callText(t, cs, "fetch_protected_url", map[string]any{"url": "https://x.example"})
	if res.IsError {
		t.Fatalf("tool errored: %s", text)
	}
	if !strings.Contains(text, "# Title") {
		t.Errorf("missing markdown heading in: %q", text)
	}
	if !strings.Contains(text, "**world**") {
		t.Errorf("missing markdown bold in: %q", text)
	}
}

func TestFetchRawHTML(t *testing.T) {
	cs := connect(t, &fakeScraper{body: []byte("<h1>Raw</h1>")})
	res, text := callText(t, cs, "fetch_protected_url", map[string]any{"url": "https://x.example", "format": "html"})
	if res.IsError {
		t.Fatalf("tool errored: %s", text)
	}
	if !strings.Contains(text, "<h1>Raw</h1>") {
		t.Errorf("expected raw HTML, got: %q", text)
	}
}

func TestFetchEmptyURLIsToolError(t *testing.T) {
	cs := connect(t, &fakeScraper{})
	res, _ := callText(t, cs, "fetch_protected_url", map[string]any{"url": ""})
	if !res.IsError {
		t.Error("expected IsError for empty url")
	}
}

func TestFetchUpstreamErrorIsToolError(t *testing.T) {
	cs := connect(t, &fakeScraper{getErr: errors.New("boom")})
	res, text := callText(t, cs, "fetch_protected_url", map[string]any{"url": "https://x.example"})
	if !res.IsError {
		t.Errorf("expected IsError, got text: %q", text)
	}
}

func TestGetCookies(t *testing.T) {
	cs := connect(t, &fakeScraper{
		body:    []byte("<html></html>"),
		cookies: []*http.Cookie{{Name: "cf_clearance", Value: "abc"}, {Name: "sid", Value: "42"}},
	})
	res, text := callText(t, cs, "get_cookies", map[string]any{"url": "https://x.example"})
	if res.IsError {
		t.Fatalf("tool errored: %s", text)
	}
	if !strings.Contains(text, "cf_clearance=abc") || !strings.Contains(text, "sid=42") {
		t.Errorf("cookies missing in: %q", text)
	}
}
