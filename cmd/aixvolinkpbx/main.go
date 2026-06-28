// Command aixvolinkpbx assembles and runs the PBX service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/app"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	"github.com/Simoon-F/aixvolink-pbx/internal/event"
	mysqlstore "github.com/Simoon-F/aixvolink-pbx/internal/platform/mysql"
	"golang.org/x/sync/errgroup"
)

const version = "0.2.0-phase2"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		slog.Error("AixvoLinkPBX stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string) (runErr error) {
	flags := flag.NewFlagSet("aixvolinkpbx", flag.ContinueOnError)
	showVersion := flags.Bool("version", false, "print version and exit")
	bindHost := flags.String("sip-bind", "127.0.0.1", "SIP bind and advertised IP")
	sipPort := flags.Int("sip-port", 5060, "SIP UDP/TCP port")
	realm := flags.String("realm", "pbx.example.invalid", "SIP authentication realm")
	tenantID := flags.String("tenant-id", "default", "single-node tenant ID")
	nodeID := flags.String("node-id", "pbx-development-1", "PBX node ID")
	mediaBind := flags.String("media-bind", "127.0.0.1", "RTP/RTCP bind IP")
	mediaAdvertised := flags.String("media-advertised", "127.0.0.1", "RTP/RTCP advertised IP")
	rtpStart := flags.Uint("rtp-start", 10000, "first even RTP port")
	rtpEnd := flags.Uint("rtp-end", 19999, "last odd RTCP port")
	if err := flags.Parse(arguments); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	mediaBindIP, err := netip.ParseAddr(*mediaBind)
	if err != nil {
		return fmt.Errorf("parse media bind IP: %w", err)
	}
	mediaAdvertisedIP, err := netip.ParseAddr(*mediaAdvertised)
	if err != nil {
		return fmt.Errorf("parse media advertised IP: %w", err)
	}
	if *rtpStart > 65535 || *rtpEnd > 65535 {
		return fmt.Errorf("media port range exceeds 65535")
	}
	dsn := os.Getenv("AIXVOLINKPBX_MYSQL_DSN")
	if dsn == "" {
		return fmt.Errorf("environment variable AIXVOLINKPBX_MYSQL_DSN is required")
	}
	nonceSecret := []byte(os.Getenv("AIXVOLINKPBX_NONCE_SECRET"))
	if len(nonceSecret) < 32 {
		return fmt.Errorf("environment variable AIXVOLINKPBX_NONCE_SECRET must contain at least 32 bytes")
	}

	connectCtx, cancelConnect := context.WithTimeout(ctx, 5*time.Second)
	store, err := mysqlstore.Open(connectCtx, dsn)
	cancelConnect()
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	callBus, err := event.NewCallBus(1024)
	if err != nil {
		return err
	}
	mediaBus, err := event.NewMediaBus(1024)
	if err != nil {
		return err
	}
	application, err := app.New(app.Config{
		BindHost: *bindHost, SIPPort: *sipPort, Realm: *realm,
		TenantID: registration.TenantID(*tenantID), NodeID: call.NodeID(*nodeID),
		NonceSecret: nonceSecret, NonceTTL: 5 * time.Minute, MaxReplayEntries: 10000,
		DefaultRegisterExpiry: 5 * time.Minute, MinRegisterExpiry: time.Minute,
		MaxRegisterExpiry: time.Hour, RegisterCleanup: 30 * time.Second,
		TransactionTimeout: 5 * time.Second, InviteTimeout: 30 * time.Second,
		DispatchTimeout: 5 * time.Second, MaxActiveCalls: 1000, CallMailboxSize: 64,
		MediaBindIP: mediaBindIP, MediaAdvertisedIP: mediaAdvertisedIP,
		RTPStartPort: uint16(*rtpStart), RTPEndPort: uint16(*rtpEnd), RTPReadPoll: 250 * time.Millisecond,
		MediaInactivity: 10 * time.Second, RTCPInterval: 5 * time.Second,
		MediaSummaryInterval: 5 * time.Second, MediaSummaryTimeout: 100 * time.Millisecond,
	}, store, store, callBus, mediaBus, slog.Default())
	if err != nil {
		return err
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return callBus.Run(groupCtx, store) })
	group.Go(func() error { return mediaBus.Run(groupCtx, store) })
	group.Go(func() error { return application.Run(groupCtx) })
	if err := group.Wait(); err != nil {
		return fmt.Errorf("run service: %w", err)
	}
	return nil
}
