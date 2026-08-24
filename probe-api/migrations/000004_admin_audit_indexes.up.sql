-- Preserve the actor UUID as an immutable snapshot when a managed user is
-- deleted. actor_username already remains a text snapshot.
ALTER TABLE audit_logs
    DROP CONSTRAINT IF EXISTS audit_logs_actor_user_id_fkey;

CREATE INDEX audit_logs_occurred_id_idx
    ON audit_logs (occurred_at DESC, id DESC);

CREATE INDEX audit_logs_action_occurred_id_idx
    ON audit_logs (action, occurred_at DESC, id DESC);

CREATE INDEX users_created_page_idx
    ON users (created_at DESC, id DESC);

COMMENT ON COLUMN audit_logs.actor_user_id IS
    'Immutable actor UUID snapshot; intentionally not a foreign key after Stage 5.';
