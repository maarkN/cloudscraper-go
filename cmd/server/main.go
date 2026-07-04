// Command server runs cloudscraper-go as an HTTP daemon (milestone M4).
//
// It keeps solved sessions hot — each ?session= id maps to its own long-lived,
// cookie-warm client — and exposes:
//
//	GET    /healthz
//	GET    /fetch?url=...&session=...&profile=...
//	DELETE /sessions/{id}
//	GET    /metrics            (Prometheus)
//
// Graceful shutdown drains in-flight requests on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maarkN/cloudscraper-go/internal/server"
	"github.com/maarkN/cloudscraper-go/pkg/cloudscraper"
)

// clientDoer adapts *cloudscraper.Client to server.Doer.
type clientDoer struct{ c *cloudscraper.Client }

func (d clientDoer) Get(ctx context.Context, url string) (int, http.Header, []byte, error) {
	resp, err := d.c.Get(ctx, url)
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, resp.Body, nil
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	defaultProfile := flag.String("profile", "chrome", "default browser fingerprint profile")
	idleTTL := flag.Duration("idle-ttl", 10*time.Minute, "evict sessions idle longer than this (0 disables)")
	flag.Parse()

	factory := func(profile string) (server.Doer, error) {
		c, err := cloudscraper.New(cloudscraper.WithProfile(profile))
		if err != nil {
			return nil, err
		}
		return clientDoer{c}, nil
	}

	srv := server.New(factory, server.Options{
		DefaultProfile: *defaultProfile,
		IdleTTL:        *idleTTL,
	})
	stopJanitor := srv.StartJanitor(time.Minute)
	defer stopJanitor()

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("cloudscraper server listening on %s (default profile=%s)", *addr, *defaultProfile)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
		os.Exit(1)
	}
	log.Println("stopped")
}
