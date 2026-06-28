// Package app assembles and runs the Phase 1 SIP service.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	"github.com/Simoon-F/aixvolink-pbx/internal/media/portalloc"
	mediasession "github.com/Simoon-F/aixvolink-pbx/internal/media/session"
	"github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
	"github.com/Simoon-F/aixvolink-pbx/internal/sip/b2bua"
	"github.com/Simoon-F/aixvolink-pbx/internal/sip/registrar"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Config defines the bounded Phase 1 service runtime.
type Config struct {
	BindHost              string
	SIPPort               int
	Realm                 string
	TenantID              registration.TenantID
	NodeID                call.NodeID
	NonceSecret           []byte
	NonceTTL              time.Duration
	MaxReplayEntries      int
	DefaultRegisterExpiry time.Duration
	MinRegisterExpiry     time.Duration
	MaxRegisterExpiry     time.Duration
	RegisterCleanup       time.Duration
	TransactionTimeout    time.Duration
	InviteTimeout         time.Duration
	DispatchTimeout       time.Duration
	MaxActiveCalls        int
	CallMailboxSize       int
	MediaBindIP           netip.Addr
	MediaAdvertisedIP     netip.Addr
	RTPStartPort          uint16
	RTPEndPort            uint16
	RTPReadPoll           time.Duration
	MediaInactivity       time.Duration
	RTCPInterval          time.Duration
	MediaSummaryInterval  time.Duration
	MediaSummaryTimeout   time.Duration
}

// App owns one sipgo user agent and its UDP/TCP listeners.
type App struct {
	cfg       Config
	ua        *sipgo.UserAgent
	server    *sipgo.Server
	manager   *registration.Manager
	registrar *registrar.Handler
	calls     *b2bua.Engine
	ready     chan struct{}
	once      sync.Once
	runMu     sync.Mutex
	ran       bool
}

// New constructs a stopped application.
func New(
	cfg Config,
	credentialStore auth.CredentialStore,
	registrationStore registration.Store,
	publisher call.Publisher,
	mediaPublisher mediasession.Publisher,
	logger *slog.Logger,
) (*App, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	manager, err := registration.NewManager(registrationStore, registration.SystemClock{})
	if err != nil {
		return nil, err
	}
	authenticator, err := auth.New(auth.Config{
		Realm: cfg.Realm, NonceSecret: cfg.NonceSecret, NonceTTL: cfg.NonceTTL,
		MaxReplayEntries: cfg.MaxReplayEntries,
	}, credentialStore, auth.SystemClock{})
	if err != nil {
		return nil, err
	}
	registerHandler, err := registrar.NewHandler(registrar.Config{
		Realm: cfg.Realm, DefaultExpires: cfg.DefaultRegisterExpiry,
		MinExpires: cfg.MinRegisterExpiry, MaxExpires: cfg.MaxRegisterExpiry,
		TransactionTimeout: cfg.TransactionTimeout,
	}, authenticator, manager)
	if err != nil {
		return nil, err
	}

	ua, err := sipgo.NewUA(sipgo.WithUserAgent("AixvoLinkPBX"), sipgo.WithUserAgentHostname(cfg.BindHost))
	if err != nil {
		return nil, fmt.Errorf("create SIP user agent: %w", err)
	}
	server, err := sipgo.NewServer(ua)
	if err != nil {
		_ = ua.Close()
		return nil, fmt.Errorf("create SIP server: %w", err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientHostname(cfg.BindHost), sipgo.WithClientPort(cfg.SIPPort))
	if err != nil {
		_ = ua.Close()
		return nil, fmt.Errorf("create SIP client: %w", err)
	}
	mediaPool, err := portalloc.New(portalloc.Config{BindIP: cfg.MediaBindIP, StartPort: cfg.RTPStartPort, EndPort: cfg.RTPEndPort})
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	mediaFactory, err := mediasession.NewFactory(mediasession.Config{
		AdvertisedIP: cfg.MediaAdvertisedIP, ReadPollInterval: cfg.RTPReadPoll,
		InactivityTimeout: cfg.MediaInactivity, RTCPInterval: cfg.RTCPInterval,
		SummaryInterval: cfg.MediaSummaryInterval, SummaryTimeout: cfg.MediaSummaryTimeout,
	}, mediaPool, mediaPublisher)
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	contact := sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "pbx", Host: cfg.BindHost, Port: cfg.SIPPort}}
	callEngine, err := b2bua.NewEngine(b2bua.Config{
		TenantID: cfg.TenantID, Realm: cfg.Realm, NodeID: cfg.NodeID,
		MaxActiveCalls: cfg.MaxActiveCalls, MailboxSize: cfg.CallMailboxSize,
		InviteTimeout: cfg.InviteTimeout, DispatchTimeout: cfg.DispatchTimeout,
	}, manager, publisher, call.SystemClock{}, client, contact, mediaFactory, logger)
	if err != nil {
		_ = ua.Close()
		return nil, err
	}
	return &App{
		cfg: cfg, ua: ua, server: server, manager: manager,
		registrar: registerHandler, calls: callEngine, ready: make(chan struct{}),
	}, nil
}

