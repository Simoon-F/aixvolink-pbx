package integration_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
	sipsdp "github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
)

type calleeBehavior string

const (
	calleeAnswers    calleeBehavior = "answer"
	calleeRejects    calleeBehavior = "reject"
	calleeRings      calleeBehavior = "ring"
	calleeEarlyMedia calleeBehavior = "early-media"
)

type endpoint struct {
	username         string
	address          string
	client           *sipgo.Client
	serverDialogs    *sipgo.DialogServerCache
	clientDialogs    *sipgo.DialogClientCache
	behavior         calleeBehavior
	byeReceived      chan struct{}
	cancelReceived   chan struct{}
	inviteCallID     chan string
	errors           chan error
	closeOnce        sync.Once
	mediaRTP         *net.UDPConn
	mediaRTCP        *net.UDPConn
	mediaTarget      chan sipsdp.Endpoint
	answerNow        chan struct{}
	dialogMu         sync.RWMutex
	activeDialog     *sipgo.DialogServerSession
	reinviteReceived chan sipsdp.Direction
	referReceived    chan struct{}
	ackReceived      chan uint32
	pendingMu        sync.Mutex
	pendingReInvite  sip.ServerTransaction
	notifyReceived   chan struct{}
	codecs           []sipsdp.Codec
}

func TestInternalCallAnswersAndPropagatesCallerBye(t *testing.T) {
	running := startTestApp(t, callCredentials())
	caller := newEndpoint(t, "1001", availableAddress(t), calleeAnswers)
	callee := newEndpoint(t, "1002", availableAddress(t), calleeAnswers)
	register(t, caller.client, running.address, "udp", "1001", "password-1001", caller.contactURI(), 120)
	register(t, callee.client, running.address, "udp", "1002", "password-1002", callee.contactURI(), 120)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	dialog, inboundCallID := caller.invite(t, ctx, running.address, "1002")
	waitCallState(t, ctx, running.recorder, call.StateRouting)
	progress := waitCallProgress(t, ctx, running.recorder)
	if progress.NewState == call.StateFailed {
		t.Fatalf("call failed before answer: %+v", progress)
	}
	if err := dialog.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("WaitAnswer() error = %v", err)
	}
	if err := dialog.Ack(ctx); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	select {
	case calleeCallID := <-callee.inviteCallID:
		if calleeCallID == inboundCallID {
			t.Fatal("B2BUA reused the caller SIP Call-ID on the callee leg")
		}
	case <-ctx.Done():
		t.Fatal("callee did not receive INVITE")
	}
	if err := dialog.Bye(ctx); err != nil {
		t.Fatalf("Bye() error = %v", err)
	}
	select {
	case <-callee.byeReceived:
	case <-ctx.Done():
		t.Fatal("callee did not receive propagated BYE")
	}
	terminal := waitCallState(t, ctx, running.recorder, call.StateTerminated)
	if terminal.CallID == "" || terminal.NodeID != "node-test-1" || terminal.TenantID != "tenant-1" {
		t.Fatalf("terminal event is not correlated: %+v", terminal)
	}
	caller.assertNoError(t)
	callee.assertNoError(t)
}

func TestInternalCallRelaysRejection(t *testing.T) {
	running := startTestApp(t, callCredentials())
	caller := newEndpoint(t, "1001", availableAddress(t), calleeAnswers)
	callee := newEndpoint(t, "1002", availableAddress(t), calleeRejects)
	register(t, caller.client, running.address, "udp", "1001", "password-1001", caller.contactURI(), 120)
	register(t, callee.client, running.address, "udp", "1002", "password-1002", callee.contactURI(), 120)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	dialog, _ := caller.invite(t, ctx, running.address, "1002")
	waitCallState(t, ctx, running.recorder, call.StateRouting)
	err := dialog.WaitAnswer(ctx, sipgo.AnswerOptions{})
	var responseErr *sipgo.ErrDialogResponse
	if !errors.As(err, &responseErr) || responseErr.Res.StatusCode != sip.StatusBusyHere {
		t.Fatalf("WaitAnswer() error = %v", err)
	}
	waitCallState(t, ctx, running.recorder, call.StateFailed)
	caller.assertNoError(t)
	callee.assertNoError(t)
}

