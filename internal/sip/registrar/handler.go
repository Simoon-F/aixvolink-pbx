// Package registrar adapts SIP REGISTER transactions to registration policy.
package registrar

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
	"github.com/emiago/sipgo/sip"
)

// Config defines REGISTER policy and transaction bounds.
type Config struct {
	Realm              string
	DefaultExpires     time.Duration
	MinExpires         time.Duration
	MaxExpires         time.Duration
	TransactionTimeout time.Duration
}

const statusIntervalTooBrief = 423

// Handler authenticates and applies REGISTER requests.
type Handler struct {
	cfg     Config
	auth    *sipauth.Authenticator
	manager *registration.Manager
}

// NewHandler constructs a REGISTER handler.
func NewHandler(cfg Config, authenticator *sipauth.Authenticator, manager *registration.Manager) (*Handler, error) {
	if cfg.Realm == "" {
		return nil, fmt.Errorf("registrar realm is required")
	}
	if cfg.MinExpires <= 0 || cfg.DefaultExpires < cfg.MinExpires || cfg.MaxExpires < cfg.DefaultExpires {
		return nil, fmt.Errorf("registration expiration bounds are invalid")
	}
	if cfg.TransactionTimeout <= 0 {
		return nil, fmt.Errorf("transaction timeout must be positive")
	}
	if authenticator == nil {
		return nil, fmt.Errorf("authenticator is required")
	}
	if manager == nil {
		return nil, fmt.Errorf("registration manager is required")
	}
	return &Handler{cfg: cfg, auth: authenticator, manager: manager}, nil
}

// Handle processes one REGISTER server transaction.
func (h *Handler) Handle(ctx context.Context, request *sip.Request, transaction sip.ServerTransaction) {
	transactionCtx, cancel := context.WithTimeout(ctx, h.cfg.TransactionTimeout)
	defer cancel()
	if err := h.handle(transactionCtx, request, transaction); err != nil {
		if errors.Is(err, errResponseFailed) {
			return
		}
		response := sip.NewResponseFromRequest(request, sip.StatusInternalServerError, "Server Internal Error", nil)
		_ = transaction.Respond(response)
	}
}

