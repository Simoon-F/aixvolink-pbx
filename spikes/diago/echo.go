// Package diagospike validates Diago's G.711 RTP media primitives.
package diagospike

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtp"
)

const readPollInterval = 200 * time.Millisecond

// ErrIdleTimeout reports that no RTP arrived within the configured interval.
var ErrIdleTimeout = errors.New("rtp idle timeout")

// Config defines the RTP echo listener and its bounded lifetime.
type Config struct {
	BindIP      net.IP
	Port        int
	IdleTimeout time.Duration
}

// Echo owns one Diago media session and echoes PCMU or PCMA RTP packets.
type Echo struct {
	cfg       Config
	ready     chan struct{}
	readyOnce sync.Once
	localAddr net.UDPAddr
}

// NewEcho constructs a stopped G.711 echo endpoint.
func NewEcho(cfg Config) (*Echo, error) {
	if cfg.BindIP == nil {
		return nil, fmt.Errorf("bind IP is required")
	}
	if cfg.Port < 0 || cfg.Port > 65534 {
		return nil, fmt.Errorf("port must be between 0 and 65534")
	}
	if cfg.IdleTimeout <= 0 {
		return nil, fmt.Errorf("idle timeout must be positive")
	}

	return &Echo{cfg: cfg, ready: make(chan struct{})}, nil
}

// Ready is closed after RTP and RTCP sockets have been bound.
func (e *Echo) Ready() <-chan struct{} {
	return e.ready
}

// LocalAddr returns the RTP address after Ready has closed.
func (e *Echo) LocalAddr() net.UDPAddr {
	return e.localAddr
}

// Run echoes G.711 RTP until cancellation, inactivity, or a network error.
func (e *Echo) Run(ctx context.Context) (runErr error) {
	session, err := media.NewMediaSession(e.cfg.BindIP, e.cfg.Port)
	if err != nil {
		return fmt.Errorf("create Diago media session: %w", err)
	}
	defer func() { runErr = errors.Join(runErr, session.Close()) }()

	session.Codecs = []media.Codec{media.CodecAudioUlaw, media.CodecAudioAlaw}
	e.localAddr = session.Laddr
	e.readyOnce.Do(func() { close(e.ready) })

	buffer := make([]byte, media.RTPBufSize)
	packet := rtp.Packet{}
	lastPacketAt := time.Now()

	for {
		if err := session.StopRTP(1, readPollInterval); err != nil {
			return fmt.Errorf("set RTP read deadline: %w", err)
		}

		_, readErr := session.ReadRTP(buffer, &packet)
		if readErr != nil {
			if errors.Is(readErr, net.ErrClosed) && ctx.Err() != nil {
				return nil
			}
			var networkErr net.Error
			if errors.As(readErr, &networkErr) && networkErr.Timeout() {
				if ctx.Err() != nil {
					return nil
				}
				if time.Since(lastPacketAt) >= e.cfg.IdleTimeout {
					return ErrIdleTimeout
				}
				continue
			}
			return fmt.Errorf("read RTP: %w", readErr)
		}

		if packet.PayloadType != media.CodecAudioUlaw.PayloadType && packet.PayloadType != media.CodecAudioAlaw.PayloadType {
			return fmt.Errorf("unsupported RTP payload type %d", packet.PayloadType)
		}

		remoteAddr, ok := session.ReadRTPFromAddr.(*net.UDPAddr)
		if !ok {
			return fmt.Errorf("rtp source is not UDP")
		}
		session.SetRemoteAddr(remoteAddr)
		if err := session.WriteRTP(&packet); err != nil {
			return fmt.Errorf("echo RTP: %w", err)
		}
		lastPacketAt = time.Now()
	}
}