func TestInternalCallPropagatesCancelBeforeAnswer(t *testing.T) {
	running := startTestApp(t, callCredentials())
	caller := newEndpoint(t, "1001", availableAddress(t), calleeAnswers)
	callee := newEndpoint(t, "1002", availableAddress(t), calleeRings)
	register(t, caller.client, running.address, "udp", "1001", "password-1001", caller.contactURI(), 120)
	register(t, callee.client, running.address, "udp", "1002", "password-1002", callee.contactURI(), 120)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	dialog, _ := caller.invite(t, ctx, running.address, "1002")
	waitCallState(t, ctx, running.recorder, call.StateRouting)
	cancelInvite, cancelWait := context.WithCancel(ctx)
	responseSeen := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- dialog.WaitAnswer(cancelInvite, sipgo.AnswerOptions{OnResponse: func(response *sip.Response) error {
			if response.StatusCode == sip.StatusRinging {
				responseSeen <- struct{}{}
			}
			return nil
		}})
	}()
	select {
	case <-responseSeen:
		cancelWait()
	case <-ctx.Done():
		t.Fatal("caller did not receive ringing response")
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitAnswer(cancel) error = %v", err)
	}
	select {
	case <-callee.cancelReceived:
	case <-ctx.Done():
		t.Fatal("callee did not receive propagated CANCEL")
	}
	waitCallState(t, ctx, running.recorder, call.StateFailed)
	caller.assertNoError(t)
	callee.assertNoError(t)
}

func TestInternalCallAnchorsBidirectionalMediaAndDTMF(t *testing.T) {
	running := startTestApp(t, callCredentials())
	caller := newEndpoint(t, "1001", availableAddress(t), calleeAnswers)
	callee := newEndpoint(t, "1002", availableAddress(t), calleeAnswers)
	register(t, caller.client, running.address, "udp", "1001", "password-1001", caller.contactURI(), 120)
	register(t, callee.client, running.address, "udp", "1002", "password-1002", callee.contactURI(), 120)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	dialog, _ := caller.invite(t, ctx, running.address, "1002")
	if err := dialog.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("WaitAnswer() error = %v", err)
	}
	if err := dialog.Ack(ctx); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	callerMedia, err := sipsdp.ParseAudio(dialog.InviteResponse.Body())
	if err != nil {
		t.Fatalf("ParseAudio(caller answer) error = %v", err)
	}
	var calleeMedia sipsdp.Endpoint
	select {
	case calleeMedia = <-callee.mediaTarget:
	case <-ctx.Done():
		t.Fatal("callee did not receive anchored SDP offer")
	}

	sendEndpointRTP(t, caller.mediaRTP, callerMedia.RTP, 0, 1, 1000, []byte{1, 2, 3})
	fromCaller := readEndpointRTP(t, callee.mediaRTP)
	if fromCaller.PayloadType != 0 || fromCaller.SSRC == 1000 {
		t.Fatalf("caller media not anchored: %+v", fromCaller.Header)
	}
	sendEndpointRTP(t, callee.mediaRTP, calleeMedia.RTP, 0, 10, 2000, []byte{4, 5, 6})
	fromCallee := readEndpointRTP(t, caller.mediaRTP)
	if fromCallee.PayloadType != 0 || fromCallee.SSRC == 2000 {
		t.Fatalf("callee media not anchored: %+v", fromCallee.Header)
	}
	sendEndpointRTP(t, caller.mediaRTP, callerMedia.RTP, 101, 2, 1000, []byte{5, 0x8a, 0x03, 0x20})
	dtmfPacket := readEndpointRTP(t, callee.mediaRTP)
	if dtmfPacket.PayloadType != 101 || len(dtmfPacket.Payload) != 4 {
		t.Fatalf("DTMF was not forwarded: %+v", dtmfPacket)
	}
	if err := dialog.Bye(ctx); err != nil {
		t.Fatalf("Bye() error = %v", err)
	}
}

