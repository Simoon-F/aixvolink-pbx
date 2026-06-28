// Package b2bua coordinates independent SIP dialog legs for internal calls.
package b2bua

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	mediasession "github.com/Simoon-F/aixvolink-pbx/internal/media/session"
	sipsdp "github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Config bounds internal-call orchestration.
type Config struct {
	TenantID        registration.TenantID
	Realm           string
	NodeID          call.NodeID
	MaxActiveCalls  int
	MailboxSize     int
	InviteTimeout   time.Duration
	DispatchTimeout time.Duration
}

// Engine owns active SIP dialog correlation and admission control.
type Engine struct {
	cfg           Config
	registrations *registration.Manager
	publisher     call.Publisher
	clock         call.Clock
	log           *slog.Logger
	dialogServer  *sipgo.DialogServerCache
	dialogClient  *sipgo.DialogClientCache
	mediaFactory  *mediasession.Factory

	mu         sync.RWMutex
	stopping   bool
	reserved   int
	byInbound  map[string]*session
	byOutbound map[string]*session
	wg         sync.WaitGroup
}

type session struct {
	engine          *Engine
	call            call.Call
	actor           *call.Actor
	inbound         *sipgo.DialogServerSession
	outbound        *sipgo.DialogClientSession
	media           *mediasession.Session
	callerOffer     sipsdp.Endpoint
	mediaMapping    sipsdp.Mapping
	callerAnswer    []byte
	mediaCancel     context.CancelFunc
	mediaResult     chan error
	pendingMu       sync.Mutex
	reInviteBusy    bool
	pendingReInvite sip.ServerTransaction
	pendingCSeq     uint32
	commands        chan sessionCommand
	done            chan struct{}
}

type hangupOrigin string

type commandKind string

const (
	commandHangup   commandKind = "hangup"
	commandReInvite commandKind = "reinvite"
	commandRefer    commandKind = "refer"
	commandNotify   commandKind = "notify"
)

type sessionCommand struct {
	kind        commandKind
	origin      hangupOrigin
	request     *sip.Request
	transaction sip.ServerTransaction
	result      chan struct{}
}

const (
	hangupCaller          hangupOrigin = "caller"
	hangupCallee          hangupOrigin = "callee"
	statusSessionProgress              = 183
)

var errMediaNegotiation = errors.New("media negotiation failed")

// NewEngine constructs a stopped B2BUA engine.
func NewEngine(
	cfg Config,
	registrations *registration.Manager,
	publisher call.Publisher,
	clock call.Clock,
	client *sipgo.Client,
	contact sip.ContactHeader,
	mediaFactory *mediasession.Factory,
	logger *slog.Logger,
) (*Engine, error) {
	if cfg.TenantID == "" || cfg.Realm == "" || cfg.NodeID == "" {
		return nil, fmt.Errorf("tenant, realm, and node ID are required")
	}
	if cfg.MaxActiveCalls <= 0 || cfg.MailboxSize <= 0 || cfg.MailboxSize > call.MaxMailboxCapacity {
		return nil, fmt.Errorf("active call and mailbox limits are invalid")
	}
	if cfg.InviteTimeout <= 0 || cfg.DispatchTimeout <= 0 {
		return nil, fmt.Errorf("invite and dispatch timeouts must be positive")
	}
	if registrations == nil || publisher == nil || clock == nil || client == nil || mediaFactory == nil || logger == nil {
		return nil, fmt.Errorf("B2BUA dependencies are required")
	}
	return &Engine{
		cfg: cfg, registrations: registrations, publisher: publisher, clock: clock, log: logger,
		dialogServer: sipgo.NewDialogServerCache(client, contact),
		dialogClient: sipgo.NewDialogClientCache(client, contact),
		mediaFactory: mediaFactory,
		byInbound:    make(map[string]*session, cfg.MaxActiveCalls),
		byOutbound:   make(map[string]*session, cfg.MaxActiveCalls),
	}, nil
}

// RegisterHandlers connects the engine to a sipgo server for the supplied process context.
func (e *Engine) RegisterHandlers(ctx context.Context, server *sipgo.Server) {
	server.OnInvite(func(request *sip.Request, transaction sip.ServerTransaction) {
		if request.To() != nil && request.To().Params.Has("tag") {
			e.handleReInvite(request, transaction)
			return
		}
		e.handleInvite(ctx, request, transaction)
	})
	server.OnAck(func(request *sip.Request, transaction sip.ServerTransaction) {
		if err := e.dialogServer.ReadAck(request, transaction); err != nil {
			e.log.Debug("ignoring unmatched ACK", "error", err)
			return
		}
		if activeSession := e.findInboundSession(request); activeSession != nil {
			activeSession.completePendingReInviteCSeq(request.CSeq().SeqNo)
		}
	})
	server.OnBye(e.handleBye)
	server.OnRefer(e.handleRefer)
	server.OnNotify(e.handleNotify)
}

