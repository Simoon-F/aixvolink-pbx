package mysql_test

import (
	"context"
	"database/sql"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	mediartp "github.com/Simoon-F/aixvolink-pbx/internal/media/rtp"
	mediasession "github.com/Simoon-F/aixvolink-pbx/internal/media/session"
	mysqlstore "github.com/Simoon-F/aixvolink-pbx/internal/platform/mysql"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
)

func TestStorePersistsRegistrarAndCallEvidence(t *testing.T) {
	dsn := os.Getenv("AIXVOLINKPBX_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("AIXVOLINKPBX_TEST_MYSQL_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := mysqlstore.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("database Close() error = %v", err)
		}
	})
	for _, table := range []string{"media_quality_samples", "call_events", "calls", "registrations", "sip_credentials"} {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	migrations, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	for _, migrationPath := range migrations {
		migration, err := os.ReadFile(migrationPath)
		if err != nil {
			t.Fatalf("read migration: %v", err)
		}
		for _, statement := range strings.Split(string(migration), ";") {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("apply migration %s: %v", migrationPath, err)
			}
		}
	}

	ha1 := sipauth.ComputeHA1("1001", "example.invalid", "test-password")
	if _, err := db.ExecContext(ctx, `
INSERT INTO sip_credentials (tenant_id, username, realm, ha1, max_bindings)
VALUES ('tenant-1', '1001', 'example.invalid', ?, 2)`, ha1); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
	credential, err := store.LookupCredential(ctx, "example.invalid", "1001")
	if err != nil || credential.HA1 != ha1 {
		t.Fatalf("LookupCredential() = %+v, %v", credential, err)
	}

	manager, err := registration.NewManager(store, registration.SystemClock{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	binding, err := manager.Apply(ctx, registration.Request{
		TenantID: "tenant-1", AoR: "sip:1001@example.invalid", Contact: "sip:1001@device.invalid",
		RouteTarget: "127.0.0.1:15061", Transport: registration.TransportUDP,
		Received: netip.MustParseAddrPort("127.0.0.1:15061"), Q: 1,
		Expires: time.Minute, MaxBindings: 2,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	resolved, err := manager.Resolve(ctx, "tenant-1", "sip:1001@example.invalid")
	if err != nil || resolved.ID != binding.ID {
		t.Fatalf("Resolve() = %+v, %v", resolved, err)
	}

	now := time.Now().UTC()
	events := []call.Event{
		{Sequence: 1, TenantID: "tenant-1", CallID: "01900000-0000-7000-8000-000000000001", NodeID: "node-1",
			CallerLeg: "01900000-0000-7000-8000-000000000002", CalleeLeg: "01900000-0000-7000-8000-000000000003",
			Direction: call.DirectionInternal, NewState: call.StateNew, ProtocolEvent: "created", OccurredAt: now},
		{Sequence: 2, TenantID: "tenant-1", CallID: "01900000-0000-7000-8000-000000000001", NodeID: "node-1",
			CallerLeg: "01900000-0000-7000-8000-000000000002", CalleeLeg: "01900000-0000-7000-8000-000000000003",
			Direction: call.DirectionInternal, OldState: call.StateNew, NewState: call.StateFailed,
			Reason: "callee not registered", ProtocolEvent: "404", OccurredAt: now.Add(time.Millisecond)},
	}
	for _, event := range events {
		if err := store.WriteCallEvent(ctx, event); err != nil {
			t.Fatalf("WriteCallEvent() error = %v", err)
		}
	}
	var state string
	var eventCount int
	if err := db.QueryRowContext(ctx, `SELECT state FROM calls WHERE call_id = ?`, events[0].CallID).Scan(&state); err != nil {
		t.Fatalf("query CDR: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM call_events WHERE call_id = ?`, events[0].CallID).Scan(&eventCount); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if state != string(call.StateFailed) || eventCount != 2 {
		t.Fatalf("state = %q, event count = %d", state, eventCount)
	}
	summary := mediasession.Summary{
		MediaSessionID: "01900000-0000-7000-8000-000000000004", TenantID: "tenant-1",
		CallID: string(events[0].CallID), NodeID: "node-1", CallerLegID: string(events[0].CallerLeg), CalleeLegID: string(events[0].CalleeLeg),
		SampledAt: now, Caller: mediasession.LegSummary{Inbound: mediartp.Stats{Packets: 10, Bytes: 1600, Lost: 1}},
		Callee: mediasession.LegSummary{Inbound: mediartp.Stats{Packets: 11, Bytes: 1760}}, OneWay: false,
	}
	if err := store.WriteMediaSummary(ctx, summary); err != nil {
		t.Fatalf("WriteMediaSummary() error = %v", err)
	}
	var mediaSampleCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_quality_samples WHERE call_id = ?`, events[0].CallID).Scan(&mediaSampleCount); err != nil {
		t.Fatalf("query media samples: %v", err)
	}
	if mediaSampleCount != 1 {
		t.Fatalf("media sample count = %d", mediaSampleCount)
	}
}
