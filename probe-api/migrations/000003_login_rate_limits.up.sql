CREATE TABLE login_rate_limits (
    scope TEXT NOT NULL,
    key_hash BYTEA NOT NULL,
    window_started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    attempt_count INTEGER NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (scope, key_hash),
    CONSTRAINT login_rate_limits_scope_valid CHECK (scope IN ('source_ip', 'username')),
    CONSTRAINT login_rate_limits_key_hash_length CHECK (octet_length(key_hash) = 32),
    CONSTRAINT login_rate_limits_attempt_positive CHECK (attempt_count > 0),
    CONSTRAINT login_rate_limits_updated_after_window CHECK (updated_at >= window_started_at)
);

CREATE INDEX login_rate_limits_updated_idx
    ON login_rate_limits (updated_at);
