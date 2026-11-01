-- auth_gen is bumped on every account-security change (password change or reset, two-factor
-- enable/disable, backup-code regeneration). A pending second-factor challenge records the
-- value it was issued under and is void once it differs.
ALTER TABLE users ADD COLUMN auth_gen INTEGER NOT NULL DEFAULT 0;
