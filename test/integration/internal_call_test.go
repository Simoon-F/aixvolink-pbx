package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type calleeBehavior string

const (
	calleeAnswers calleeBehavior = "answer"
	calleeRejects calleeBehavior = "reject"
	calleeRings   calleeBehavior = "ring"
)

type endpoint struct {
	username       string
	address        string
	client         *sipgo.Client
	serverDialogs  *sipgo.DialogServerCache
	clientDialogs  *sipgo.DialogClientCache
	behavior       calleeBehavior
	byeReceived    chan struct{}
	cancelReceived chan struct{}
	inviteCallID   chan string
	errors         chan error
	closeOnce      sync.Once
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
	endpoint := &endpoint{
		username: username, address: address, client: client, behavior: behavior,
		serverDialogs: sipgo.NewDialogServerCache(client, contact),
		clientDialogs: sipgo.NewDialogClientCache(client, contact),
		byeReceived:   make(chan struct{}), cancelReceived: make(chan struct{}),
		inviteCallID: make(chan string, 1), errors: make(chan error, 8),
	}
	server.OnInvite(endpoint.handleInvite)
	server.OnAck(func(request *sip.Request, transaction sip.ServerTransaction) {
		if err := endpoint.serverDialogs.ReadAck(request, transaction); err != nil {
			endpoint.report(err)
		}
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
	return endpoint
}

func (e *endpoint) handleInvite(request *sip.Request, transaction sip.ServerTransaction) {
	if request.CallID() != nil {
		e.inviteCallID <- request.CallID().Value()
	}
	if e.behavior == calleeRejects {
		if err := transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBusyHere, "Busy Here", nil)); err != nil {
			e.report(err)
		}
		return
	}
	dialog, err := e.serverDialogs.ReadInvite(request, transaction)
	if err != nil {
		e.report(err)
		return
	}
	if err := dialog.Respond(sip.StatusTrying, "Trying", nil); err != nil {
		e.report(err)
		return
	}
	if err := dialog.Respond(sip.StatusRinging, "Ringing", nil); err != nil {
		e.report(err)
		return
	}
	if e.behavior == calleeRings {
		<-dialog.Context().Done()
		e.closeOnce.Do(func() { close(e.cancelReceived) })
		return
	}
	if err := dialog.Respond(sip.StatusOK, "OK", nil); err != nil {
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
	dialog, err := e.clientDialogs.WriteInvite(ctx, request)
	if err != nil {
		t.Fatalf("WriteInvite() error = %v", err)
	}
	return dialog, request.CallID().Value()
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
