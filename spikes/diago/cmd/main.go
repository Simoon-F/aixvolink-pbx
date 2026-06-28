// Command diago-spike runs the Phase 0 G.711 RTP echo validation.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	diagospike "github.com/Simoon-F/aixvolink-pbx/spikes/diago"
)

func main() {
	bindIP := flag.String("bind-ip", "127.0.0.1", "RTP bind IP")
	port := flag.Int("port", 16000, "RTP port; RTCP uses the next port")
	idleTimeout := flag.Duration("idle-timeout", 30*time.Second, "exit after this period without RTP")
	flag.Parse()

	echo, err := diagospike.NewEcho(diagospike.Config{
		BindIP:      net.ParseIP(*bindIP),
		Port:        *port,
		IdleTimeout: *idleTimeout,
	})
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := echo.Run(ctx); err != nil && !errors.Is(err, diagospike.ErrIdleTimeout) {
		slog.Error("Diago echo stopped", "error", err)
		os.Exit(1)
	}
}
