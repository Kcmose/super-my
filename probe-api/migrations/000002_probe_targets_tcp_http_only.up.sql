-- Stage 4 initially enables only TCP and HTTP(S) probes.
-- ICMP remains a planned protocol value but is fail-closed until its Agent
-- implementation and capability boundary are delivered in a later migration.

ALTER TABLE probe_targets
    DROP CONSTRAINT probe_targets_type_valid;

ALTER TABLE probe_targets
    ADD CONSTRAINT probe_targets_type_valid
    CHECK (probe_type IN ('tcp', 'http', 'https'));

ALTER TABLE node_agent_settings
    ADD CONSTRAINT node_agent_settings_queue_covers_report
    CHECK (max_memory_queue_seconds >= report_interval_seconds);

ALTER TABLE probe_result_5m
    ADD COLUMN http_error_count BIGINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT probe_result_5m_http_error_count_valid
    CHECK (http_error_count >= 0 AND http_error_count <= received_count);

ALTER TABLE probe_result_1h
    ADD COLUMN http_error_count BIGINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT probe_result_1h_http_error_count_valid
    CHECK (http_error_count >= 0 AND http_error_count <= received_count);

CREATE INDEX probe_targets_created_page_idx
    ON probe_targets (created_at DESC, id DESC);

CREATE INDEX probe_targets_node_created_page_idx
    ON probe_targets (node_id, created_at DESC, id DESC);

CREATE INDEX probe_result_raw_effective_idx
    ON probe_result_raw (effective_at);

COMMENT ON CONSTRAINT probe_targets_type_valid ON probe_targets IS
    'Stage 4 enabled probe types. ICMP is intentionally unavailable until a later migration.';

COMMENT ON CONSTRAINT node_agent_settings_queue_covers_report ON node_agent_settings IS
    'The memory queue must survive at least one configured report interval.';

COMMENT ON COLUMN probe_result_5m.http_error_count IS
    'Received HTTP responses with a status below 200 or at least 400.';

COMMENT ON COLUMN probe_result_1h.http_error_count IS
    'Received HTTP responses with a status below 200 or at least 400.';
