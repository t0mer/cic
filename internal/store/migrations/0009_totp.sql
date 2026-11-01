-- Optional TOTP two-factor login. totp_secret is AES-GCM encrypted (context totp:<user id>);
-- it is set while setup is pending and totp_enabled = 0, and active once enabled.
-- totp_last_step is the highest accepted time step (replay guard).
ALTER TABLE users ADD COLUMN totp_secret TEXT;
ALTER TABLE users ADD COLUMN totp_enabled INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;

-- code_hash is the hex SHA-256 of the normalised backup code; the code itself is never stored.
CREATE TABLE backup_codes (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,
    used_at   INTEGER,
    UNIQUE (user_id, code_hash)
);
CREATE INDEX backup_codes_user_id ON backup_codes(user_id);
