// Package mysql implements durable stores with database/sql and MySQL 8.
package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
	mysqldriver "github.com/go-sql-driver/mysql"
)

// Store implements credential, registration, CDR, and call-event persistence.
type Store struct {
	db *sql.DB
}

// Open validates a DSN, connects, and verifies MySQL availability.
func Open(ctx context.Context, dsn string) (*Store, error) {
	config, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse MySQL DSN: %w", err)
	}
	config.ParseTime = true
	config.Loc = time.UTC
	db, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open MySQL: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping MySQL: %w", err)
	}
	return &Store{db: db}, nil
}

// NewStore wraps an existing database connection for integration tests.
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	return &Store{db: db}, nil
}

// Close releases the database pool.
func (s *Store) Close() error { return s.db.Close() }

// LookupCredential resolves one HA1 credential.
func (s *Store) LookupCredential(ctx context.Context, realm, username string) (sipauth.Credential, error) {
	var credential sipauth.Credential
	err := s.db.QueryRowContext(ctx, `
SELECT tenant_id, username, realm, ha1, max_bindings
FROM sip_credentials
WHERE realm = ? AND username = ?`, realm, username).Scan(
		&credential.TenantID, &credential.Username, &credential.Realm, &credential.HA1, &credential.MaxBindings,
	)
	if err != nil {
		return sipauth.Credential{}, fmt.Errorf("query credential: %w", err)
	}
	return credential, nil
}

// Upsert atomically applies a binding limit and stores a registration.
func (s *Store) Upsert(ctx context.Context, binding registration.Binding, maxBindings int) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin registration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
SELECT contact
FROM registrations
WHERE tenant_id = ? AND aor = ? AND expires_at > ?
FOR UPDATE`, binding.TenantID, binding.AoR, binding.UpdatedAt)
	if err != nil {
		return fmt.Errorf("lock registration bindings: %w", err)
	}
	activeCount := 0
	contactExists := false
	for rows.Next() {
		var contact string
		if err := rows.Scan(&contact); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan locked registration: %w", err)
		}
		activeCount++
		contactExists = contactExists || contact == binding.Contact
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close registration rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate registration rows: %w", err)
	}
	if !contactExists && activeCount >= maxBindings {
		return registration.ErrBindingLimit
	}

	contactHash := sha256.Sum256([]byte(binding.Contact))
	_, err = tx.ExecContext(ctx, `
INSERT INTO registrations (
    registration_id, tenant_id, aor, contact, contact_hash, route_target, transport,
    received_ip, received_port, user_agent, q, expires_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    registration_id = VALUES(registration_id),
    route_target = VALUES(route_target),
    transport = VALUES(transport),
    received_ip = VALUES(received_ip),
    received_port = VALUES(received_port),
    user_agent = VALUES(user_agent),
    q = VALUES(q),
    expires_at = VALUES(expires_at),
    updated_at = VALUES(updated_at)`,
		binding.ID, binding.TenantID, binding.AoR, binding.Contact, contactHash[:], binding.RouteTarget, binding.Transport,
		binding.Received.Addr().String(), binding.Received.Port(), binding.UserAgent, binding.Q, binding.ExpiresAt, binding.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert registration: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit registration transaction: %w", err)
	}
	return nil
}

// Delete removes one contact binding.
func (s *Store) Delete(ctx context.Context, tenantID registration.TenantID, aor registration.AoR, contact string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM registrations WHERE tenant_id = ? AND aor = ? AND contact = ?`, tenantID, aor, contact)
	if err != nil {
		return fmt.Errorf("delete registration: %w", err)
	}
	return nil
}

// DeleteAll removes every binding for one AoR.
func (s *Store) DeleteAll(ctx context.Context, tenantID registration.TenantID, aor registration.AoR) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM registrations WHERE tenant_id = ? AND aor = ?`, tenantID, aor)
	if err != nil {
		return fmt.Errorf("delete all registrations: %w", err)
	}
	return nil
}

// ListActive returns active bindings for routing.
func (s *Store) ListActive(ctx context.Context, tenantID registration.TenantID, aor registration.AoR, now time.Time) ([]registration.Binding, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT registration_id, tenant_id, aor, contact, route_target, transport,
       received_ip, received_port, user_agent, q, expires_at, updated_at
FROM registrations
WHERE tenant_id = ? AND aor = ? AND expires_at > ?`, tenantID, aor, now)
	if err != nil {
		return nil, fmt.Errorf("query active registrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	bindings := make([]registration.Binding, 0)
	for rows.Next() {
		var binding registration.Binding
		var receivedIP string
		var receivedPort uint16
		if err := rows.Scan(
			&binding.ID, &binding.TenantID, &binding.AoR, &binding.Contact, &binding.RouteTarget, &binding.Transport,
			&receivedIP, &receivedPort, &binding.UserAgent, &binding.Q, &binding.ExpiresAt, &binding.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan active registration: %w", err)
		}
		address, err := netip.ParseAddr(receivedIP)
		if err != nil {
			return nil, fmt.Errorf("parse stored received IP: %w", err)
		}
		binding.Received = netip.AddrPortFrom(address, receivedPort)
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active registrations: %w", err)
	}
	return bindings, nil
}

// DeleteExpired removes expired bindings.
func (s *Store) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM registrations WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired registrations: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read deleted registration count: %w", err)
	}
	return deleted, nil
}

// WriteCallEvent persists an ordered event and updates the CDR summary atomically.
func (s *Store) WriteCallEvent(ctx context.Context, event call.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin call event transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var answeredAt, endedAt sql.NullTime
	if event.NewState == call.StateAnswered {
		answeredAt = sql.NullTime{Time: event.OccurredAt, Valid: true}
	}
	if event.NewState == call.StateTerminated || event.NewState == call.StateFailed {
		endedAt = sql.NullTime{Time: event.OccurredAt, Valid: true}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO calls (
    call_id, tenant_id, node_id, direction, caller_leg_id, callee_leg_id,
    state, started_at, answered_at, ended_at, cause, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
    state = VALUES(state),
    answered_at = COALESCE(calls.answered_at, VALUES(answered_at)),
    ended_at = COALESCE(VALUES(ended_at), calls.ended_at),
    cause = IF(VALUES(cause) = '', calls.cause, VALUES(cause)),
    updated_at = VALUES(updated_at)`,
		event.CallID, event.TenantID, event.NodeID, event.Direction, event.CallerLeg, event.CalleeLeg,
		event.NewState, event.OccurredAt, answeredAt, endedAt, event.Reason, event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("upsert call CDR: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO call_events (
    call_id, sequence, tenant_id, leg_id, node_id, old_state, new_state,
    reason, protocol_event, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.CallID, event.Sequence, event.TenantID, event.LegID, event.NodeID, event.OldState, event.NewState,
		event.Reason, event.ProtocolEvent, event.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("insert call event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit call event transaction: %w", err)
	}
	return nil
}

// IsNotFound reports a missing SQL row.
func IsNotFound(err error) bool { return errors.Is(err, sql.ErrNoRows) }
