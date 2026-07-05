// Command cloudscraper is the CLI for the cloudscraper-go library: fetch an
// anti-bot–protected URL with a browser TLS fingerprint, or probe what
// fingerprint a server actually sees.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/maarkN/cloudscraper-go/internal/agent"
	"github.com/maarkN/cloudscraper-go/internal/crawl"
	"github.com/maarkN/cloudscraper-go/internal/fingerprint"
	"github.com/maarkN/cloudscraper-go/pkg/cloudscraper"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cloudscraper",
		Short:         "Fetch anti-bot–protected pages with a browser TLS fingerprint (uTLS).",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(fetchCmd(), fingerprintCmd(), crawlCmd(), mcpCmd())
	return root
}

// isDisconnect reports whether err is a normal MCP client disconnect (stdin EOF)
// or a signal-triggered shutdown, rather than a real failure.
func isDisconnect(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		strings.Contains(err.Error(), "EOF")
}

// mcpScraper adapts *cloudscraper.Client to agent.Scraper.
type mcpScraper struct{ c *cloudscraper.Client }

func (s mcpScraper) Get(ctx context.Context, url string) (int, http.Header, []byte, error) {
	resp, err := s.c.Get(ctx, url)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, resp.Body, nil
}

func (s mcpScraper) Cookies(url string) ([]*http.Cookie, error) { return s.c.Cookies(url) }

func mcpCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server over stdio, exposing cloudscraper tools to AI agents",
		Long: "Starts a Model Context Protocol server on stdio exposing the tools\n" +
			"fetch_protected_url and get_cookies. Point any MCP client (Claude Desktop,\n" +
			"IDEs) at `cloudscraper mcp`.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := signalContext()
			defer cancel()
			client, err := cloudscraper.New(cloudscraper.WithProfile(profile))
			if err != nil {
				return err
			}
			// A client disconnect (stdin EOF) or a signal is a clean shutdown.
			err = agent.NewServer(mcpScraper{client}).Run(ctx, &mcp.StdioTransport{})
			if err == nil || isDisconnect(err) {
				return nil
			}
			return err
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", fingerprint.DefaultProfile,
		"browser fingerprint profile ("+strings.Join(fingerprint.Names(), ", ")+")")
	return cmd
}

// clientFetcher adapts *cloudscraper.Client to the crawl.Fetcher interface.
type clientFetcher struct{ c *cloudscraper.Client }

func (f clientFetcher) Fetch(ctx context.Context, rawURL string) (int, []byte, error) {
	resp, err := f.c.Get(ctx, rawURL)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, resp.Body, nil
}

func crawlCmd() *cobra.Command {
	var (
		profile     string
		timeout     time.Duration
		concurrency int
		rps         float64
	)
	cmd := &cobra.Command{
		Use:   "crawl <url>...",
		Short: "Fetch many URLs concurrently (bounded worker pool + per-host rate limit)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx, cancel := signalContext()
			defer cancel()

			client, err := cloudscraper.New(
				cloudscraper.WithProfile(profile),
				cloudscraper.WithTimeout(timeout),
			)
			if err != nil {
				return err
			}

			crawler := crawl.New(clientFetcher{client}, crawl.Options{
				Concurrency: concurrency,
				PerHostRPS:  rps,
			})
			results, err := crawler.Crawl(ctx, args)
			for _, r := range results {
				if r.Err != nil {
					fmt.Fprintf(os.Stderr, "ERR  %s  %v\n", r.URL, r.Err)
					continue
				}
				fmt.Fprintf(os.Stderr, "%3d  %-50s  %d bytes  %s\n",
					r.StatusCode, r.URL, len(r.Body), r.Duration.Round(time.Millisecond))
			}
			return err
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", fingerprint.DefaultProfile,
		"browser fingerprint profile ("+strings.Join(fingerprint.Names(), ", ")+")")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 30*time.Second, "per-request timeout")
	cmd.Flags().IntVarP(&concurrency, "concurrency", "c", 0, "max concurrent fetches (0 = NumCPU*2)")
	cmd.Flags().Float64Var(&rps, "rps", 0, "per-host requests/second (0 = unlimited)")
	return cmd
}

