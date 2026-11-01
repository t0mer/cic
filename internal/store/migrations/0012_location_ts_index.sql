-- The retention purge deletes by time across all devices ("WHERE ts < ?"); without this index
-- that is a full table scan under the write lock.
CREATE INDEX idx_location_points_ts ON location_points(ts);
