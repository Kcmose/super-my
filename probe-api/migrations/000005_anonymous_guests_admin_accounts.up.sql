-- Guests are anonymous allowlisted readers, not user records. Remove every
-- legacy viewer session explicitly before deleting the corresponding users.
DELETE FROM sessions
WHERE user_id IN (SELECT id FROM users WHERE role = 'viewer');

DELETE FROM users
WHERE role = 'viewer';

ALTER TABLE users
    DROP CONSTRAINT users_role_valid;

ALTER TABLE users
    ADD CONSTRAINT users_role_valid CHECK (role = 'admin');

COMMENT ON COLUMN users.role IS
    'Account role; V1 accounts are administrators only. Guests are anonymous and have no user row or session.';
