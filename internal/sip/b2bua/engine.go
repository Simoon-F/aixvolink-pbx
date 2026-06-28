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

	mu         sync.RWMutex
	stopping   bool
	reserved   int
	byInbound  map[string]*session
	byOutbound map[string]*session
	wg         sync.WaitGroup
}

type session struct {
	engine   *Engine
	call     call.Call
	actor    *call.Actor
	inbound  *sipgo.DialogServerSession
	outbound *sipgo.DialogClientSession
	commands chan hangupOrigin
	done     chan struct{}
}

type hangupOrigin string

const (
	hangupCaller          hangupOrigin = "caller"
	hangupCallee          hangupOrigin = "callee"
	statusSessionProgress              = 183
)

// NewEngine constructs a stopped B2BUA engine.
func NewEngine(
	cfg Config,
	registrations *registration.Manager,
	publisher call.Publisher,
	clock call.Clock,
	client *sipgo.Client,
	contact sip.ContactHeader,
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
	if registrations == nil || publisher == nil || clock == nil || client == nil || logger == nil {
		return nil, fmt.Errorf("B2BUA dependencies are required")
	}
	return &Engine{
		cfg: cfg, registrations: registrations, publisher: publisher, clock: clock, log: logger,
		dialogServer: sipgo.NewDialogServerCache(client, contact),
		dialogClient: sipgo.NewDialogClientCache(client, contact),
		byInbound:    make(map[string]*session, cfg.MaxActiveCalls),
		byOutbound:   make(map[string]*session, cfg.MaxActiveCalls),
	}, nil
}

// RegisterHandlers connects the engine to a sipgo server for the supplied process context.
func (e *Engine) RegisterHandlers(ctx context.Context, server *sipgo.Server) {
	server.OnInvite(func(request *sip.Request, transaction sip.ServerTransaction) {
		e.handleInvite(ctx, request, transaction)
	})
	server.OnAck(func(request *sip.Request, transaction sip.ServerTransaction) {
		if err := e.dialogServer.ReadAck(request, transaction); err != nil {
			e.log.Debug("ignoring unmatched ACK", "error", err)
		}
	})
	server.OnBye(e.handleBye)
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
		commands: make(chan hangupOrigin, e.cfg.MailboxSize), done: make(chan struct{}),
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

	inviteCtx, cancelInvite := context.WithTimeout(parent, s.engine.cfg.InviteTimeout)
	stopCancelWatch := context.AfterFunc(s.inbound.Context(), cancelInvite)
	defer func() {
		stopCancelWatch()
		cancelInvite()
	}()
	outbound, err := s.createOutbound(inviteCtx, inboundRequest, binding)
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
		if response.StatusCode == sip.StatusRinging || response.StatusCode == statusSessionProgress {
			if err := s.transition(parent, call.StateRinging, s.call.CalleeLeg, strconv.Itoa(response.StatusCode), ""); err != nil {
				return err
			}
		}
		return s.inbound.Respond(response.StatusCode, response.Reason, slices.Clone(response.Body()))
	}})
	if err != nil {
		var responseErr *sipgo.ErrDialogResponse
		if errors.As(err, &responseErr) && responseErr.Res != nil {
			_ = s.inbound.Respond(responseErr.Res.StatusCode, responseErr.Res.Reason, slices.Clone(responseErr.Res.Body()))
			s.fail(parent, responseErr.Res.Reason, strconv.Itoa(responseErr.Res.StatusCode))
		} else {
			s.fail(parent, "outbound invite canceled or timed out", "CANCEL")
		}
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
	responseHeaders := make([]sip.Header, 0, 1)
	if contentType := outbound.InviteResponse.ContentType(); contentType != nil {
		responseHeaders = append(responseHeaders, sip.HeaderClone(contentType))
	}
	if err := s.inbound.Respond(sip.StatusOK, "OK", slices.Clone(outbound.InviteResponse.Body()), responseHeaders...); err != nil {
		_ = outbound.Bye(parent)
		s.terminate(parent, "caller answer transaction failed", "CANCEL", s.call.CallerLeg)
		return
	}
	stopCancelWatch()

	select {
	case origin := <-s.commands:
		s.handleHangup(parent, origin)
	case <-parent.Done():
		s.handleHangup(context.WithoutCancel(parent), hangupCaller)
	}
}

// Wait rejects new calls and blocks until admitted sessions release resources.
func (e *Engine) Wait() {
	e.mu.Lock()
	e.stopping = true
	e.mu.Unlock()
	e.wg.Wait()
}

func (s *session) createOutbound(ctx context.Context, inbound *sip.Request, binding registration.Binding) (*sipgo.DialogClientSession, error) {
	var recipient sip.Uri
	if err := sip.ParseUri(binding.Contact, &recipient); err != nil {
		return nil, fmt.Errorf("parse registered contact: %w", err)
	}
	request := sip.NewRequest(sip.INVITE, recipient)
	request.SetTransport(string(binding.Transport))
	request.SetDestination(binding.RouteTarget)
	request.SetBody(slices.Clone(inbound.Body()))
	from := sip.HeaderClone(inbound.From()).(*sip.FromHeader)
	from.Params.Remove("tag")
	from.Params.Add("tag", sip.GenerateTagN(16))
	request.AppendHeader(from)
	if contentType := inbound.ContentType(); contentType != nil {
		request.AppendHeader(sip.HeaderClone(contentType))
	}
	return s.engine.dialogClient.WriteInvite(ctx, request)
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
				activeSession.enqueue(hangupCaller)
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
				activeSession.enqueue(hangupCallee)
				return
			}
		}
	}
	_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
}

func (s *session) enqueue(origin hangupOrigin) {
	dispatch := time.NewTimer(s.engine.cfg.DispatchTimeout)
	defer dispatch.Stop()
	select {
	case s.commands <- origin:
	case <-s.done:
	case <-dispatch.C:
		s.engine.log.Error("call command mailbox full", "call_id", s.call.ID, "node_id", s.call.NodeID)
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
