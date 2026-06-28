package session_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/media/portalloc"
	"github.com/Simoon-F/aixvolink-pbx/internal/media/session"
	sipsdp "github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
	"github.com/pion/rtp"
)

const concurrentMediaSessions = 100
const packetsPerDirection = 50

func TestHundredConcurrentG711AnchoredSessions(t *testing.T) {
	pool, err := portalloc.New(portalloc.Config{
		BindIP: netip.MustParseAddr("127.0.0.1"), StartPort: 28000, EndPort: 28399,
	})
	if err != nil {
		t.Fatalf("portalloc.New() error = %v", err)
	}
	factory, err := session.NewFactory(session.Config{
		AdvertisedIP: netip.MustParseAddr("127.0.0.1"), ReadPollInterval: 20 * time.Millisecond,
		InactivityTimeout: 5 * time.Second, RTCPInterval: time.Second,
		SummaryInterval: time.Second, SummaryTimeout: 20 * time.Millisecond,
	}, pool, session.DiscardPublisher{})
	if err != nil {
		t.Fatalf("NewFactory() error = %v", err)
	}

	type activeMedia struct {
		session *session.Session
		caller  *remotePair
		callee  *remotePair
		result  <-chan error
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := make([]activeMedia, 0, concurrentMediaSessions)
	mapping := sipsdp.Mapping{Name: "PCMU", ClockRate: 8000, CallerPT: 0, CalleePT: 0}
	for range concurrentMediaSessions {
		media, err := factory.New(ctx, testMetadata())
		if err != nil {
			cancel()
			t.Fatalf("Factory.New() error = %v", err)
		}
		caller := newRemotePair(t)
		callee := newRemotePair(t)
		configureSession(t, media, caller, callee, mapping)
		active = append(active, activeMedia{session: media, caller: caller, callee: callee, result: runSession(t, ctx, media)})
	}
	if pool.InUse() != concurrentMediaSessions*2 {
		cancel()
		t.Fatalf("pool InUse() = %d, want %d", pool.InUse(), concurrentMediaSessions*2)
	}

	loadStarted := time.Now()
	for index, media := range active {
		callerTarget, _, _ := media.session.LocalEndpoint(session.CallerLeg)
		calleeTarget, _, _ := media.session.LocalEndpoint(session.CalleeLeg)
		for sequence := 1; sequence <= packetsPerDirection; sequence++ {
			sendRTP(t, media.caller.rtp, callerTarget, rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 0, SSRC: uint32(index + 1), SequenceNumber: uint16(sequence), Timestamp: uint32(sequence * 160)},
				Payload: make([]byte, 160),
			})
			_ = readRTP(t, media.callee.rtp)
			sendRTP(t, media.callee.rtp, calleeTarget, rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 0, SSRC: uint32(index + 1001), SequenceNumber: uint16(sequence), Timestamp: uint32(sequence * 160)},
				Payload: make([]byte, 160),
			})
			_ = readRTP(t, media.caller.rtp)
		}
		snapshot := media.session.Snapshot()
		if snapshot.Caller.Inbound.Packets != packetsPerDirection || snapshot.Callee.Inbound.Packets != packetsPerDirection {
			cancel()
			t.Fatalf("session %d packet counts = %d/%d", index, snapshot.Caller.Inbound.Packets, snapshot.Callee.Inbound.Packets)
		}
	}
	elapsed := time.Since(loadStarted)
	totalPackets := concurrentMediaSessions * packetsPerDirection * 2
	t.Logf("anchored %d RTP packets across %d concurrent calls in %s (%.0f packets/s)", totalPackets, concurrentMediaSessions, elapsed, float64(totalPackets)/elapsed.Seconds())

	cancel()
	for _, media := range active {
		if err := <-media.result; err != nil {
			t.Fatalf("Session.Run() error = %v", err)
		}
	}
	if pool.InUse() != 0 {
		t.Fatalf("pool InUse() after shutdown = %d", pool.InUse())
	}
}
