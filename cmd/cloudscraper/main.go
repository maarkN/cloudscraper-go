// Command cloudscraper is the CLI for the cloudscraper-go library: fetch an
// anti-bot–protected URL with a browser TLS fingerprint, or probe what
// fingerprint a server actually sees.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

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
	root.AddCommand(fetchCmd(), fingerprintCmd())
	return root
}

func fetchCmd() *cobra.Command {
	var (
		profile     string
		timeout     time.Duration
		dumpHeaders bool
		noRedirect  bool
		insecure    bool
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
			}
			if noRedirect {
				opts = append(opts, cloudscraper.WithoutRedirects())
			}
			if insecure {
				opts = append(opts, cloudscraper.WithInsecureSkipVerify())
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