func (e *Engine) handleInvite(ctx context.Context, request *sip.Request, transaction sip.ServerTransaction) {
	if !e.reserve() {
		response := sip.NewResponseFromRequest(request, sip.StatusServiceUnavailable, "Service Unavailable", nil)
		response.AppendHeader(sip.NewHeader("Retry-After", "1"))
		_ = transaction.Respond(response)
		return
	}

	from := request.From()
	to := request.To()
	if from == nil || to == nil || from.Address.User == "" || to.Address.User == "" {
		e.release()
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Request", nil))
		return
	}
	callerAoR := registration.AoR("sip:" + from.Address.User + "@" + e.cfg.Realm)
	registered, err := e.sourceIsRegistered(ctx, callerAoR, request.Source(), request.Transport())
	if err != nil || !registered {
		e.release()
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusForbidden, "Forbidden", nil))
		return
	}

	inbound, err := e.dialogServer.ReadInvite(request, transaction)
	if err != nil {
		e.release()
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Dialog", nil))
		return
	}
	current, err := call.New(call.TenantID(e.cfg.TenantID), e.cfg.NodeID, e.clock.Now())
	if err != nil {
		e.release()
		_ = inbound.Respond(sip.StatusInternalServerError, "Server Internal Error", nil)
		return
	}
	actor, err := call.NewActor(call.ActorConfig{MailboxCapacity: e.cfg.MailboxSize}, current, e.publisher, e.clock)
	if err != nil {
		e.release()
		_ = inbound.Respond(sip.StatusInternalServerError, "Server Internal Error", nil)
		return
	}
	activeSession := &session{
		engine: e, call: current, actor: actor, inbound: inbound,
		commands: make(chan sessionCommand, e.cfg.MailboxSize), done: make(chan struct{}),
	}
	e.mu.Lock()
	e.byInbound[inbound.ID] = activeSession
	e.mu.Unlock()
	activeSession.run(ctx, request.Clone())
}

