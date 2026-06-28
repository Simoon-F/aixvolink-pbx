// Command pion-spike runs the Phase 0 browser WebRTC audio validation.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pionspike "github.com/Simoon-F/aixvolink-pbx/spikes/pion"
)

func main() {
	listenAddr := flag.String("http", "127.0.0.1:18080", "HTTP signaling listen address")
	maxPeers := flag.Int("max-peers", 16, "maximum concurrent peer connections")
	sessionTimeout := flag.Duration("session-timeout", 5*time.Minute, "maximum peer connection lifetime")
	flag.Parse()

	service, err := pionspike.NewService(pionspike.Config{
		MaxPeers:       *maxPeers,
		SessionTimeout: *sessionTimeout,
	})
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              *listenAddr,
		Handler:           service,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.ListenAndServe() }()
	slog.Info("starting Pion browser audio spike", "url", "http://"+*listenAddr)

	select {
	case err := <-serveResult:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server stopped", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP shutdown failed", "error", err)
		}
	}
	if err := service.Close(); err != nil {
		slog.Error("peer cleanup failed", "error", err)
	}
}
