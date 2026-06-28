package session

import (
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/media/portalloc"
	mediartp "github.com/Simoon-F/aixvolink-pbx/internal/media/rtp"
	sipsdp "github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
	"github.com/pion/rtcp"
)

type remoteConfig struct {
	rtp       netip.AddrPort
	rtcp      netip.AddrPort
	audioPT   uint8
	dtmfPT    uint8
	hasDTMF   bool
	direction sipsdp.Direction
}

type endpoint struct {
	leg                Leg
	lease              *portalloc.Lease
	remote             atomic.Pointer[remoteConfig]
	learnedRTPPort     atomic.Uint32
	learnedRTCPPort    atomic.Uint32
	tracker            *mediartp.Tracker
	invalidPackets     atomic.Uint64
	dtmfEvents         atomic.Uint64
	timedOut           atomic.Bool
	remoteFractionLost atomic.Uint32
	remoteTotalLost    atomic.Uint32
	remoteJitter       atomic.Uint32
	rttNanos           atomic.Int64
	lastRemoteSR       atomic.Uint32
	lastRemoteSRNanos  atomic.Int64
	lastSentSR         atomic.Uint32
}

func newEndpoint(leg Leg, lease *portalloc.Lease) *endpoint {
	return &endpoint{leg: leg, lease: lease, tracker: mediartp.NewTracker(8000)}
}

func (e *endpoint) configure(remote sipsdp.Endpoint, audioPT, dtmfPT uint8, hasDTMF bool) {
	current := e.remote.Load()
	if current == nil || current.rtp != remote.RTP || current.rtcp != remote.RTCP {
		e.learnedRTPPort.Store(0)
		e.learnedRTCPPort.Store(0)
	}
	e.remote.Store(&remoteConfig{
		rtp: remote.RTP, rtcp: remote.RTCP, audioPT: audioPT, dtmfPT: dtmfPT,
		hasDTMF: hasDTMF, direction: remote.Direction,
	})
}

func (e *endpoint) acceptsRTP(source netip.AddrPort) bool {
	remote := e.remote.Load()
	if remote == nil || source.Addr() != remote.rtp.Addr() {
		return false
	}
	learned := e.learnedRTPPort.Load()
	if learned == 0 {
		e.learnedRTPPort.CompareAndSwap(0, uint32(source.Port()))
		learned = e.learnedRTPPort.Load()
	}
	return learned == uint32(source.Port())
}

func (e *endpoint) acceptsRTCP(source netip.AddrPort) bool {
	remote := e.remote.Load()
	if remote == nil || source.Addr() != remote.rtcp.Addr() {
		return false
	}
	learned := e.learnedRTCPPort.Load()
	if learned == 0 {
		e.learnedRTCPPort.CompareAndSwap(0, uint32(source.Port()))
		learned = e.learnedRTCPPort.Load()
	}
	return learned == uint32(source.Port())
}

func (e *endpoint) rtpTarget() (netip.AddrPort, bool) {
	remote := e.remote.Load()
	if remote == nil {
		return netip.AddrPort{}, false
	}
	port := e.learnedRTPPort.Load()
	if port == 0 {
		return remote.rtp, true
	}
	return netip.AddrPortFrom(remote.rtp.Addr(), uint16(port)), true
}

func (e *endpoint) rtcpTarget() (netip.AddrPort, bool) {
	remote := e.remote.Load()
	if remote == nil {
		return netip.AddrPort{}, false
	}
	port := e.learnedRTCPPort.Load()
	if port == 0 {
		return remote.rtcp, true
	}
	return netip.AddrPortFrom(remote.rtcp.Addr(), uint16(port)), true
}

func (e *endpoint) canSend() bool {
	remote := e.remote.Load()
	return remote != nil && (remote.direction == sipsdp.DirectionSendRecv || remote.direction == sipsdp.DirectionSendOnly)
}

func (e *endpoint) canReceive() bool {
	remote := e.remote.Load()
	return remote != nil && (remote.direction == sipsdp.DirectionSendRecv || remote.direction == sipsdp.DirectionRecvOnly)
}

func (e *endpoint) updateRemoteReport(reports []rtcp.ReceptionReport, senderSSRC uint32, receivedAt time.Time) {
	for _, report := range reports {
		if report.SSRC != senderSSRC {
			continue
		}
		e.remoteFractionLost.Store(uint32(report.FractionLost))
		e.remoteTotalLost.Store(report.TotalLost)
		e.remoteJitter.Store(report.Jitter)
		if report.LastSenderReport != 0 && report.LastSenderReport == e.lastSentSR.Load() {
			arrival := compactNTP(ntpTimestamp(receivedAt))
			if arrival > report.LastSenderReport+report.Delay {
				rttUnits := arrival - report.LastSenderReport - report.Delay
				e.rttNanos.Store(int64(time.Duration(rttUnits) * time.Second / 65536))
			}
		}
		return
	}
}
