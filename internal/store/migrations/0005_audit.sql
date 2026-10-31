-- One row per command attempt. detail holds only masked/previewed values (see package audit);
-- command_id links an SMS row to its later delivery outcome.
CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    at         INTEGER NOT NULL, -- unix seconds
    user       TEXT NOT NULL,
    device     TEXT NOT NULL,
    action     TEXT NOT NULL,
    status     TEXT NOT NULL,
    detail     TEXT NOT NULL DEFAULT '',
    command_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at ON audit_log(at);
CREATE INDEX audit_log_command_id ON audit_log(command_id);