func TestInternalCallForwardsEarlyMediaBeforeAnswer(t *testing.T) {
	running := startTestApp(t, callCredentials())
	caller := newEndpoint(t, "1001", availableAddress(t), calleeAnswers)
	callee := newEndpoint(t, "1002", availableAddress(t), calleeEarlyMedia)
	register(t, caller.client, running.address, "udp", "1001", "password-1001", caller.contactURI(), 120)
	register(t, callee.client, running.address, "udp", "1002", "password-1002", callee.contactURI(), 120)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	dialog, _ := caller.invite(t, ctx, running.address, "1002")
	earlySeen := make(chan sipsdp.Endpoint, 1)
	answerResult := make(chan error, 1)
	go func() {
		answerResult <- dialog.WaitAnswer(ctx, sipgo.AnswerOptions{OnResponse: func(response *sip.Response) error {
			if response.StatusCode != 183 {
				return nil
			}
			endpoint, err := sipsdp.ParseAudio(response.Body())
			if err != nil {
				return err
			}
			earlySeen <- endpoint
			return nil
		}})
	}()
	var earlyEndpoint sipsdp.Endpoint
	select {
	case earlyEndpoint = <-earlySeen:
	case <-ctx.Done():
		t.Fatal("caller did not receive 183 SDP")
	}
	waitCallState(t, ctx, running.recorder, call.StateEarlyMedia)
	sendEndpointRTP(t, caller.mediaRTP, earlyEndpoint.RTP, 0, 1, 3000, []byte{7, 8, 9})
	_ = readEndpointRTP(t, callee.mediaRTP)
	close(callee.answerNow)
	if err := <-answerResult; err != nil {
		t.Fatalf("WaitAnswer() error = %v", err)
	}
	if err := dialog.Ack(ctx); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	if err := dialog.Bye(ctx); err != nil {
		t.Fatalf("Bye() error = %v", err)
	}
}

func TestInternalCallRelaysHoldResumeAndBlindTransfer(t *testing.T) {
	running := startTestApp(t, callCredentials())
	caller := newEndpoint(t, "1001", availableAddress(t), calleeAnswers)
	callee := newEndpoint(t, "1002", availableAddress(t), calleeAnswers)
	register(t, caller.client, running.address, "udp", "1001", "password-1001", caller.contactURI(), 120)
	register(t, callee.client, running.address, "udp", "1002", "password-1002", callee.contactURI(), 120)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	dialog, _ := caller.invite(t, ctx, running.address, "1002")
	if err := dialog.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("WaitAnswer() error = %v", err)
	}
	if err := dialog.Ack(ctx); err != nil {
		t.Fatalf("Ack() error = %v", err)
	}
	waitCallState(t, ctx, running.recorder, call.StateAnswered)
	select {
	case <-callee.ackReceived:
	case <-ctx.Done():
		t.Fatal("callee did not receive initial ACK")
	}

	caller.reinvite(t, ctx, dialog, sipsdp.DirectionSendOnly)
	select {
	case direction := <-callee.reinviteReceived:
		if direction != sipsdp.DirectionSendOnly {
			t.Fatalf("callee hold direction = %s", direction)
		}
	case <-ctx.Done():
		t.Fatal("callee did not receive hold re-INVITE")
	}
	waitCallState(t, ctx, running.recorder, call.StateHeld)
	select {
	case <-callee.ackReceived:
	case <-ctx.Done():
		t.Fatal("callee did not receive hold ACK")
	}
	caller.reinvite(t, ctx, dialog, sipsdp.DirectionSendRecv)
	select {
	case direction := <-callee.reinviteReceived:
		if direction != sipsdp.DirectionSendRecv {
			select {
			case endpointErr := <-callee.errors:
				t.Fatalf("callee resume direction = %s; endpoint error = %v", direction, endpointErr)
			default:
				t.Fatalf("callee resume direction = %s", direction)
			}
		}
	case <-ctx.Done():
		t.Fatal("callee did not receive resume re-INVITE")
	}
	waitCallState(t, ctx, running.recorder, call.StateAnswered)

	refer := sip.NewRequest(sip.REFER, dialog.InviteRequest.Recipient)
	refer.AppendHeader(sip.NewHeader("Refer-To", "<sip:1003@pbx.example.invalid>"))
	response, err := dialog.Do(ctx, refer)
	if err != nil || response.StatusCode != sip.StatusAccepted {
		t.Fatalf("REFER response = %v, %v", response, err)
	}
	select {
	case <-callee.referReceived:
	case <-ctx.Done():
		t.Fatal("callee did not receive blind transfer REFER")
	}
	callee.sendNotify(t, ctx)
	select {
	case <-caller.notifyReceived:
	case <-ctx.Done():
		t.Fatal("caller did not receive transfer NOTIFY")
	}
	if err := dialog.Bye(ctx); err != nil {
		t.Fatalf("Bye() error = %v", err)
	}
}