func fetchCmd() *cobra.Command {
	var (
		profile     string
		timeout     time.Duration
		dumpHeaders bool
		noRedirect  bool
		insecure    bool
		proxy       string
		retries     int
	)
	cmd := &cobra.Command{
		Use:   "fetch <url>",
		Short: "GET a URL and print the body to stdout (status/headers go to stderr)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			ctx, cancel := signalContext()
			defer cancel()

			opts := []cloudscraper.Option{
				cloudscraper.WithProfile(profile),
				cloudscraper.WithTimeout(timeout),
				cloudscraper.WithRetries(retries),
			}
			if noRedirect {
				opts = append(opts, cloudscraper.WithoutRedirects())
			}
			if insecure {
				opts = append(opts, cloudscraper.WithInsecureSkipVerify())
			}
			if proxy != "" {
				opts = append(opts, cloudscraper.WithProxy(proxy))
			}

			client, err := cloudscraper.New(opts...)
			if err != nil {
				return err
			}
			resp, err := client.Get(ctx, args[0])
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stderr, "%s %d\n", resp.Proto, resp.StatusCode)
			if dumpHeaders {
				for name, values := range resp.Header {
					for _, v := range values {
						fmt.Fprintf(os.Stderr, "%s: %s\n", name, v)
					}
				}
				fmt.Fprintln(os.Stderr)
			}
			_, err = os.Stdout.Write(resp.Body)
			return err
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", fingerprint.DefaultProfile,
		"browser fingerprint profile ("+strings.Join(fingerprint.Names(), ", ")+")")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 30*time.Second, "overall request timeout")
	cmd.Flags().BoolVar(&dumpHeaders, "dump-headers", false, "print response headers to stderr")
	cmd.Flags().BoolVar(&noRedirect, "no-redirect", false, "do not follow redirects")
	cmd.Flags().BoolVar(&insecure, "insecure", false, "skip TLS certificate verification")
	cmd.Flags().StringVar(&proxy, "proxy", "", "proxy URL (http://host:port or socks5://host:port)")
	cmd.Flags().IntVar(&retries, "retries", 2, "retries on transient failures (network / 429 / 5xx)")
	return cmd
}

// fingerprintCmd proves the fingerprint end-to-end by asking tls.peet.ws what
// JA3/JA4 it observed — a Chrome profile should report a Chrome-like JA3, not
// Go's default.
func fingerprintCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{
		Use:   "fingerprint",
		Short: "Probe tls.peet.ws and print the JA3/JA4 the server sees",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, cancel := signalContext()
			defer cancel()

			client, err := cloudscraper.New(cloudscraper.WithProfile(profile))
			if err != nil {
				return err
			}
			resp, err := client.Get(ctx, "https://tls.peet.ws/api/all")
			if err != nil {
				return err
			}

			var data struct {
				TLS struct {
					JA3     string `json:"ja3"`
					JA3Hash string `json:"ja3_hash"`
					JA4     string `json:"ja4"`
				} `json:"tls"`
				HTTP2 struct {
					AkamaiFingerprint string `json:"akamai_fingerprint"`
				} `json:"http2"`
				HTTPVersion string `json:"http_version"`
				UserAgent   string `json:"user_agent"`
			}
			if err := json.Unmarshal(resp.Body, &data); err != nil || data.TLS.JA3Hash == "" {
				// Schema changed or unexpected payload — show the raw response.
				_, _ = os.Stdout.Write(resp.Body)
				return nil
			}

			fmt.Printf("profile:       %s\n", profile)
			fmt.Printf("http_version:  %s\n", data.HTTPVersion)
			fmt.Printf("ja3:           %s\n", data.TLS.JA3)
			fmt.Printf("ja3_hash:      %s\n", data.TLS.JA3Hash)
			fmt.Printf("ja4:           %s\n", data.TLS.JA4)
			fmt.Printf("akamai_h2:     %s\n", data.HTTP2.AkamaiFingerprint)
			fmt.Printf("user_agent:    %s\n", data.UserAgent)
			return nil
		},
	}
	cmd.Flags().StringVarP(&profile, "profile", "p", fingerprint.DefaultProfile,
		"browser fingerprint profile ("+strings.Join(fingerprint.Names(), ", ")+")")
	return cmd
}

// signalContext returns a context cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}
