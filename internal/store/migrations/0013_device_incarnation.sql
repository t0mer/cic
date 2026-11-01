-- Durable identity of a device row: fresh at every creation, so location fixes of a deleted
-- device can never be stored against a later device with the same key. Existing rows keep 0.
ALTER TABLE devices ADD COLUMN incarnation INTEGER NOT NULL DEFAULT 0;
