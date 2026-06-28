// Package sipgospike validates sipgo transport and transaction behavior.
package sipgospike

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

const listenerCount = 2

// Config defines loopback SIP listener addresses for the spike.
type Config struct {
	UDPAddr string
	TCPAddr string
}

// Server owns the bounded set of sipgo listeners used by this spike.
type Server struct {
	cfg       Config
	ready     chan struct{}
	readyOnce sync.Once
}

// NewServer constructs a stopped server.
func NewServer(cfg Config) (*Server, error) {
	if cfg.UDPAddr == "" {
		return nil, fmt.Errorf("udp address is required")
	}
	if cfg.TCPAddr == "" {
		return nil, fmt.Errorf("tcp address is required")
	}

	return &Server{cfg: cfg, ready: make(chan struct{})}, nil
}

// Ready is closed after both listeners have bound their sockets.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// Run serves UDP and TCP until ctx is canceled.
func (s *Server) Run(ctx context.Context) (runErr error) {
	ua, err := sipgo.NewUA()
	if err != nil {
		return fmt.Errorf("create SIP user agent: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, ua.Close()) }()

	server, err := sipgo.NewServer(ua)
	if err != nil {
		return fmt.Errorf("create SIP server: %w", err)
	}
	server.OnOptions(respondOK)

	listenConfig := net.ListenConfig{}
	udpPacketConn, err := listenConfig.ListenPacket(ctx, "udp4", s.cfg.UDPAddr)
	if err != nil {
		return fmt.Errorf("listen on SIP UDP address: %w", err)
	}
	udpConn, ok := udpPacketConn.(*net.UDPConn)
	if !ok {
		_ = udpPacketConn.Close()
		return fmt.Errorf("sip UDP listener has unexpected type %T", udpPacketConn)
	}
	tcpListener, err := listenConfig.Listen(ctx, "tcp4", s.cfg.TCPAddr)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("listen on SIP TCP address: %w", err)
	}

	errorsByListener := make(chan error, listenerCount)
	go func() { errorsByListener <- server.ServeUDP(udpConn) }()
	go func() { errorsByListener <- server.ServeTCP(tcpListener) }()
	s.readyOnce.Do(func() { close(s.ready) })

	receivedErrors := 0
	select {
	case <-ctx.Done():
	case serveErr := <-errorsByListener:
		runErr = normalizeServeError(serveErr)
		receivedErrors++
	}

	runErr = errors.Join(runErr, normalizeServeError(udpConn.Close()), normalizeServeError(tcpListener.Close()))
	for receivedErrors < listenerCount {
		serveErr := <-errorsByListener
		runErr = errors.Join(runErr, normalizeServeError(serveErr))
		receivedErrors++
	}
	if runErr == nil {
		return nil
	}
	return fmt.Errorf("serve SIP listeners: %w", runErr)
}

func normalizeServeError(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func respondOK(req *sip.Request, tx sip.ServerTransaction) {
	response := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	_ = tx.Respond(response)
}
