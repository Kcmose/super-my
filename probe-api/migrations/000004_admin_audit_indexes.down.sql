DROP INDEX IF EXISTS users_created_page_idx;
DROP INDEX IF EXISTS audit_logs_action_occurred_id_idx;
DROP INDEX IF EXISTS audit_logs_occurred_id_idx;

-- The Stage 4 foreign key cannot represent a deleted actor snapshot. Rolling
-- back across such rows is therefore intentionally lossy for actor_user_id;
-- actor_username remains available. Reapplying 000004 cannot reconstruct IDs.
UPDATE audit_logs AS audit
SET actor_user_id = NULL
WHERE actor_user_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM users WHERE users.id = audit.actor_user_id);

COMMENT ON COLUMN audit_logs.actor_user_id IS NULL;

ALTER TABLE audit_logs
    ADD CONSTRAINT audit_logs_actor_user_id_fkey
    FOREIGN KEY (actor_user_id) REFERENCES users(id) ON DELETE SET NULL;
