package session_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/media/dtmf"
	"github.com/Simoon-F/aixvolink-pbx/internal/media/portalloc"
	"github.com/Simoon-F/aixvolink-pbx/internal/media/session"
	sipsdp "github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

const mediaTestTimeout = 3 * time.Second

type remotePair struct {
	rtp  *net.UDPConn
	rtcp *net.UDPConn
}

func TestSessionAnchorsG711DTMFAndRTCPBothDirections(t *testing.T) {
	media, pool := newConfiguredSession(t, 25000, 25007, 500*time.Millisecond)
	caller := newRemotePair(t)
	callee := newRemotePair(t)
	mapping := sipsdp.Mapping{
		Name: "PCMU", ClockRate: 8000, CallerPT: 0, CalleePT: 0,
		HasDTMF: true, CallerDTMFPT: 101, CalleeDTMFPT: 110,
	}
	configureSession(t, media, caller, callee, mapping)
	ctx, cancel := context.WithCancel(context.Background())
	result := runSession(t, ctx, media)

	callerLocal, callerRTCP, _ := media.LocalEndpoint(session.CallerLeg)
	calleeLocal, _, _ := media.LocalEndpoint(session.CalleeLeg)
	sendRTP(t, caller.rtp, callerLocal, rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SSRC: 100, SequenceNumber: 1, Timestamp: 160}, Payload: make([]byte, 160)})
	forwardedToCallee := readRTP(t, callee.rtp)
	if forwardedToCallee.PayloadType != 0 || forwardedToCallee.SSRC == 100 || len(forwardedToCallee.Payload) != 160 {
		t.Fatalf("caller RTP was not rewritten: %+v", forwardedToCallee.Header)
	}

	dtmfPayload, err := dtmf.Encode(dtmf.Event{Code: 5, End: true, Volume: 10, Duration: 800})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	sendRTP(t, caller.rtp, callerLocal, rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 101, SSRC: 100, SequenceNumber: 2, Timestamp: 320}, Payload: dtmfPayload})
	forwardedDTMF := readRTP(t, callee.rtp)
	if forwardedDTMF.PayloadType != 110 {
		t.Fatalf("DTMF payload type = %d, want 110", forwardedDTMF.PayloadType)
	}

	sendRTP(t, callee.rtp, calleeLocal, rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SSRC: 200, SequenceNumber: 10, Timestamp: 1000}, Payload: make([]byte, 160)})
	forwardedToCaller := readRTP(t, caller.rtp)
	if forwardedToCaller.SSRC == 200 || forwardedToCaller.PayloadType != 0 {
		t.Fatalf("callee RTP was not rewritten: %+v", forwardedToCaller.Header)
	}
	if err := callee.rtcp.SetReadDeadline(time.Now().Add(mediaTestTimeout)); err != nil {
		t.Fatalf("SetReadDeadline(RTCP) error = %v", err)
	}
	rtcpBuffer := make([]byte, 1600)
	rtcpBytes, _, err := callee.rtcp.ReadFromUDP(rtcpBuffer)
	if err != nil {
		t.Fatalf("read generated RTCP: %v", err)
	}
	generatedReports, err := rtcp.Unmarshal(rtcpBuffer[:rtcpBytes])
	if err != nil || len(generatedReports) == 0 {
		t.Fatalf("generated RTCP = %v, %v", generatedReports, err)
	}

	receiverReport := &rtcp.ReceiverReport{SSRC: 999, Reports: []rtcp.ReceptionReport{{
		SSRC: forwardedToCaller.SSRC, FractionLost: 64, TotalLost: 3, Jitter: 27,
	}}}
	wire, err := receiverReport.Marshal()
	if err != nil {
		t.Fatalf("RTCP Marshal() error = %v", err)
	}
	if _, err := caller.rtcp.WriteToUDPAddrPort(wire, callerRTCP); err != nil {
		t.Fatalf("write RTCP: %v", err)
	}
	waitFor(t, func() bool {
		snapshot := media.Snapshot()
		return snapshot.Caller.RemoteFractionLost == 64 && snapshot.Caller.RemoteTotalLost == 3 && snapshot.Caller.RemoteJitter == 27
	})

	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if pool.InUse() != 0 {
		t.Fatalf("pool InUse() = %d", pool.InUse())
	}
	snapshot := media.Snapshot()
	if snapshot.Caller.DTMFEvents != 1 || snapshot.Caller.Inbound.Packets != 2 || snapshot.Callee.Inbound.Packets != 1 {
		t.Fatalf("summary = %+v", snapshot)
	}
}

