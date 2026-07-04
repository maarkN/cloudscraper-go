// Command server exposes cloudscraper-go over HTTP.
//
// This is an early seed of milestone M4 (daemon / hot-session server): it serves
// GET /fetch?url=... and GET /healthz, with graceful shutdown on SIGINT/SIGTERM.
// Session pooling and an MCP bridge come later.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maarkN/cloudscraper-go/pkg/cloudscraper"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	profile := flag.String("profile", "chrome", "browser fingerprint profile")
	flag.Parse()

	client, err := cloudscraper.New(cloudscraper.WithProfile(*profile))
	if err != nil {
		log.Fatalf("init client: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/fetch", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("url")
		if target == "" {
			http.Error(w, "missing ?url=", http.StatusBadRequest)
			return
		}
		resp, err := client.Get(r.Context(), target)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("X-Upstream-Status", fmt.Sprintf("%d", resp.StatusCode))
		w.Header().Set("X-Upstream-Proto", resp.Proto)
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write(resp.Body)
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown: signal -> stop accepting -> drain in-flight -> close.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("cloudscraper server listening on %s (profile=%s)", *addr, *profile)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
		os.Exit(1)
	}
	log.Println("stopped")
}
