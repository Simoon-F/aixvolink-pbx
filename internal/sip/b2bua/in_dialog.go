package b2bua

import (
	"context"
	"fmt"
	"slices"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	mediasession "github.com/Simoon-F/aixvolink-pbx/internal/media/session"
	sipsdp "github.com/Simoon-F/aixvolink-pbx/internal/sip/sdp"
	"github.com/emiago/sipgo/sip"
)

func (e *Engine) handleReInvite(request *sip.Request, transaction sip.ServerTransaction) {
	activeSession := e.findInboundSession(request)
	if activeSession == nil || activeSession.outbound == nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		return
	}
	if !activeSession.beginReInvite() {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusRequestPending, "Request Pending", nil))
		return
	}
	defer activeSession.finishReInvite()
	if err := activeSession.inbound.ReadRequest(request, transaction); err != nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Dialog Request", nil))
		return
	}
	if !activeSession.enqueueAndWait(sessionCommand{kind: commandReInvite, request: request.Clone(), transaction: transaction}, activeSession.engine.cfg.InviteTimeout) {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusRequestTimeout, "Request Timeout", nil))
	}
}

func (e *Engine) handleRefer(request *sip.Request, transaction sip.ServerTransaction) {
	activeSession := e.findInboundSession(request)
	if activeSession == nil || activeSession.outbound == nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		return
	}
	if request.GetHeader("Refer-To") == nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Missing Refer-To", nil))
		return
	}
	if err := activeSession.inbound.ReadRequest(request, transaction); err != nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Dialog Request", nil))
		return
	}
	if !activeSession.enqueueAndWait(sessionCommand{kind: commandRefer, request: request.Clone(), transaction: transaction}, activeSession.engine.cfg.InviteTimeout) {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusRequestTimeout, "Request Timeout", nil))
	}
}

func (e *Engine) handleNotify(request *sip.Request, transaction sip.ServerTransaction) {
	activeSession := e.findOutboundSession(request)
	if activeSession == nil || activeSession.inbound == nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		return
	}
	if err := activeSession.outbound.ReadRequest(request, transaction); err != nil {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Dialog Request", nil))
		return
	}
	if !activeSession.enqueueAndWait(sessionCommand{kind: commandNotify, request: request.Clone(), transaction: transaction}, activeSession.engine.cfg.InviteTimeout) {
		_ = transaction.Respond(sip.NewResponseFromRequest(request, sip.StatusRequestTimeout, "Request Timeout", nil))
	}
}

func (e *Engine) findInboundSession(request *sip.Request) *session {
	dialogID, err := sip.DialogIDFromRequestUAS(request)
	if err != nil {
		e.log.Debug("inbound dialog ID failed", "error", err)
		return nil
	}
	e.mu.RLock()
	activeSession := e.byInbound[dialogID]
	activeCount := len(e.byInbound)
	e.mu.RUnlock()
	if activeSession == nil {
		e.log.Debug("inbound dialog not found", "method", request.Method, "call_id", request.CallID().Value(), "dialog_id", dialogID, "active_count", activeCount)
	}
	return activeSession
}

func (e *Engine) findOutboundSession(request *sip.Request) *session {
	dialogID, err := sip.DialogIDFromRequestUAC(request)
	if err != nil {
		return nil
	}
	e.mu.RLock()
	activeSession := e.byOutbound[dialogID]
	e.mu.RUnlock()
	return activeSession
}

// handleCommand returns true when the session has reached a terminal state.
func (s *session) handleCommand(ctx context.Context, command sessionCommand) bool {
	if command.result != nil {
		defer close(command.result)
	}
	switch command.kind {
	case commandHangup:
		s.handleHangup(ctx, command.origin)
		return true
	case commandReInvite:
		s.handleReInviteCommand(ctx, command)
	case commandRefer:
		s.relayToCallee(ctx, command)
	case commandNotify:
		s.relayToCaller(ctx, command)
	}
	return false
}

func (s *session) handleReInviteCommand(ctx context.Context, command sessionCommand) {
	if s.hasPendingReInvite() {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusRequestPending, "Request Pending", nil))
		return
	}
	offer, err := sipsdp.ParseAudio(command.request.Body())
	if err != nil {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusNotAcceptableHere, "Not Acceptable Here", nil))
		return
	}
	calleeRTP, calleeRTCP, err := s.media.LocalEndpoint(mediasession.CalleeLeg)
	if err != nil {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusInternalServerError, "Server Internal Error", nil))
		return
	}
	outboundOffer, err := sipsdp.BuildAudio(calleeRTP, calleeRTCP, sipsdp.CodecListForOffer(offer), offer.Direction)
	if err != nil {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusNotAcceptableHere, "Not Acceptable Here", nil))
		return
	}
	reinvite := sip.NewRequest(sip.INVITE, s.outbound.InviteRequest.Recipient)
	reinvite.SetBody(outboundOffer)
	reinvite.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	requestCtx, cancel := context.WithTimeout(ctx, s.engine.cfg.InviteTimeout)
	response, err := s.outbound.Do(requestCtx, reinvite)
	cancel()
	if err != nil {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusRequestTimeout, "Request Timeout", nil))
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, response.StatusCode, response.Reason, slices.Clone(response.Body())))
		return
	}
	s.outbound.InviteRequest = reinvite
	s.outbound.InviteResponse = response
	if err := s.outbound.Ack(ctx); err != nil {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusServiceUnavailable, "Service Unavailable", nil))
		return
	}
	previousOffer := s.callerOffer
	s.callerOffer = offer
	answer, err := s.applyMediaAnswer(ctx, response.Body())
	if err != nil {
		s.callerOffer = previousOffer
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusNotAcceptableHere, "Not Acceptable Here", nil))
		return
	}
	result := sip.NewResponseFromRequest(command.request, sip.StatusOK, "OK", answer)
	result.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	state := call.StateAnswered
	if offer.Direction != sipsdp.DirectionSendRecv {
		state = call.StateHeld
	}
	if err := s.transition(ctx, state, s.call.CallerLeg, "re-INVITE", ""); err != nil {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusInternalServerError, "Server Internal Error", nil))
		return
	}
	s.setPendingReInvite(command.transaction, command.request.CSeq().SeqNo)
	_ = command.transaction.Respond(result)
}