func TestSessionIdentifiesLossOneWayAndTimeout(t *testing.T) {
	media, _ := newConfiguredSession(t, 25100, 25107, 80*time.Millisecond)
	caller := newRemotePair(t)
	callee := newRemotePair(t)
	mapping := sipsdp.Mapping{Name: "PCMA", ClockRate: 8000, CallerPT: 8, CalleePT: 8}
	configureSession(t, media, caller, callee, mapping)
	ctx, cancel := context.WithCancel(context.Background())
	result := runSession(t, ctx, media)
	callerLocal, _, _ := media.LocalEndpoint(session.CallerLeg)
	for _, sequence := range []uint16{1, 3} {
		sendRTP(t, caller.rtp, callerLocal, rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, SSRC: 300, SequenceNumber: sequence, Timestamp: uint32(sequence) * 160}, Payload: make([]byte, 160)})
		_ = readRTP(t, callee.rtp)
	}
	waitFor(t, func() bool {
		snapshot := media.Snapshot()
		return snapshot.OneWay && snapshot.Caller.TimedOut && snapshot.Callee.TimedOut && snapshot.Caller.Inbound.Lost == 1
	})
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestFactoryRejectsPortExhaustion(t *testing.T) {
	pool, err := portalloc.New(portalloc.Config{BindIP: netip.MustParseAddr("127.0.0.1"), StartPort: 25200, EndPort: 25203})
	if err != nil {
		t.Fatalf("portalloc.New() error = %v", err)
	}
	factory := newFactory(t, pool, 100*time.Millisecond)
	first, err := factory.New(context.Background(), testMetadata())
	if err != nil {
		t.Fatalf("New(first) error = %v", err)
	}
	if _, err := factory.New(context.Background(), testMetadata()); !errors.Is(err, portalloc.ErrExhausted) {
		t.Fatalf("New(exhausted) error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func newConfiguredSession(t *testing.T, start, end uint16, inactivity time.Duration) (*session.Session, *portalloc.Pool) {
	t.Helper()
	pool, err := portalloc.New(portalloc.Config{BindIP: netip.MustParseAddr("127.0.0.1"), StartPort: start, EndPort: end})
	if err != nil {
		t.Fatalf("portalloc.New() error = %v", err)
	}
	factory := newFactory(t, pool, inactivity)
	media, err := factory.New(context.Background(), testMetadata())
	if err != nil {
		t.Fatalf("Factory.New() error = %v", err)
	}
	t.Cleanup(func() { _ = media.Close() })
	return media, pool
}

func newFactory(t *testing.T, pool *portalloc.Pool, inactivity time.Duration) *session.Factory {
	t.Helper()
	factory, err := session.NewFactory(session.Config{
		AdvertisedIP: netip.MustParseAddr("127.0.0.1"), ReadPollInterval: 10 * time.Millisecond,
		InactivityTimeout: inactivity, RTCPInterval: 20 * time.Millisecond,
		SummaryInterval: 20 * time.Millisecond, SummaryTimeout: 10 * time.Millisecond,
	}, pool, session.DiscardPublisher{})
	if err != nil {
		t.Fatalf("NewFactory() error = %v", err)
	}
	return factory
}

func testMetadata() session.Metadata {
	return session.NewMetadata("tenant-test", call.ID("call-test"), call.NodeID("node-test"), call.LegID("caller-leg"), call.LegID("callee-leg"))
}

func configureSession(t *testing.T, media *session.Session, caller, callee *remotePair, mapping sipsdp.Mapping) {
	t.Helper()
	callerEndpoint := sipsdp.Endpoint{RTP: localAddrPort(t, caller.rtp), RTCP: localAddrPort(t, caller.rtcp), Direction: sipsdp.DirectionSendRecv}
	calleeEndpoint := sipsdp.Endpoint{RTP: localAddrPort(t, callee.rtp), RTCP: localAddrPort(t, callee.rtcp), Direction: sipsdp.DirectionSendRecv}
	if err := media.Configure(callerEndpoint, calleeEndpoint, mapping); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
}

func newRemotePair(t *testing.T) *remotePair {
	t.Helper()
	rtpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenUDP(RTP) error = %v", err)
	}
	rtcpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		_ = rtpConn.Close()
		t.Fatalf("ListenUDP(RTCP) error = %v", err)
	}
	t.Cleanup(func() {
		_ = rtpConn.Close()
		_ = rtcpConn.Close()
	})
	return &remotePair{rtp: rtpConn, rtcp: rtcpConn}
}

func localAddrPort(t *testing.T, connection *net.UDPConn) netip.AddrPort {
	t.Helper()
	address, err := netip.ParseAddrPort(connection.LocalAddr().String())
	if err != nil {
		t.Fatalf("ParseAddrPort() error = %v", err)
	}
	return address
}

func runSession(t *testing.T, ctx context.Context, media *session.Session) <-chan error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- media.Run(ctx) }()
	return result
}

func sendRTP(t *testing.T, connection *net.UDPConn, destination netip.AddrPort, packet rtp.Packet) {
	t.Helper()
	wire, err := packet.Marshal()
	if err != nil {
		t.Fatalf("RTP Marshal() error = %v", err)
	}
	if _, err := connection.WriteToUDPAddrPort(wire, destination); err != nil {
		t.Fatalf("write RTP: %v", err)
	}
}

func readRTP(t *testing.T, connection *net.UDPConn) rtp.Packet {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(mediaTestTimeout)); err != nil {
		t.Fatalf("SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 1600)
	readBytes, _, err := connection.ReadFromUDP(buffer)
	if err != nil {
		t.Fatalf("read RTP: %v", err)
	}
	var packet rtp.Packet
	if err := packet.Unmarshal(buffer[:readBytes]); err != nil {
		t.Fatalf("RTP Unmarshal() error = %v", err)
	}
	return packet
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(mediaTestTimeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}
