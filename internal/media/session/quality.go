package session

import (
	"context"
	"time"

	mediartp "github.com/Simoon-F/aixvolink-pbx/internal/media/rtp"
)

// LegSummary contains one leg's inbound, outbound, and peer-reported quality.
type LegSummary struct {
	Inbound            mediartp.Stats
	Outbound           mediartp.SenderStats
	InvalidPackets     uint64
	DTMFEvents         uint64
	RemoteFractionLost uint8
	RemoteTotalLost    uint32
	RemoteJitter       uint32
	RTT                time.Duration
	TimedOut           bool
}

// Summary is an immutable call-correlated media quality sample.
type Summary struct {
	MediaSessionID   ID
	TenantID         string
	CallID           string
	NodeID           string
	CallerLegID      string
	CalleeLegID      string
	SampledAt        time.Time
	Caller           LegSummary
	Callee           LegSummary
	OneWay           bool
	DroppedSummaries uint64
}

// Snapshot returns current lock-free media counters.
func (s *Session) Snapshot() Summary {
	caller := s.legSummary(s.caller, s.calleeToCaller)
	callee := s.legSummary(s.callee, s.callerToCallee)
	startedNanos := s.startedAt.Load()
	oneWay := false
	if startedNanos != 0 && time.Since(time.Unix(0, startedNanos)) >= s.cfg.InactivityTimeout {
		oneWay = (caller.Inbound.Packets > 0) != (callee.Inbound.Packets > 0)
	}
	return Summary{
		MediaSessionID: s.id, TenantID: string(s.metadata.TenantID), CallID: string(s.metadata.CallID), NodeID: string(s.metadata.NodeID),
		CallerLegID: string(s.metadata.CallerID), CalleeLegID: string(s.metadata.CalleeID), SampledAt: time.Now().UTC(),
		Caller: caller, Callee: callee, OneWay: oneWay, DroppedSummaries: s.summaryDrops.Load(),
	}
}

func (s *Session) legSummary(endpoint *endpoint, outbound *mediartp.Rewriter) LegSummary {
	return LegSummary{
		Inbound: endpoint.tracker.Snapshot(), Outbound: outbound.Snapshot(),
		InvalidPackets: endpoint.invalidPackets.Load(), DTMFEvents: endpoint.dtmfEvents.Load(),
		RemoteFractionLost: uint8(endpoint.remoteFractionLost.Load()), RemoteTotalLost: endpoint.remoteTotalLost.Load(),
		RemoteJitter: endpoint.remoteJitter.Load(), RTT: time.Duration(endpoint.rttNanos.Load()), TimedOut: endpoint.timedOut.Load(),
	}
}

func (s *Session) publishSummary(ctx context.Context) {
	publishCtx, cancel := context.WithTimeout(ctx, s.cfg.SummaryTimeout)
	err := s.publisher.PublishMediaSummary(publishCtx, s.Snapshot())
	cancel()
	if err != nil {
		s.summaryDrops.Add(1)
	}
}

// PublishSummary emits one on-demand immutable sample outside packet loops.
func (s *Session) PublishSummary(ctx context.Context) {
	s.publishSummary(ctx)
}