func (s *session) run(parent context.Context, inboundRequest *sip.Request) {
	defer s.engine.wg.Done()
	defer close(s.done)
	defer s.engine.remove(s)
	defer func() { _ = s.inbound.Close() }()
	defer func() { s.stopMedia(context.WithoutCancel(parent)) }()
	defer s.completePendingReInvite()

	actorCtx, cancelActor := context.WithCancel(parent)
	defer cancelActor()
	actorResult := make(chan error, 1)
	go func() { actorResult <- s.actor.Run(actorCtx) }()
	defer func() {
		cancelActor()
		<-actorResult
	}()

	if err := s.transition(parent, call.StateRouting, s.call.CallerLeg, "INVITE", ""); err != nil {
		s.engine.log.Error("call actor routing failed", "call_id", s.call.ID, "node_id", s.call.NodeID, "error", err)
		return
	}
	if err := s.inbound.Respond(sip.StatusTrying, "Trying", nil); err != nil {
		s.fail(parent, "caller transaction failed", "INVITE")
		return
	}

	calleeAoR := registration.AoR("sip:" + inboundRequest.To().Address.User + "@" + s.engine.cfg.Realm)
	binding, err := s.engine.registrations.Resolve(parent, s.engine.cfg.TenantID, calleeAoR)
	if err != nil {
		_ = s.inbound.Respond(sip.StatusNotFound, "Not Found", nil)
		s.fail(parent, "callee not registered", "404")
		return
	}
	callerOffer, err := sipsdp.ParseAudio(inboundRequest.Body())
	if err != nil {
		_ = s.inbound.Respond(sip.StatusNotAcceptableHere, "Not Acceptable Here", nil)
		s.fail(parent, "invalid or unsupported caller SDP", "488")
		return
	}
	s.callerOffer = callerOffer
	mediaCtx, cancelMediaAllocation := context.WithTimeout(parent, s.engine.cfg.DispatchTimeout)
	s.media, err = s.engine.mediaFactory.New(mediaCtx, mediasession.NewMetadata(
		string(s.call.TenantID), s.call.ID, s.call.NodeID, s.call.CallerLeg, s.call.CalleeLeg,
	))
	cancelMediaAllocation()
	if err != nil {
		_ = s.inbound.Respond(sip.StatusServiceUnavailable, "Media Resources Unavailable", nil)
		s.fail(parent, "media resources unavailable", "503")
		return
	}
	calleeRTP, calleeRTCP, err := s.media.LocalEndpoint(mediasession.CalleeLeg)
	if err != nil {
		_ = s.inbound.Respond(sip.StatusInternalServerError, "Server Internal Error", nil)
		s.fail(parent, "media endpoint unavailable", "500")
		return
	}
	outboundOffer, err := sipsdp.BuildAudio(calleeRTP, calleeRTCP, sipsdp.CodecListForOffer(callerOffer), callerOffer.Direction)
	if err != nil {
		_ = s.inbound.Respond(sip.StatusNotAcceptableHere, "Not Acceptable Here", nil)
		s.fail(parent, "unable to build callee SDP", "488")
		return
	}

	inviteCtx, cancelInvite := context.WithTimeout(parent, s.engine.cfg.InviteTimeout)
	stopCancelWatch := context.AfterFunc(s.inbound.Context(), cancelInvite)
	defer func() {
		stopCancelWatch()
		cancelInvite()
	}()
	outbound, err := s.createOutbound(inviteCtx, inboundRequest, binding, outboundOffer)
	if err != nil {
		_ = s.inbound.Respond(sip.StatusServiceUnavailable, "Service Unavailable", nil)
		s.fail(parent, "outbound invite failed", "INVITE")
		return
	}
	s.outbound = outbound

	err = outbound.WaitAnswer(inviteCtx, sipgo.AnswerOptions{OnResponse: func(response *sip.Response) error {
		if !response.IsProvisional() || response.StatusCode == sip.StatusTrying {
			return nil
		}
		responseBody := slices.Clone(response.Body())
		responseHeaders := contentTypeHeaders(response)
		if response.StatusCode == statusSessionProgress && len(response.Body()) > 0 {
			var err error
			responseBody, err = s.applyMediaAnswer(parent, response.Body())
			if err != nil {
				return errors.Join(errMediaNegotiation, err)
			}
			if err := s.transition(parent, call.StateEarlyMedia, s.call.CalleeLeg, strconv.Itoa(response.StatusCode), ""); err != nil {
				return err
			}
		} else if response.StatusCode == sip.StatusRinging || response.StatusCode == statusSessionProgress {
			if err := s.transition(parent, call.StateRinging, s.call.CalleeLeg, strconv.Itoa(response.StatusCode), ""); err != nil {
				return err
			}
		}
		return s.inbound.Respond(response.StatusCode, response.Reason, responseBody, responseHeaders...)
	}})
	if err != nil {
		var responseErr *sipgo.ErrDialogResponse
		if errors.As(err, &responseErr) && responseErr.Res != nil {
			_ = s.inbound.Respond(responseErr.Res.StatusCode, responseErr.Res.Reason, slices.Clone(responseErr.Res.Body()))
			s.fail(parent, responseErr.Res.Reason, strconv.Itoa(responseErr.Res.StatusCode))
		} else if errors.Is(err, errMediaNegotiation) {
			_ = s.inbound.Respond(sip.StatusNotAcceptableHere, "Not Acceptable Here", nil)
			s.fail(parent, "callee early media negotiation failed", "488")
		} else {
			s.fail(parent, "outbound invite canceled or timed out", "CANCEL")
		}
		return
	}
	if len(outbound.InviteResponse.Body()) > 0 {
		if _, err := s.applyMediaAnswer(parent, outbound.InviteResponse.Body()); err != nil {
			_ = outbound.Ack(parent)
			_ = outbound.Bye(parent)
			_ = s.inbound.Respond(sip.StatusNotAcceptableHere, "Not Acceptable Here", nil)
			s.fail(parent, "callee SDP negotiation failed", "488")
			return
		}
	}
	if len(s.callerAnswer) == 0 {
		_ = outbound.Ack(parent)
		_ = outbound.Bye(parent)
		_ = s.inbound.Respond(sip.StatusNotAcceptableHere, "Not Acceptable Here", nil)
		s.fail(parent, "callee answer contained no usable SDP", "488")
		return
	}
	if err := outbound.Ack(parent); err != nil {
		_ = s.inbound.Respond(sip.StatusServiceUnavailable, "Service Unavailable", nil)
		s.fail(parent, "outbound ACK failed", "ACK")
		return
	}
	s.engine.mu.Lock()
	s.engine.byOutbound[outbound.ID] = s
	s.engine.mu.Unlock()
	if err := s.transition(parent, call.StateAnswered, s.call.CalleeLeg, "200", ""); err != nil {
		_ = outbound.Bye(parent)
		return
	}
	responseHeaders := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	if err := s.inbound.Respond(sip.StatusOK, "OK", slices.Clone(s.callerAnswer), responseHeaders...); err != nil {
		_ = outbound.Bye(parent)
		s.terminate(parent, "caller answer transaction failed", "CANCEL", s.call.CallerLeg)
		return
	}
	stopCancelWatch()

	for {
		select {
		case command := <-s.commands:
			if s.handleCommand(parent, command) {
				return
			}
		case <-parent.Done():
			s.handleHangup(context.WithoutCancel(parent), hangupCaller)
			return
		case mediaErr := <-s.mediaResult:
			s.mediaResult = nil
			if mediaErr != nil {
				s.engine.log.Error("media session failed", "call_id", s.call.ID, "node_id", s.call.NodeID, "error", mediaErr)
			}
			s.handleHangup(context.WithoutCancel(parent), hangupCaller)
			return
		}
	}
}

