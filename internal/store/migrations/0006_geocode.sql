-- Reverse-geocoding cache, keyed by "lat,lon" rounded to 3 decimals (~110 m). An empty name
-- records "the provider has no name here", so such places cost no further calls.
CREATE TABLE geocode_cache (
    key        TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at INTEGER NOT NULL -- unix seconds
);

-- One row per UTC day: provider calls made, cache hits, and the last X-RateLimit-Remaining seen.
CREATE TABLE geocode_usage (
    day            TEXT PRIMARY KEY, -- UTC date, YYYY-MM-DD
    calls          INTEGER NOT NULL DEFAULT 0,
    cache_hits     INTEGER NOT NULL DEFAULT 0,
    last_remaining INTEGER
);

-- The resolved place name of a device and the coordinates it was resolved for.
ALTER TABLE devices ADD COLUMN location_name TEXT;
ALTER TABLE devices ADD COLUMN location_name_at INTEGER; -- unix seconds
ALTER TABLE devices ADD COLUMN geocoded_lat REAL;
ALTER TABLE devices ADD COLUMN geocoded_lon REAL;