func TestInternalCallRejectsNoCommonG711Codec(t *testing.T) {
	running := startTestApp(t, callCredentials())
	caller := newEndpoint(t, "1001", availableAddress(t), calleeAnswers)
	callee := newEndpoint(t, "1002", availableAddress(t), calleeAnswers)
	caller.codecs = []sipsdp.Codec{{Name: "PCMU", PayloadType: 0, ClockRate: 8000}}
	callee.codecs = []sipsdp.Codec{{Name: "PCMA", PayloadType: 8, ClockRate: 8000}}
	register(t, caller.client, running.address, "udp", "1001", "password-1001", caller.contactURI(), 120)
	register(t, callee.client, running.address, "udp", "1002", "password-1002", callee.contactURI(), 120)

	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	dialog, _ := caller.invite(t, ctx, running.address, "1002")
	err := dialog.WaitAnswer(ctx, sipgo.AnswerOptions{})
	var responseErr *sipgo.ErrDialogResponse
	if !errors.As(err, &responseErr) || responseErr.Res.StatusCode != sip.StatusNotAcceptableHere {
		t.Fatalf("WaitAnswer() error = %v", err)
	}
	waitCallState(t, ctx, running.recorder, call.StateFailed)
}

func callCredentials() []sipauth.Credential {
	return []sipauth.Credential{
		{TenantID: "tenant-1", Username: "1001", Realm: "pbx.example.invalid", HA1: sipauth.ComputeHA1("1001", "pbx.example.invalid", "password-1001"), MaxBindings: 2},
		{TenantID: "tenant-1", Username: "1002", Realm: "pbx.example.invalid", HA1: sipauth.ComputeHA1("1002", "pbx.example.invalid", "password-1002"), MaxBindings: 2},
	}
}

