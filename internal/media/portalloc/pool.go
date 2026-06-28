// Package portalloc owns bounded RTP/RTCP UDP port leases.
package portalloc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
)

var (
	// ErrExhausted indicates that every configured RTP/RTCP pair is leased or unavailable.
	ErrExhausted = errors.New("RTP port pool exhausted")
	// ErrLeaseReleased indicates use of a released lease.
	ErrLeaseReleased = errors.New("RTP port lease released")
)

// Config defines an inclusive range of even RTP ports with adjacent RTCP ports.
type Config struct {
	BindIP    netip.Addr
	StartPort uint16
	EndPort   uint16
}

// Pool allocates a fixed set of UDP port pairs.
type Pool struct {
	bindIP    netip.Addr
	available chan uint16
	inUse     atomic.Int64
}

// Lease owns one bound RTP/RTCP socket pair until Release.
type Lease struct {
	pool     *Pool
	rtpPort  uint16
	rtpConn  *net.UDPConn
	rtcpConn *net.UDPConn
	once     sync.Once
	released atomic.Bool
}

// New constructs a stopped pool without opening sockets.
func New(cfg Config) (*Pool, error) {
	if !cfg.BindIP.IsValid() || cfg.BindIP.IsUnspecified() {
		return nil, fmt.Errorf("media bind IP must be a specified address")
	}
	if cfg.StartPort < 1024 || cfg.StartPort%2 != 0 {
		return nil, fmt.Errorf("RTP start port must be an even port at or above 1024")
	}
	if cfg.EndPort <= cfg.StartPort || cfg.EndPort%2 == 0 {
		return nil, fmt.Errorf("RTCP end port must be odd and above the RTP start port")
	}
	pairCount := (int(cfg.EndPort) - int(cfg.StartPort) + 1) / 2
	if pairCount <= 0 {
		return nil, fmt.Errorf("RTP port range contains no pairs")
	}
	pool := &Pool{bindIP: cfg.BindIP, available: make(chan uint16, pairCount)}
	for port := int(cfg.StartPort); port+1 <= int(cfg.EndPort); port += 2 {
		pool.available <- uint16(port)
	}
	return pool, nil
}

// Acquire binds and returns one available pair or a bounded exhaustion error.
func (p *Pool) Acquire(ctx context.Context) (*Lease, error) {
	if p == nil {
		return nil, fmt.Errorf("RTP port pool is required")
	}
	attempts := cap(p.available)
	failed := make([]uint16, 0, attempts)
	defer func() {
		for _, port := range failed {
			p.available <- port
		}
	}()
	for range attempts {
		var port uint16
		select {
		case port = <-p.available:
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return nil, ErrExhausted
		}
		lease, err := p.bind(port)
		if err != nil {
			failed = append(failed, port)
			continue
		}
		p.inUse.Add(1)
		return lease, nil
	}
	return nil, ErrExhausted
}

func (p *Pool) bind(port uint16) (*Lease, error) {
	network := "udp4"
	if p.bindIP.Is6() {
		network = "udp6"
	}
	rtpAddress := net.UDPAddrFromAddrPort(netip.AddrPortFrom(p.bindIP, port))
	rtpConn, err := net.ListenUDP(network, rtpAddress)
	if err != nil {
		return nil, fmt.Errorf("bind RTP port %d: %w", port, err)
	}
	rtcpAddress := net.UDPAddrFromAddrPort(netip.AddrPortFrom(p.bindIP, port+1))
	rtcpConn, err := net.ListenUDP(network, rtcpAddress)
	if err != nil {
		_ = rtpConn.Close()
		return nil, fmt.Errorf("bind RTCP port %d: %w", port+1, err)
	}
	return &Lease{pool: p, rtpPort: port, rtpConn: rtpConn, rtcpConn: rtcpConn}, nil
}

// RTPPort returns the even RTP port.
func (l *Lease) RTPPort() uint16 { return l.rtpPort }

// RTCPPort returns the odd RTCP port adjacent to RTP.
func (l *Lease) RTCPPort() uint16 { return l.rtpPort + 1 }

// RTPConn returns the bound RTP socket while the lease is active.
func (l *Lease) RTPConn() (*net.UDPConn, error) {
	if l == nil || l.released.Load() {
		return nil, ErrLeaseReleased
	}
	return l.rtpConn, nil
}

// RTCPConn returns the bound RTCP socket while the lease is active.
func (l *Lease) RTCPConn() (*net.UDPConn, error) {
	if l == nil || l.released.Load() {
		return nil, ErrLeaseReleased
	}
	return l.rtcpConn, nil
}

// Release closes both sockets and returns the pair exactly once.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	var releaseErr error
	l.once.Do(func() {
		l.released.Store(true)
		releaseErr = errors.Join(l.rtpConn.Close(), l.rtcpConn.Close())
		l.pool.inUse.Add(-1)
		l.pool.available <- l.rtpPort
	})
	return releaseErr
}

// Capacity returns the configured pair count.
func (p *Pool) Capacity() int { return cap(p.available) }

// InUse returns the current leased-pair count.
func (p *Pool) InUse() int { return int(p.inUse.Load()) }
