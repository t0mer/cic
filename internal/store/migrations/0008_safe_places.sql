-- Named circular zones ("safe places"); a device inside one is shown by its name.
CREATE TABLE safe_places (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
  latitude   REAL    NOT NULL,
  longitude  REAL    NOT NULL,
  radius_m   INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