func newEndpoint(t *testing.T, username, address string, behavior calleeBehavior) *endpoint {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("phase1-"+username), sipgo.WithUserAgentHostname("127.0.0.1"))
	if err != nil {
		t.Fatalf("NewUA() error = %v", err)
	}
	t.Cleanup(func() {
		if err := ua.Close(); err != nil {
			t.Errorf("endpoint UA Close() error = %v", err)
		}
	})
	server, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	client, err := sipgo.NewClient(ua, sipgo.WithClientConnectionAddr(address), sipgo.WithClientNAT())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	host, port, err := sip.ParseAddr(address)
	if err != nil {
		t.Fatalf("ParseAddr() error = %v", err)
	}
	contact := sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: username, Host: host, Port: port}}
	mediaRTP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenUDP(media RTP) error = %v", err)
	}
	mediaRTCP, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		_ = mediaRTP.Close()
		t.Fatalf("ListenUDP(media RTCP) error = %v", err)
	}
	t.Cleanup(func() {
		_ = mediaRTP.Close()
		_ = mediaRTCP.Close()
	})
	endpoint := &endpoint{
		username: username, address: address, client: client, behavior: behavior,
		serverDialogs: sipgo.NewDialogServerCache(client, contact),
		clientDialogs: sipgo.NewDialogClientCache(client, contact),
		byeReceived:   make(chan struct{}), cancelReceived: make(chan struct{}),
		inviteCallID: make(chan string, 1), errors: make(chan error, 8),
		mediaRTP: mediaRTP, mediaRTCP: mediaRTCP,
		mediaTarget: make(chan sipsdp.Endpoint, 1), answerNow: make(chan struct{}),
		reinviteReceived: make(chan sipsdp.Direction, 4), referReceived: make(chan struct{}, 1),
		ackReceived:    make(chan uint32, 8),
		notifyReceived: make(chan struct{}, 1),
		codecs: []sipsdp.Codec{
			{Name: "PCMU", PayloadType: 0, ClockRate: 8000},
			{Name: "PCMA", PayloadType: 8, ClockRate: 8000},
			{Name: "TELEPHONE-EVENT", PayloadType: 101, ClockRate: 8000},
		},
	}
	server.OnInvite(endpoint.handleInvite)
	server.OnAck(func(request *sip.Request, transaction sip.ServerTransaction) {
		if err := endpoint.serverDialogs.ReadAck(request, transaction); err != nil {
			endpoint.report(err)
			return
		}
		endpoint.pendingMu.Lock()
		if endpoint.pendingReInvite != nil {
			endpoint.pendingReInvite.Terminate()
			endpoint.pendingReInvite = nil
		}
		endpoint.pendingMu.Unlock()
		endpoint.ackReceived <- request.CSeq().SeqNo
	})
	server.OnBye(func(request *sip.Request, transaction sip.ServerTransaction) {
		if err := endpoint.serverDialogs.ReadBye(request, transaction); err == nil {
			endpoint.closeOnce.Do(func() { close(endpoint.byeReceived) })
			return
		}
		if err := endpoint.clientDialogs.ReadBye(request, transaction); err != nil {
			endpoint.report(err)
		}
	})
	server.OnRefer(endpoint.handleRefer)
	server.OnNotify(func(request *sip.Request, transaction sip.ServerTransaction) {
		endpoint.notifyReceived <- struct{}{}
		if err := transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusOK, "OK", nil)); err != nil {
			endpoint.report(err)
		}
	})
	return endpoint
}

func (e *endpoint) handleInvite(request *sip.Request, transaction sip.ServerTransaction) {
	if request.To() != nil && request.To().Params.Has("tag") {
		e.handleReInvite(request, transaction)
		return
	}
	if request.CallID() != nil {
		e.inviteCallID <- request.CallID().Value()
	}
	if e.behavior == calleeRejects {
		if err := transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBusyHere, "Busy Here", nil)); err != nil {
			e.report(err)
		}
		return
	}
	if len(request.Body()) > 0 {
		mediaTarget, err := sipsdp.ParseAudio(request.Body())
		if err != nil {
			e.report(err)
			return
		}
		e.mediaTarget <- mediaTarget
	}
	dialog, err := e.serverDialogs.ReadInvite(request, transaction)
	if err != nil {
		e.report(err)
		return
	}
	e.dialogMu.Lock()
	e.activeDialog = dialog
	e.dialogMu.Unlock()
	if err := dialog.Respond(sip.StatusTrying, "Trying", nil); err != nil {
		e.report(err)
		return
	}
	if e.behavior == calleeEarlyMedia {
		answer, err := e.audioSDP()
		if err != nil {
			e.report(err)
			return
		}
		if err := dialog.Respond(183, "Session Progress", answer, sip.NewHeader("Content-Type", "application/sdp")); err != nil {
			e.report(err)
			return
		}
		<-e.answerNow
	} else {
		if err := dialog.Respond(sip.StatusRinging, "Ringing", nil); err != nil {
			e.report(err)
			return
		}
	}
	if e.behavior == calleeRings {
		<-dialog.Context().Done()
		e.closeOnce.Do(func() { close(e.cancelReceived) })
		return
	}
	answer, err := e.audioSDP()
	if err != nil {
		e.report(err)
		return
	}
	if err := dialog.Respond(sip.StatusOK, "OK", answer, sip.NewHeader("Content-Type", "application/sdp")); err != nil {
		e.report(err)
	}
}