// Wait rejects new calls and blocks until admitted sessions release resources.
func (e *Engine) Wait() {
	e.mu.Lock()
	e.stopping = true
	e.mu.Unlock()
	e.wg.Wait()
}

func (s *session) createOutbound(ctx context.Context, inbound *sip.Request, binding registration.Binding, offer []byte) (*sipgo.DialogClientSession, error) {
	var recipient sip.Uri
	if err := sip.ParseUri(binding.Contact, &recipient); err != nil {
		return nil, fmt.Errorf("parse registered contact: %w", err)
	}
	request := sip.NewRequest(sip.INVITE, recipient)
	request.SetTransport(string(binding.Transport))
	request.SetDestination(binding.RouteTarget)
	request.SetBody(slices.Clone(offer))
	from := sip.HeaderClone(inbound.From()).(*sip.FromHeader)
	from.Params.Remove("tag")
	from.Params.Add("tag", sip.GenerateTagN(16))
	request.AppendHeader(from)
	request.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	return s.engine.dialogClient.WriteInvite(ctx, request)
}

func (s *session) applyMediaAnswer(ctx context.Context, body []byte) ([]byte, error) {
	calleeAnswer, err := sipsdp.ParseAudio(body)
	if err != nil {
		return nil, fmt.Errorf("parse callee SDP: %w", err)
	}
	mapping, err := sipsdp.Negotiate(s.callerOffer, calleeAnswer)
	if err != nil {
		return nil, err
	}
	if s.mediaMapping.Name != "" && s.mediaMapping != mapping {
		return nil, fmt.Errorf("callee changed negotiated media mapping")
	}
	if err := s.media.Configure(s.callerOffer, calleeAnswer, mapping); err != nil {
		return nil, err
	}
	s.mediaMapping = mapping
	callerRTP, callerRTCP, err := s.media.LocalEndpoint(mediasession.CallerLeg)
	if err != nil {
		return nil, err
	}
	answer, err := sipsdp.BuildAudio(callerRTP, callerRTCP, sipsdp.CodecListForAnswer(mapping), calleeAnswer.Direction)
	if err != nil {
		return nil, err
	}
	s.callerAnswer = slices.Clone(answer)
	s.startMedia(ctx)
	return answer, nil
}

func (s *session) startMedia(ctx context.Context) {
	if s.mediaResult != nil {
		return
	}
	mediaCtx, cancel := context.WithCancel(ctx)
	s.mediaCancel = cancel
	s.mediaResult = make(chan error, 1)
	go func() { s.mediaResult <- s.media.Run(mediaCtx) }()
}

func (s *session) stopMedia(ctx context.Context) {
	if s.media == nil {
		return
	}
	if s.mediaCancel != nil {
		s.mediaCancel()
	}
	_ = s.media.Close()
	if s.mediaResult != nil {
		if err := <-s.mediaResult; err != nil {
			s.engine.log.Error("media session stopped", "call_id", s.call.ID, "node_id", s.call.NodeID, "error", err)
		}
	}
	s.media.PublishSummary(ctx)
}

func contentTypeHeaders(response *sip.Response) []sip.Header {
	if len(response.Body()) == 0 {
		return nil
	}
	if contentType := response.ContentType(); contentType != nil {
		return []sip.Header{sip.HeaderClone(contentType)}
	}
	return []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
}

