// Package session owns one bounded two-leg anchored RTP/RTCP media session.
package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	"github.com/Simoon-F/aixvolink-pbx/internal/media/portalloc"
	mediartp "github.com/Simoon-F/aixvolink-pbx/internal/media/rtp"
	sipsdp "github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

const maxDatagramSize = 1600

// ID is one negotiated media interval.
type ID string

// Leg identifies a session-facing SIP leg.
type Leg string

const (
	CallerLeg Leg = "caller"
	CalleeLeg Leg = "callee"
)

// Metadata correlates media quality with a business call.
type Metadata struct {
	TenantID registration.TenantID
	CallID   call.ID
	NodeID   call.NodeID
	CallerID call.LegID
	CalleeID call.LegID
}

// NewMetadata constructs correlated metadata at the SIP/media boundary.
func NewMetadata(tenantID string, callID call.ID, nodeID call.NodeID, callerID, calleeID call.LegID) Metadata {
	return Metadata{TenantID: registration.TenantID(tenantID), CallID: callID, NodeID: nodeID, CallerID: callerID, CalleeID: calleeID}
}

// Config bounds media worker timing and advertised addressing.
type Config struct {
	AdvertisedIP      netip.Addr
	ReadPollInterval  time.Duration
	InactivityTimeout time.Duration
	RTCPInterval      time.Duration
	SummaryInterval   time.Duration
	SummaryTimeout    time.Duration
}

// Publisher receives periodic immutable quality summaries outside packet loops.
type Publisher interface {
	PublishMediaSummary(ctx context.Context, summary Summary) error
}

// DiscardPublisher explicitly drops media summaries.
type DiscardPublisher struct{}

// PublishMediaSummary implements Publisher.
func (DiscardPublisher) PublishMediaSummary(context.Context, Summary) error { return nil }

// Factory owns the shared bounded port pool.
type Factory struct {
	cfg       Config
	pool      *portalloc.Pool
	publisher Publisher
}

// NewFactory constructs a media factory without opening sockets.
func NewFactory(cfg Config, pool *portalloc.Pool, publisher Publisher) (*Factory, error) {
	if !cfg.AdvertisedIP.IsValid() || cfg.AdvertisedIP.IsUnspecified() {
		return nil, fmt.Errorf("media advertised IP must be a specified address")
	}
	if cfg.ReadPollInterval <= 0 || cfg.InactivityTimeout <= cfg.ReadPollInterval {
		return nil, fmt.Errorf("media inactivity timeout must exceed the read poll interval")
	}
	if cfg.RTCPInterval <= 0 || cfg.SummaryInterval <= 0 || cfg.SummaryTimeout <= 0 {
		return nil, fmt.Errorf("media RTCP and summary intervals must be positive")
	}
	if pool == nil || publisher == nil {
		return nil, fmt.Errorf("media port pool and summary publisher are required")
	}
	return &Factory{cfg: cfg, pool: pool, publisher: publisher}, nil
}

// New leases both leg-facing socket pairs atomically from the caller's perspective.
func (f *Factory) New(ctx context.Context, metadata Metadata) (*Session, error) {
	callerLease, err := f.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("lease caller media ports: %w", err)
	}
	calleeLease, err := f.pool.Acquire(ctx)
	if err != nil {
		_ = callerLease.Release()
		return nil, fmt.Errorf("lease callee media ports: %w", err)
	}
	callerToCallee, err := mediartp.NewRewriter()
	if err != nil {
		_ = callerLease.Release()
		_ = calleeLease.Release()
		return nil, err
	}
	calleeToCaller, err := mediartp.NewRewriter()
	if err != nil {
		_ = callerLease.Release()
		_ = calleeLease.Release()
		return nil, err
	}
	mediaID, err := uuid.NewV7()
	if err != nil {
		_ = callerLease.Release()
		_ = calleeLease.Release()
		return nil, fmt.Errorf("generate media session ID: %w", err)
	}
	return &Session{
		id: ID(mediaID.String()), metadata: metadata, cfg: f.cfg, publisher: f.publisher,
		caller: newEndpoint(CallerLeg, callerLease), callee: newEndpoint(CalleeLeg, calleeLease),
		callerToCallee: callerToCallee, calleeToCaller: calleeToCaller,
		done: make(chan struct{}),
	}, nil
}