func (e *endpoint) handleReInvite(request *sip.Request, transaction sip.ServerTransaction) {
	e.dialogMu.RLock()
	dialog := e.activeDialog
	e.dialogMu.RUnlock()
	if dialog == nil {
		e.report(errors.New("re-INVITE arrived before dialog"))
		return
	}
	if err := dialog.ReadRequest(request, transaction); err != nil {
		e.report(err)
		return
	}
	offer, err := sipsdp.ParseAudio(request.Body())
	if err != nil {
		e.report(err)
		return
	}
	e.reinviteReceived <- offer.Direction
	answer, err := e.audioSDPDirection(offer.Direction)
	if err != nil {
		e.report(err)
		return
	}
	response := sip.NewResponseFromRequest(request, sip.StatusOK, "OK", answer)
	response.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	e.pendingMu.Lock()
	e.pendingReInvite = transaction
	e.pendingMu.Unlock()
	if err := transaction.Respond(response); err != nil {
		e.report(err)
	}
}

func (e *endpoint) handleRefer(request *sip.Request, transaction sip.ServerTransaction) {
	e.dialogMu.RLock()
	dialog := e.activeDialog
	e.dialogMu.RUnlock()
	if dialog == nil {
		e.report(errors.New("REFER arrived before dialog"))
		return
	}
	if err := dialog.ReadRequest(request, transaction); err != nil {
		e.report(err)
		return
	}
	if request.GetHeader("Refer-To") == nil {
		e.report(errors.New("REFER missing Refer-To"))
		return
	}
	e.referReceived <- struct{}{}
	if err := transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusAccepted, "Accepted", nil)); err != nil {
		e.report(err)
	}
}

func (e *endpoint) invite(t *testing.T, ctx context.Context, serverAddress, callee string) (*sipgo.DialogClientSession, string) {
	t.Helper()
	host, port, err := sip.ParseAddr(serverAddress)
	if err != nil {
		t.Fatalf("ParseAddr(server) error = %v", err)
	}
	recipient := sip.Uri{Scheme: "sip", User: callee, Host: host, Port: port}
	request := sip.NewRequest(sip.INVITE, recipient)
	request.SetTransport("udp")
	from := sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: e.username, Host: "pbx.example.invalid"}, Params: sip.NewParams()}
	from.Params.Add("tag", sip.GenerateTagN(12))
	request.AppendHeader(&from)
	offer, err := e.audioSDP()
	if err != nil {
		t.Fatalf("audioSDP() error = %v", err)
	}
	request.SetBody(offer)
	request.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	dialog, err := e.clientDialogs.WriteInvite(ctx, request)
	if err != nil {
		t.Fatalf("WriteInvite() error = %v", err)
	}
	return dialog, request.CallID().Value()
}

func (e *endpoint) audioSDP() ([]byte, error) {
	return e.audioSDPDirection(sipsdp.DirectionSendRecv)
}

func (e *endpoint) audioSDPDirection(direction sipsdp.Direction) ([]byte, error) {
	rtpAddress, err := netip.ParseAddrPort(e.mediaRTP.LocalAddr().String())
	if err != nil {
		return nil, err
	}
	rtcpAddress, err := netip.ParseAddrPort(e.mediaRTCP.LocalAddr().String())
	if err != nil {
		return nil, err
	}
	return sipsdp.BuildAudio(rtpAddress, rtcpAddress, e.codecs, direction)
}

