ALTER TABLE users
    DROP CONSTRAINT users_role_valid;

ALTER TABLE users
    ADD CONSTRAINT users_role_valid CHECK (role IN ('viewer', 'admin'));

COMMENT ON COLUMN users.role IS NULL;

-- This rollback restores only the former constraint. Viewer users and their
-- sessions deleted by the up migration are intentionally not recoverable.