func (s *session) hasPendingReInvite() bool {
	s.pendingMu.Lock()
	transaction := s.pendingReInvite
	s.pendingMu.Unlock()
	if transaction == nil {
		return false
	}
	select {
	case <-transaction.Acks():
		s.completePendingReInviteTransaction(transaction)
		return false
	default:
		return true
	}
}

func (s *session) beginReInvite() bool {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	if s.reInviteBusy || s.pendingReInvite != nil {
		return false
	}
	s.reInviteBusy = true
	return true
}

func (s *session) finishReInvite() {
	s.pendingMu.Lock()
	s.reInviteBusy = false
	s.pendingMu.Unlock()
}

func (s *session) setPendingReInvite(transaction sip.ServerTransaction, cseq uint32) {
	s.pendingMu.Lock()
	s.pendingReInvite = transaction
	s.pendingCSeq = cseq
	s.pendingMu.Unlock()
	go s.watchPendingReInvite(transaction)
}

func (s *session) completePendingReInvite() {
	s.pendingMu.Lock()
	transaction := s.pendingReInvite
	s.pendingReInvite = nil
	s.pendingCSeq = 0
	s.pendingMu.Unlock()
	if transaction != nil {
		transaction.Terminate()
	}
}

func (s *session) completePendingReInviteCSeq(cseq uint32) {
	s.pendingMu.Lock()
	transaction := s.pendingReInvite
	if transaction != nil && s.pendingCSeq == cseq {
		s.pendingReInvite = nil
		s.pendingCSeq = 0
	} else {
		transaction = nil
	}
	s.pendingMu.Unlock()
	if transaction != nil {
		transaction.Terminate()
	}
}

func (s *session) completePendingReInviteTransaction(expected sip.ServerTransaction) {
	s.pendingMu.Lock()
	transaction := s.pendingReInvite
	if transaction == expected {
		s.pendingReInvite = nil
		s.pendingCSeq = 0
	} else {
		transaction = nil
	}
	s.pendingMu.Unlock()
	if transaction != nil {
		transaction.Terminate()
	}
}

func (s *session) watchPendingReInvite(transaction sip.ServerTransaction) {
	select {
	case <-transaction.Acks():
		s.completePendingReInviteTransaction(transaction)
	case <-transaction.Done():
		s.completePendingReInviteTransaction(transaction)
	case <-s.done:
	}
}

func (s *session) relayToCallee(ctx context.Context, command sessionCommand) {
	request := cloneInDialogRequest(command.request, s.outbound.InviteRequest.Recipient)
	requestCtx, cancel := context.WithTimeout(ctx, s.engine.cfg.DispatchTimeout)
	response, err := s.outbound.Do(requestCtx, request)
	cancel()
	s.respondRelayed(command, response, err)
}

func (s *session) relayToCaller(ctx context.Context, command sessionCommand) {
	recipient := s.inbound.InviteRequest.From().Address
	if contact := s.inbound.InviteRequest.Contact(); contact != nil {
		recipient = contact.Address
	}
	request := cloneInDialogRequest(command.request, recipient)
	requestCtx, cancel := context.WithTimeout(ctx, s.engine.cfg.DispatchTimeout)
	response, err := s.inbound.Do(requestCtx, request)
	cancel()
	s.respondRelayed(command, response, err)
}

func cloneInDialogRequest(source *sip.Request, recipient sip.Uri) *sip.Request {
	request := sip.NewRequest(source.Method, recipient)
	request.SetBody(slices.Clone(source.Body()))
	for _, name := range []string{"Refer-To", "Referred-By", "Event", "Subscription-State", "Content-Type"} {
		for _, header := range source.GetHeaders(name) {
			request.AppendHeader(sip.HeaderClone(header))
		}
	}
	return request
}

func (s *session) respondRelayed(command sessionCommand, response *sip.Response, err error) {
	if err != nil || response == nil {
		_ = command.transaction.Respond(sip.NewResponseFromRequest(command.request, sip.StatusRequestTimeout, "Request Timeout", nil))
		return
	}
	result := sip.NewResponseFromRequest(command.request, response.StatusCode, response.Reason, slices.Clone(response.Body()))
	for _, name := range []string{"Event", "Subscription-State", "Content-Type"} {
		for _, header := range response.GetHeaders(name) {
			result.AppendHeader(sip.HeaderClone(header))
		}
	}
	if err := command.transaction.Respond(result); err != nil {
		s.engine.log.Error("relay in-dialog response", "call_id", s.call.ID, "node_id", s.call.NodeID, "error", fmt.Errorf("respond: %w", err))
	}
}