func (e *endpoint) reinvite(t *testing.T, ctx context.Context, dialog *sipgo.DialogClientSession, direction sipsdp.Direction) {
	t.Helper()
	body, err := e.audioSDPDirection(direction)
	if err != nil {
		t.Fatalf("audioSDPDirection() error = %v", err)
	}
	var response *sip.Response
	for attempt := 0; attempt < 5; attempt++ {
		request := sip.NewRequest(sip.INVITE, dialog.InviteRequest.Recipient)
		request.SetBody(body)
		request.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		response, err = dialog.Do(ctx, request)
		if err != nil || response == nil || response.StatusCode != sip.StatusRequestPending {
			break
		}
		retry := time.NewTimer(25 * time.Millisecond)
		select {
		case <-retry.C:
		case <-ctx.Done():
			if !retry.Stop() {
				<-retry.C
			}
			t.Fatalf("re-INVITE retry: %v", ctx.Err())
		}
	}
	if err != nil || response == nil || response.StatusCode != sip.StatusOK {
		t.Fatalf("re-INVITE response = %v, %v", response, err)
	}
	ack := sip.NewRequest(sip.ACK, dialog.InviteRequest.Recipient)
	if err := dialog.WriteRequest(ack); err != nil {
		t.Fatalf("re-INVITE ACK error = %v", err)
	}
}

func (e *endpoint) sendNotify(t *testing.T, ctx context.Context) {
	t.Helper()
	e.dialogMu.RLock()
	dialog := e.activeDialog
	e.dialogMu.RUnlock()
	if dialog == nil {
		t.Fatal("callee dialog is unavailable for NOTIFY")
	}
	recipient := dialog.InviteRequest.From().Address
	if contact := dialog.InviteRequest.Contact(); contact != nil {
		recipient = contact.Address
	}
	request := sip.NewRequest(sip.NOTIFY, recipient)
	request.AppendHeader(sip.NewHeader("Event", "refer"))
	request.AppendHeader(sip.NewHeader("Subscription-State", "terminated;reason=noresource"))
	request.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag"))
	request.SetBody([]byte("SIP/2.0 200 OK\r\n"))
	response, err := dialog.Do(ctx, request)
	if err != nil || response.StatusCode != sip.StatusOK {
		t.Fatalf("NOTIFY response = %v, %v", response, err)
	}
}

func (e *endpoint) contactURI() string { return "sip:" + e.username + "@" + e.address }

func (e *endpoint) report(err error) {
	select {
	case e.errors <- err:
	default:
	}
}

func (e *endpoint) assertNoError(t *testing.T) {
	t.Helper()
	select {
	case err := <-e.errors:
		t.Errorf("endpoint %s error: %v", e.username, err)
	default:
	}
}

func waitCallState(t *testing.T, ctx context.Context, recorder *callRecorder, state call.State) call.Event {
	t.Helper()
	for {
		select {
		case event := <-recorder.notify:
			if event.NewState == state {
				return event
			}
		case <-ctx.Done():
			t.Fatalf("call did not reach state %s", state)
		}
	}
}

func waitCallProgress(t *testing.T, ctx context.Context, recorder *callRecorder) call.Event {
	t.Helper()
	for {
		select {
		case event := <-recorder.notify:
			if event.NewState == call.StateRinging || event.NewState == call.StateAnswered || event.NewState == call.StateFailed {
				return event
			}
		case <-ctx.Done():
			t.Fatal("call did not make progress")
		}
	}
}

func sendEndpointRTP(t *testing.T, connection *net.UDPConn, destination netip.AddrPort, payloadType uint8, sequence uint16, ssrc uint32, payload []byte) {
	t.Helper()
	packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: payloadType, SequenceNumber: sequence, Timestamp: uint32(sequence) * 160, SSRC: ssrc}, Payload: payload}
	wire, err := packet.Marshal()
	if err != nil {
		t.Fatalf("RTP Marshal() error = %v", err)
	}
	if _, err := connection.WriteToUDPAddrPort(wire, destination); err != nil {
		t.Fatalf("write RTP: %v", err)
	}
}

func readEndpointRTP(t *testing.T, connection *net.UDPConn) rtp.Packet {
	t.Helper()
	if err := connection.SetReadDeadline(time.Now().Add(integrationTimeout)); err != nil {
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
