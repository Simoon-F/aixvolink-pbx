// Command server runs an isolated in-memory Phase 1 endpoint for SIPp.
package main

import (
	"context"
	"flag"
	"io"
	"log"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/app"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/platform/memory"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
)

type discardPublisher struct{}

func (discardPublisher) Publish(context.Context, call.Event) error { return nil }

func main() {
	port := flag.Int("port", 15060, "SIP UDP/TCP port")
	flag.Parse()

	const realm = "pbx.example.invalid"
	credentials := []sipauth.Credential{
		{TenantID: "tenant-sipp", Username: "1001", Realm: realm, HA1: sipauth.ComputeHA1("1001", realm, "password-1001"), MaxBindings: 8},
		{TenantID: "tenant-sipp", Username: "1002", Realm: realm, HA1: sipauth.ComputeHA1("1002", realm, "password-1002"), MaxBindings: 8},
	}
	application, err := app.New(app.Config{
		BindHost: "127.0.0.1", SIPPort: *port, Realm: realm, TenantID: "tenant-sipp", NodeID: "node-sipp",
		NonceSecret: []byte("phase1-sipp-only-secret-32-bytes!!"), NonceTTL: time.Minute, MaxReplayEntries: 4096,
		DefaultRegisterExpiry: 5 * time.Minute, MinRegisterExpiry: time.Second, MaxRegisterExpiry: time.Hour,
		RegisterCleanup: time.Second, TransactionTimeout: 3 * time.Second, InviteTimeout: 5 * time.Second,
		DispatchTimeout: 3 * time.Second, MaxActiveCalls: 64, CallMailboxSize: 64,
	}, memory.NewCredentialStore(credentials), memory.NewRegistrationStore(), discardPublisher{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := application.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