func (h *Handler) handle(ctx context.Context, request *sip.Request, transaction sip.ServerTransaction) error {
	if request.To() == nil || request.To().Address.User == "" || request.To().Address.Host != h.cfg.Realm {
		return respond(transaction, sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Request", nil))
	}
	authorization := ""
	if header := request.GetHeader("Authorization"); header != nil {
		authorization = header.Value()
	}
	credential, authErr := h.auth.Verify(ctx, request.Method.String(), request.Recipient.Addr(), authorization)
	if authErr != nil || credential.Username != request.To().Address.User {
		stale := errors.Is(authErr, sipauth.ErrStaleNonce)
		challenge, err := h.auth.Challenge(stale)
		if err != nil {
			return fmt.Errorf("create digest challenge: %w", err)
		}
		response := sip.NewResponseFromRequest(request, sip.StatusUnauthorized, "Unauthorized", nil)
		response.AppendHeader(sip.NewHeader("WWW-Authenticate", challenge))
		return respond(transaction, response)
	}

	aor := registration.AoR("sip:" + credential.Username + "@" + credential.Realm)
	contactHeaders := request.GetHeaders("Contact")
	if len(contactHeaders) == 0 {
		return h.respondBindings(ctx, transaction, request, credential.TenantID, aor)
	}
	received, err := netip.ParseAddrPort(request.Source())
	if err != nil {
		return respond(transaction, sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Request", nil))
	}
	transport, err := parseTransport(request.Transport())
	if err != nil {
		return respond(transaction, sip.NewResponseFromRequest(request, sip.StatusUnsupportedMediaType, "Unsupported Transport", nil))
	}
	userAgent := headerValue(request.GetHeader("User-Agent"), 255)

	for _, genericHeader := range contactHeaders {
		contact, ok := genericHeader.(*sip.ContactHeader)
		if !ok {
			return respond(transaction, sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Contact", nil))
		}
		expires, err := h.contactExpires(request, contact)
		if err != nil {
			if errors.Is(err, errIntervalTooBrief) {
				response := sip.NewResponseFromRequest(request, statusIntervalTooBrief, "Interval Too Brief", nil)
				response.AppendHeader(sip.NewHeader("Min-Expires", strconv.FormatInt(int64(h.cfg.MinExpires/time.Second), 10)))
				return respond(transaction, response)
			}
			return respond(transaction, sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Expires", nil))
		}
		contactValue := contact.Address.Addr()
		if contact.Address.Wildcard {
			contactValue = "*"
		}
		q, err := parseQ(contact)
		if err != nil {
			return respond(transaction, sip.NewResponseFromRequest(request, sip.StatusBadRequest, "Bad Contact", nil))
		}
		_, err = h.manager.Apply(ctx, registration.Request{
			TenantID: credential.TenantID, AoR: aor, Contact: contactValue,
			RouteTarget: received.String(), Transport: transport, Received: received,
			UserAgent: userAgent, Q: q, Expires: expires, MaxBindings: credential.MaxBindings,
		})
		if registration.IsBindingLimit(err) {
			return respond(transaction, sip.NewResponseFromRequest(request, sip.StatusForbidden, "Binding Limit Reached", nil))
		}
		if err != nil {
			return fmt.Errorf("apply registration: %w", err)
		}
	}
	return h.respondBindings(ctx, transaction, request, credential.TenantID, aor)
}

func (h *Handler) respondBindings(ctx context.Context, transaction sip.ServerTransaction, request *sip.Request, tenantID registration.TenantID, aor registration.AoR) error {
	bindings, err := h.manager.List(ctx, tenantID, aor)
	if err != nil {
		return fmt.Errorf("list response bindings: %w", err)
	}
	response := sip.NewResponseFromRequest(request, sip.StatusOK, "OK", nil)
	now := time.Now().UTC()
	for _, binding := range bindings {
		contact := sip.ContactHeader{Address: sip.Uri{}}
		if err := sip.ParseUri(binding.Contact, &contact.Address); err != nil {
			return fmt.Errorf("parse stored contact: %w", err)
		}
		params := sip.NewParams()
		contact.Params = params.Add("expires", strconv.FormatInt(max(0, int64(binding.ExpiresAt.Sub(now)/time.Second)), 10))
		response.AppendHeader(&contact)
	}
	return respond(transaction, response)
}

var (
	errIntervalTooBrief = errors.New("registration interval too brief")
	errResponseFailed   = errors.New("sip response failed")
)

func (h *Handler) contactExpires(request *sip.Request, contact *sip.ContactHeader) (time.Duration, error) {
	seconds := int64(h.cfg.DefaultExpires / time.Second)
	if header := request.GetHeader("Expires"); header != nil {
		parsed, err := strconv.ParseInt(header.Value(), 10, 32)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid Expires header")
		}
		seconds = parsed
	}
	if value, exists := contact.Params.Get("expires"); exists {
		parsed, err := strconv.ParseInt(value, 10, 32)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid Contact expires parameter")
		}
		seconds = parsed
	}
	if seconds == 0 {
		return 0, nil
	}
	duration := time.Duration(seconds) * time.Second
	if duration < h.cfg.MinExpires {
		return 0, errIntervalTooBrief
	}
	if duration > h.cfg.MaxExpires {
		return h.cfg.MaxExpires, nil
	}
	return duration, nil
}

func parseTransport(value string) (registration.Transport, error) {
	switch strings.ToLower(value) {
	case "udp", "":
		return registration.TransportUDP, nil
	case "tcp":
		return registration.TransportTCP, nil
	default:
		return "", fmt.Errorf("unsupported transport %q", value)
	}
}

func parseQ(contact *sip.ContactHeader) (float32, error) {
	value, exists := contact.Params.Get("q")
	if !exists {
		return 1, nil
	}
	parsed, err := strconv.ParseFloat(value, 32)
	if err != nil || parsed < 0 || parsed > 1 {
		return 0, fmt.Errorf("q value must be between 0 and 1")
	}
	return float32(parsed), nil
}

func headerValue(header sip.Header, maxLength int) string {
	if header == nil {
		return ""
	}
	value := header.Value()
	if len(value) > maxLength {
		return value[:maxLength]
	}
	return value
}

func respond(transaction sip.ServerTransaction, response *sip.Response) error {
	if err := transaction.Respond(response); err != nil {
		return fmt.Errorf("%w: respond to REGISTER: %w", errResponseFailed, err)
	}
	return nil
}
