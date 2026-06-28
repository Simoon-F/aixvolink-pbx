CREATE TABLE IF NOT EXISTS sip_credentials (
    tenant_id VARCHAR(64) NOT NULL,
    username VARCHAR(128) NOT NULL,
    realm VARCHAR(255) NOT NULL,
    ha1 CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    max_bindings SMALLINT UNSIGNED NOT NULL DEFAULT 4,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (realm, username),
    KEY idx_sip_credentials_tenant (tenant_id, username)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS registrations (
    registration_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    aor VARCHAR(512) NOT NULL,
    contact VARCHAR(1024) NOT NULL,
    contact_hash BINARY(32) NOT NULL,
    route_target VARCHAR(255) NOT NULL,
    transport VARCHAR(8) CHARACTER SET ascii NOT NULL,
    received_ip VARCHAR(45) CHARACTER SET ascii NOT NULL,
    received_port SMALLINT UNSIGNED NOT NULL,
    user_agent VARCHAR(255) NOT NULL DEFAULT '',
    q DECIMAL(4,3) NOT NULL DEFAULT 1.000,
    expires_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (registration_id),
    UNIQUE KEY uq_registrations_contact (tenant_id, aor, contact_hash),
    KEY idx_registrations_resolve (tenant_id, aor, expires_at, q)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS calls (
    call_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    node_id VARCHAR(128) NOT NULL,
    direction VARCHAR(32) CHARACTER SET ascii NOT NULL,
    caller_leg_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    callee_leg_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    state VARCHAR(32) CHARACTER SET ascii NOT NULL,
    started_at DATETIME(6) NOT NULL,
    answered_at DATETIME(6) NULL,
    ended_at DATETIME(6) NULL,
    cause VARCHAR(255) NOT NULL DEFAULT '',
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (call_id),
    KEY idx_calls_tenant_started (tenant_id, started_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS call_events (
    call_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    sequence BIGINT UNSIGNED NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    leg_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    node_id VARCHAR(128) NOT NULL,
    old_state VARCHAR(32) CHARACTER SET ascii NOT NULL DEFAULT '',
    new_state VARCHAR(32) CHARACTER SET ascii NOT NULL,
    reason VARCHAR(255) NOT NULL DEFAULT '',
    protocol_event VARCHAR(64) NOT NULL DEFAULT '',
    occurred_at DATETIME(6) NOT NULL,
    PRIMARY KEY (call_id, sequence),
    KEY idx_call_events_tenant_time (tenant_id, occurred_at),
    CONSTRAINT fk_call_events_call FOREIGN KEY (call_id) REFERENCES calls(call_id)
) ENGINE=InnoDB;
