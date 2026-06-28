// Command sipgo-spike runs the Phase 0 sipgo listener validation.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	sipgospike "github.com/Simoon-F/aixvolink-pbx/spikes/sipgo"
)

func main() {
	udpAddr := flag.String("udp", "127.0.0.1:15060", "UDP listen address")
	tcpAddr := flag.String("tcp", "127.0.0.1:15060", "TCP listen address")
	flag.Parse()

	server, err := sipgospike.NewServer(sipgospike.Config{UDPAddr: *udpAddr, TCPAddr: *tcpAddr})
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("starting sipgo spike", "udp_addr", *udpAddr, "tcp_addr", *tcpAddr)
	if err := server.Run(ctx); err != nil {
		slog.Error("sipgo spike stopped", "error", err)
		os.Exit(1)
	}
}