// Ready is closed after both SIP sockets are bound.
func (a *App) Ready() <-chan struct{} { return a.ready }

// Run serves UDP/TCP SIP, expires bindings, and performs bounded cleanup.
func (a *App) Run(ctx context.Context) (runErr error) {
	a.runMu.Lock()
	if a.ran {
		a.runMu.Unlock()
		return fmt.Errorf("application can only run once")
	}
	a.ran = true
	a.runMu.Unlock()
	defer func() { runErr = errors.Join(runErr, a.ua.Close()) }()

	a.server.OnRegister(func(request *sip.Request, transaction sip.ServerTransaction) {
		a.registrar.Handle(ctx, request, transaction)
	})
	a.calls.RegisterHandlers(ctx, a.server)
	a.server.OnOptions(func(request *sip.Request, transaction sip.ServerTransaction) {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusOK, "OK", nil))
	})

	address := net.JoinHostPort(a.cfg.BindHost, strconv.Itoa(a.cfg.SIPPort))
	listenConfig := net.ListenConfig{}
	packetConn, err := listenConfig.ListenPacket(ctx, "udp", address)
	if err != nil {
		return fmt.Errorf("listen SIP UDP: %w", err)
	}
	udpConn, ok := packetConn.(*net.UDPConn)
	if !ok {
		_ = packetConn.Close()
		return fmt.Errorf("sip UDP listener has unexpected type %T", packetConn)
	}
	tcpListener, err := listenConfig.Listen(ctx, "tcp", address)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("listen SIP TCP: %w", err)
	}
	a.once.Do(func() { close(a.ready) })

	serveErrors := make(chan error, 2)
	go func() { serveErrors <- a.server.ServeUDP(udpConn) }()
	go func() { serveErrors <- a.server.ServeTCP(tcpListener) }()
	cleanupTicker := time.NewTicker(a.cfg.RegisterCleanup)
	defer cleanupTicker.Stop()

	received := 0
	running := true
	for running {
		select {
		case <-ctx.Done():
			running = false
		case serveErr := <-serveErrors:
			received++
			runErr = errors.Join(runErr, normalizeServeError(serveErr))
			if runErr == nil {
				runErr = fmt.Errorf("sip listener stopped unexpectedly")
			}
			running = false
		case <-cleanupTicker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, a.cfg.TransactionTimeout)
			_, cleanupErr := a.manager.Expire(cleanupCtx)
			cancel()
			if cleanupErr != nil && ctx.Err() == nil {
				runErr = errors.Join(runErr, cleanupErr)
				running = false
			}
		}
	}
	runErr = errors.Join(runErr, normalizeServeError(udpConn.Close()), normalizeServeError(tcpListener.Close()))
	for received < 2 {
		runErr = errors.Join(runErr, normalizeServeError(<-serveErrors))
		received++
	}
	a.calls.Wait()
	return runErr
}

func validateConfig(cfg Config) error {
	if net.ParseIP(cfg.BindHost) == nil {
		return fmt.Errorf("bind host must be an IP address")
	}
	if cfg.SIPPort <= 0 || cfg.SIPPort > 65535 {
		return fmt.Errorf("SIP port must be between 1 and 65535")
	}
	if cfg.Realm == "" || cfg.TenantID == "" || cfg.NodeID == "" {
		return fmt.Errorf("realm, tenant ID, and node ID are required")
	}
	if cfg.RegisterCleanup <= 0 {
		return fmt.Errorf("registration cleanup interval must be positive")
	}
	if !cfg.MediaBindIP.IsValid() || !cfg.MediaAdvertisedIP.IsValid() {
		return fmt.Errorf("media bind and advertised IPs are required")
	}
	return nil
}

func normalizeServeError(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