// Session owns two RTP/RTCP socket pairs and their packet workers.
type Session struct {
	id             ID
	metadata       Metadata
	cfg            Config
	publisher      Publisher
	caller         *endpoint
	callee         *endpoint
	callerToCallee *mediartp.Rewriter
	calleeToCaller *mediartp.Rewriter
	mapping        atomic.Pointer[sipsdp.Mapping]
	startedAt      atomic.Int64
	summaryDrops   atomic.Uint64
	runOnce        sync.Once
	closeOnce      sync.Once
	done           chan struct{}
	runErr         error
}

// ID returns the stable media session identifier.
func (s *Session) ID() ID { return s.id }

// LocalEndpoint returns the advertised leg-facing RTP and RTCP pair.
func (s *Session) LocalEndpoint(leg Leg) (netip.AddrPort, netip.AddrPort, error) {
	endpoint, err := s.endpoint(leg)
	if err != nil {
		return netip.AddrPort{}, netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(s.cfg.AdvertisedIP, endpoint.lease.RTPPort()), netip.AddrPortFrom(s.cfg.AdvertisedIP, endpoint.lease.RTCPPort()), nil
}

// Configure applies the completed offer/answer mapping before media starts.
func (s *Session) Configure(caller, callee sipsdp.Endpoint, mapping sipsdp.Mapping) error {
	if mapping.Name == "" || mapping.ClockRate == 0 {
		return fmt.Errorf("media payload mapping is required")
	}
	s.caller.configure(caller, mapping.CallerPT, mapping.CallerDTMFPT, mapping.HasDTMF)
	s.callee.configure(callee, mapping.CalleePT, mapping.CalleeDTMFPT, mapping.HasDTMF)
	mappingCopy := mapping
	s.mapping.Store(&mappingCopy)
	return nil
}

func (s *Session) endpoint(leg Leg) (*endpoint, error) {
	switch leg {
	case CallerLeg:
		return s.caller, nil
	case CalleeLeg:
		return s.callee, nil
	default:
		return nil, fmt.Errorf("unknown media leg %q", leg)
	}
}

// Run owns packet loops until cancellation, close, or an I/O failure.
func (s *Session) Run(ctx context.Context) error {
	s.runOnce.Do(func() {
		s.runErr = s.run(ctx)
	})
	return s.runErr
}

func (s *Session) run(ctx context.Context) error {
	if s.mapping.Load() == nil || s.caller.remote.Load() == nil || s.callee.remote.Load() == nil {
		return fmt.Errorf("media session must be configured before Run")
	}
	s.startedAt.Store(time.Now().UTC().UnixNano())
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return s.forwardRTP(groupCtx, s.caller, s.callee, s.callerToCallee) })
	group.Go(func() error { return s.forwardRTP(groupCtx, s.callee, s.caller, s.calleeToCaller) })
	group.Go(func() error { return s.readRTCP(groupCtx, s.caller, s.calleeToCaller) })
	group.Go(func() error { return s.readRTCP(groupCtx, s.callee, s.callerToCallee) })
	group.Go(func() error { return s.reportLoop(groupCtx) })
	err := group.Wait()
	closeErr := s.Close()
	if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		err = nil
	}
	return errors.Join(err, closeErr)
}

// Done is closed after sockets are released.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close releases all media resources exactly once.
func (s *Session) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = errors.Join(s.caller.lease.Release(), s.callee.lease.Release())
		close(s.done)
	})
	return closeErr
}
