-- location_points is the recorded location history of each device, keyed like devices.key.
-- ts is the phone's own fix time (unix seconds). There is no foreign key: Registry.Delete
-- removes a device's points in the same transaction as the device row.
CREATE TABLE location_points (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    device_key TEXT NOT NULL,
    ts         INTEGER NOT NULL,
    lat        REAL NOT NULL,
    lon        REAL NOT NULL,
    accuracy   REAL NULL
);
-- Unique so a replayed fix (same device, same time) is ignored.
CREATE UNIQUE INDEX idx_location_points_device_ts ON location_points(device_key, ts);
