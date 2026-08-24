DROP INDEX probe_result_raw_effective_idx;
DROP INDEX probe_targets_node_created_page_idx;
DROP INDEX probe_targets_created_page_idx;

ALTER TABLE probe_result_1h
    DROP CONSTRAINT probe_result_1h_http_error_count_valid,
    DROP COLUMN http_error_count;

ALTER TABLE probe_result_5m
    DROP CONSTRAINT probe_result_5m_http_error_count_valid,
    DROP COLUMN http_error_count;

ALTER TABLE node_agent_settings
    DROP CONSTRAINT node_agent_settings_queue_covers_report;

ALTER TABLE probe_targets
    DROP CONSTRAINT probe_targets_type_valid;

ALTER TABLE probe_targets
    ADD CONSTRAINT probe_targets_type_valid
    CHECK (probe_type IN ('icmp', 'tcp', 'http', 'https'));
