-- End of a 429 backoff (unix seconds), so a restart does not resume calling during the block.
ALTER TABLE geocode_usage ADD COLUMN blocked_until INTEGER;