func (s *session) handleHangup(ctx context.Context, origin hangupOrigin) {
	legID := s.call.CallerLeg
	if origin == hangupCallee {
		legID = s.call.CalleeLeg
	}
	if err := s.transition(ctx, call.StateTerminating, legID, "BYE", "normal clearing"); err != nil {
		return
	}
	hangupCtx, cancel := context.WithTimeout(ctx, s.engine.cfg.DispatchTimeout)
	defer cancel()
	if origin == hangupCaller && s.outbound != nil {
		_ = s.outbound.Bye(hangupCtx)
	}
	if origin == hangupCallee {
		_ = s.inbound.Bye(hangupCtx)
	}
	_ = s.transition(ctx, call.StateTerminated, legID, "200", "normal clearing")
}

func (s *session) fail(ctx context.Context, reason, protocolEvent string) {
	_ = s.transition(ctx, call.StateFailed, s.call.CalleeLeg, protocolEvent, reason)
}

func (s *session) terminate(ctx context.Context, reason, protocolEvent string, legID call.LegID) {
	_ = s.transition(ctx, call.StateTerminating, legID, protocolEvent, reason)
	_ = s.transition(ctx, call.StateTerminated, legID, protocolEvent, reason)
}

func (s *session) transition(ctx context.Context, state call.State, legID call.LegID, protocolEvent, reason string) error {
	dispatchCtx, cancel := context.WithTimeout(ctx, s.engine.cfg.DispatchTimeout)
	defer cancel()
	return s.actor.Transition(dispatchCtx, call.Transition{State: state, LegID: legID, ProtocolEvent: protocolEvent, Reason: reason})
}

func (e *Engine) handleBye(request *sip.Request, transaction sip.ServerTransaction) {
	if dialogID, err := sip.DialogIDFromRequestUAS(request); err == nil {
		e.mu.RLock()
		activeSession := e.byInbound[dialogID]
		e.mu.RUnlock()
		if activeSession != nil {
			if err := e.dialogServer.ReadBye(request, transaction); err == nil {
				activeSession.enqueue(sessionCommand{kind: commandHangup, origin: hangupCaller})
				return
			}
		}
	}
	if dialogID, err := sip.DialogIDFromRequestUAC(request); err == nil {
		e.mu.RLock()
		activeSession := e.byOutbound[dialogID]
		e.mu.RUnlock()
		if activeSession != nil {
			if err := e.dialogClient.ReadBye(request, transaction); err == nil {
				activeSession.enqueue(sessionCommand{kind: commandHangup, origin: hangupCallee})
				return
			}
		}
	}
	_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
}

func (s *session) enqueue(command sessionCommand) {
	dispatch := time.NewTimer(s.engine.cfg.DispatchTimeout)
	defer dispatch.Stop()
	select {
	case s.commands <- command:
	case <-s.done:
	case <-dispatch.C:
		s.engine.log.Error("call command mailbox full", "call_id", s.call.ID, "node_id", s.call.NodeID)
	}
}

func (s *session) enqueueAndWait(command sessionCommand, timeout time.Duration) bool {
	command.result = make(chan struct{})
	s.enqueue(command)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-command.result:
		return true
	case <-s.done:
		return false
	case <-timer.C:
		return false
	}
}

func (e *Engine) sourceIsRegistered(ctx context.Context, aor registration.AoR, source, transport string) (bool, error) {
	bindings, err := e.registrations.List(ctx, e.cfg.TenantID, aor)
	if err != nil {
		return false, err
	}
	for _, binding := range bindings {
		if binding.RouteTarget == source && string(binding.Transport) == strings.ToLower(transport) {
			return true, nil
		}
	}
	return false, nil
}

func (e *Engine) reserve() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping || e.reserved >= e.cfg.MaxActiveCalls {
		return false
	}
	e.reserved++
	e.wg.Add(1)
	return true
}

func (e *Engine) release() {
	e.mu.Lock()
	e.reserved--
	e.mu.Unlock()
	e.wg.Done()
}

func (e *Engine) remove(activeSession *session) {
	e.mu.Lock()
	delete(e.byInbound, activeSession.inbound.ID)
	if activeSession.outbound != nil {
		delete(e.byOutbound, activeSession.outbound.ID)
	}
	e.reserved--
	e.mu.Unlock()
	if activeSession.outbound != nil {
		_ = activeSession.outbound.Close()
	}
}
