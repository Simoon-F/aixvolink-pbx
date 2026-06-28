package integration_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/app"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	mediasession "github.com/Simoon-F/aixvolink-pbx/internal/media/session"
	"github.com/Simoon-F/aixvolink-pbx/internal/platform/memory"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
)

const integrationTimeout = 10 * time.Second

type callRecorder struct {
	mu     sync.Mutex
	events []call.Event
	notify chan call.Event
}

func (r *callRecorder) Publish(ctx context.Context, event call.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	if r.notify != nil {
		select {
		case r.notify <- event:
		default:
		}
	}
	return nil
}

type runningApp struct {
	address  string
	cancel   context.CancelFunc
	result   chan error
	store    *memory.RegistrationStore
	recorder *callRecorder
}

func TestRegistrarOverUDPAndTCPHandlesRefreshUnregisterAndExpiry(t *testing.T) {
	running := startTestApp(t, []sipauth.Credential{
		{TenantID: "tenant-1", Username: "1001", Realm: "pbx.example.invalid", HA1: sipauth.ComputeHA1("1001", "pbx.example.invalid", "password-1001"), MaxBindings: 2},
	})
	manager, err := registration.NewManager(running.store, registration.SystemClock{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	for _, transport := range []string{"udp", "tcp"} {
		t.Run(transport, func(t *testing.T) {
			client := newSIPClient(t, transport, availableAddress(t))
			contact := fmt.Sprintf("sip:1001@127.0.0.1:%d", 16000+len(transport))
			register(t, client, running.address, transport, "1001", "password-1001", contact, 60)
			binding, err := manager.Resolve(context.Background(), "tenant-1", "sip:1001@pbx.example.invalid")
			if err != nil {
				t.Fatalf("Resolve(create) error = %v", err)
			}
			if binding.Contact != contact || string(binding.Transport) != transport {
				t.Fatalf("binding = %+v", binding)
			}

			register(t, client, running.address, transport, "1001", "password-1001", contact, 120)
			refreshed, err := manager.Resolve(context.Background(), "tenant-1", "sip:1001@pbx.example.invalid")
			if err != nil || !refreshed.ExpiresAt.After(binding.ExpiresAt) {
				t.Fatalf("Resolve(refresh) = %+v, %v", refreshed, err)
			}
			register(t, client, running.address, transport, "1001", "password-1001", contact, 0)
		})
	}
	if _, err := manager.Resolve(context.Background(), "tenant-1", "sip:1001@pbx.example.invalid"); !errors.Is(err, registration.ErrNotFound) {
		t.Fatalf("Resolve(after unregister) error = %v", err)
	}
}

func startTestApp(t *testing.T, credentials []sipauth.Credential) *runningApp {
	t.Helper()
	address := availableAddress(t)
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi() error = %v", err)
	}
	registrationStore := memory.NewRegistrationStore()
	recorder := &callRecorder{notify: make(chan call.Event, 128)}
	application, err := app.New(app.Config{
		BindHost: "127.0.0.1", SIPPort: port, Realm: "pbx.example.invalid",
		TenantID: "tenant-1", NodeID: "node-test-1",
		NonceSecret: []byte("0123456789abcdef0123456789abcdef"), NonceTTL: time.Minute, MaxReplayEntries: 128,
		DefaultRegisterExpiry: 5 * time.Minute, MinRegisterExpiry: time.Second,
		MaxRegisterExpiry: time.Hour, RegisterCleanup: 50 * time.Millisecond,
		TransactionTimeout: 3 * time.Second, InviteTimeout: 3 * time.Second,
		DispatchTimeout: 3 * time.Second, MaxActiveCalls: 16, CallMailboxSize: 64,
		MediaBindIP: netip.MustParseAddr("127.0.0.1"), MediaAdvertisedIP: netip.MustParseAddr("127.0.0.1"),
		RTPStartPort: 26000, RTPEndPort: 26063, RTPReadPoll: 20 * time.Millisecond,
		MediaInactivity: 500 * time.Millisecond, RTCPInterval: 100 * time.Millisecond,
		MediaSummaryInterval: 100 * time.Millisecond, MediaSummaryTimeout: 20 * time.Millisecond,
	}, memory.NewCredentialStore(credentials), registrationStore, recorder, mediasession.DiscardPublisher{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("app.New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- application.Run(ctx) }()
	select {
	case <-application.Ready():
	case <-time.After(integrationTimeout):
		cancel()
		t.Fatal("application did not become ready")
	}
	running := &runningApp{address: address, cancel: cancel, result: result, store: registrationStore, recorder: recorder}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("application Run() error = %v", err)
			}
		case <-time.After(integrationTimeout):
			t.Error("application did not stop")
		}
	})
	return running
}

func newSIPClient(t *testing.T, transport, localAddress string) *sipgo.Client {
	t.Helper()
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("phase1-integration"), sipgo.WithUserAgentHostname("127.0.0.1"))
	if err != nil {
		t.Fatalf("NewUA() error = %v", err)
	}
	t.Cleanup(func() {
		if err := ua.Close(); err != nil {
			t.Errorf("UA Close() error = %v", err)
		}
	})
	client, err := sipgo.NewClient(ua, sipgo.WithClientConnectionAddr(localAddress), sipgo.WithClientNAT())
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_ = transport
	return client
}

func register(t *testing.T, client *sipgo.Client, serverAddress, transport, username, password, contactURI string, expires int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationTimeout)
	defer cancel()
	request := sip.NewRequest(sip.REGISTER, sip.Uri{Scheme: "sip", Host: "pbx.example.invalid"})
	request.SetTransport(transport)
	request.SetDestination(serverAddress)
	to := sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: username, Host: "pbx.example.invalid"}}
	from := sip.FromHeader{Address: to.Address, Params: sip.NewParams()}
	from.Params.Add("tag", sip.GenerateTagN(12))
	var contactURIValue sip.Uri
	if err := sip.ParseUri(contactURI, &contactURIValue); err != nil {
		t.Fatalf("ParseUri() error = %v", err)
	}
	contact := sip.ContactHeader{Address: contactURIValue, Params: sip.NewParams()}
	contact.Params.Add("expires", strconv.Itoa(expires))
	request.AppendHeader(&to)
	request.AppendHeader(&from)
	request.AppendHeader(&contact)

	response, err := client.Do(ctx, request, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		t.Fatalf("initial REGISTER error = %v", err)
	}
	if response.StatusCode != sip.StatusUnauthorized {
		t.Fatalf("initial REGISTER status = %d", response.StatusCode)
	}
	challengeHeader := response.GetHeader("WWW-Authenticate")
	if challengeHeader == nil {
		t.Fatal("REGISTER challenge missing")
	}
	challenge, err := digest.ParseChallenge(challengeHeader.Value())
	if err != nil {
		t.Fatalf("ParseChallenge() error = %v", err)
	}
	credentials, err := digest.Digest(challenge, digest.Options{
		Method: sip.REGISTER.String(), URI: request.Recipient.Addr(), Username: username,
		Password: password, Count: 1, Cnonce: "integration-client",
	})
	if err != nil {
		t.Fatalf("Digest() error = %v", err)
	}
	request.RemoveHeader("Via")
	request.AppendHeader(sip.NewHeader("Authorization", credentials.String()))
	response, err = client.Do(ctx, request, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		t.Fatalf("authenticated REGISTER error = %v", err)
	}
	if response.StatusCode != sip.StatusOK {
		t.Fatalf("authenticated REGISTER status = %d", response.StatusCode)
	}
}

func availableAddress(t *testing.T) string {
	t.Helper()
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release address: %v", err)
	}
	return address
}
