# CIC

[![CI](https://github.com/t0mer/cic/actions/workflows/ci.yml/badge.svg)](https://github.com/t0mer/cic/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/t0mer/cic)](LICENSE)
[![Release](https://img.shields.io/github/v/release/t0mer/cic)](https://github.com/t0mer/cic/releases)
[![Docker Hub](https://img.shields.io/docker/v/techblog/cic?label=docker%20hub)](https://hub.docker.com/r/techblog/cic)

CIC is the control plane for [DRADIS](https://github.com/t0mer/dradis), the Android app that reports phone telemetry over MQTT. CIC subscribes to the same MQTT broker as your phones, keeps the latest state of every device in SQLite, and serves a web UI and a REST API. From the UI an admin can see where every phone is, how charged it is and whether it is online, and can send commands back to a phone: ring it, show a notification, send an SMS, or ask for a fresh status or location.

It ships as a single static Go binary (pure Go, no CGO) with the React web UI embedded, and as a multi-arch Docker image (`techblog/cic`).

## Table of contents

- [Features](#features)
- [Screenshots](#screenshots)
- [Architecture](#architecture)
- [Requirements](#requirements)
- [Installation](#installation)
- [Configuration](#configuration)
- [MQTT contract](#mqtt-contract)
- [Usage](#usage)
- [API](#api)
- [Security notes](#security-notes)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Features

- Live device cards and a map (MapLibre GL) updated over Server-Sent Events: battery and charging state, Wi-Fi, location, screen-lock state, app version, last seen.
- Online/offline detection based on live traffic only (see [Liveness](#liveness)).
- Remote commands for admins: ping (ring) and stop, notification, SMS, refresh status, refresh location. Every attempt is written to an audit log; the audit write happens before the command is published, so a failed audit sends nothing.
- SMS safety limits: per-user rate limits, a per-recipient hourly limit and a daily cap on billed SMS segments.
- Reverse geocoding of device positions through the LatLng API (optional), with a local cache and a daily quota guard.
- Safe places: named circles on the map; a device inside one shows that name.
- Location history and per-day routes (admin only) with automatic thinning and retention.
- Local users with `admin` and `viewer` roles, argon2id password hashes, server-side sessions, login throttling and optional TOTP two-factor authentication with backup codes.
- User management from the UI or the `cic user` CLI.
- Light and dark themes, responsive layout.
- OpenAPI 3 spec served at `/api/openapi.yaml`.

## Screenshots

All screenshots use fictional demo data.

### Login

| Light | Dark |
|---|---|
| ![Login, light](assets/screenshots/login-light.png) | ![Login, dark](assets/screenshots/login-dark.png) |

### Login with two-factor code

| Light | Dark |
|---|---|
| ![2FA login, light](assets/screenshots/login-2fa-light.png) | ![2FA login, dark](assets/screenshots/login-2fa-dark.png) |

### Device cards

| Light | Dark | Mobile |
|---|---|---|
| ![Device cards, light](assets/screenshots/cards-light.png) | ![Device cards, dark](assets/screenshots/cards-dark.png) | ![Device cards, mobile](assets/screenshots/cards-mobile.png) |

### Map

| Light | Dark | Mobile |
|---|---|---|
| ![Map, light](assets/screenshots/map-light.png) | ![Map, dark](assets/screenshots/map-dark.png) | ![Map, mobile](assets/screenshots/map-mobile.png) |

### Phone actions (admin)

| Light | Dark |
|---|---|
| ![Phone actions, light](assets/screenshots/actions-light.png) | ![Phone actions, dark](assets/screenshots/actions-dark.png) |

### Location history and routes (admin)

| Light | Dark |
|---|---|
| ![History, light](assets/screenshots/history-light.png) | ![History, dark](assets/screenshots/history-dark.png) |

### Settings: devices

| Light | Dark |
|---|---|
| ![Settings devices, light](assets/screenshots/settings-devices-light.png) | ![Settings devices, dark](assets/screenshots/settings-devices-dark.png) |

### Settings: safe places

| Light | Dark |
|---|---|
| ![Settings safe places, light](assets/screenshots/settings-places-light.png) | ![Settings safe places, dark](assets/screenshots/settings-places-dark.png) |

### Settings: geocoding

| Light | Dark |
|---|---|
| ![Settings geocoding, light](assets/screenshots/settings-geocoding-light.png) | ![Settings geocoding, dark](assets/screenshots/settings-geocoding-dark.png) |

### Settings: users

| Light | Dark |
|---|---|
| ![Settings users, light](assets/screenshots/settings-users-light.png) | ![Settings users, dark](assets/screenshots/settings-users-dark.png) |

### Settings: account and two-factor authentication

| Light | Dark |
|---|---|
| ![Settings account 2FA, light](assets/screenshots/settings-account-2fa-light.png) | ![Settings account 2FA, dark](assets/screenshots/settings-account-2fa-dark.png) |

## Architecture

```mermaid
flowchart LR
    P1[DRADIS phone] -- MQTT --> B[(MQTT broker)]
    P2[DRADIS phone] -- MQTT --> B
    B -- telemetry, QoS 1 --> C[CIC]
    C -- commands, QoS 1, not retained --> B
    C <--> D[(SQLite /data/cic.db)]
    C -- REST + SSE --> W[Browser UI]
    C -. reverse geocoding .-> G[LatLng API]
```

CIC holds one MQTT connection (MQTT 3.1.1, clean session, client ID `cic-<16 hex>` generated once and stored in the database). Incoming messages update an in-memory device registry that is persisted to SQLite; changes are pushed to browsers over a Server-Sent Events stream (`GET /api/events`). Commands from the UI go through validation, rate limiting and an audit write before they are published to the broker.

## Requirements

- An MQTT broker reachable from CIC (the same broker your DRADIS phones publish to). CIC works without one, but then it only shows stored data and every command answers `503 not_connected`.
- Optional: a LatLng **server** API key (starts with `latlng_`) for reverse geocoding.
- Optional: a LatLng **public** map key (starts with `pk_`) if you want LatLng map tiles instead of the default OpenFreeMap.
- To build from source: Go (see `go.mod`) and Node.js 24 for the web UI.

## Installation

### Docker

The image is `techblog/cic` (tags `latest` and `YYYY.M.PATCH`; platforms `linux/amd64`, `linux/arm64`, `linux/arm/v7`). It runs as a non-root user (65532), listens on `8080`, and keeps its database and encryption key in the `/data` volume. Use a named volume so the directory is writable.

```bash
docker run -d --name cic \
  -p 8080:8080 \
  -v cic-data:/data \
  -e CIC_MQTT_BROKER_HOST=mqtt.example.local \
  -e CIC_MQTT_BROKER_USERNAME=cic \
  -e CIC_MQTT_BROKER_PASSWORD=change-me \
  -e CIC_SERVER_BASE_URL=https://cic.example.local \
  techblog/cic:latest
```

On the first start CIC creates the `admin` user and prints a one-time random password to **stderr**; read it with `docker logs cic`. See [First login](#first-login).

The image has a built-in `HEALTHCHECK` (`/cic healthcheck`). It does not ship a `.env` file; pass settings with `-e`, `--env-file`, or a mounted YAML file plus `CIC_CONFIG`.

Back up the whole `/data` volume: it contains `cic.db` and `secret.key` (see [Secret key](#secret-key)).

### Release binaries

Each GitHub release attaches static binaries named `cic_<version>_<os>_<arch>` for Linux only: `amd64`, `arm64` and `armv7`. There are no macOS or Windows builds; build from source on those systems.

```bash
chmod +x cic_<version>_linux_amd64
./cic_<version>_linux_amd64 serve --db-path ./cic.db --insecure-cookies   # plain-HTTP local trial only
```

### Build from source

```bash
git clone https://github.com/t0mer/cic.git && cd cic
(cd web && npm ci && npm run build)      # writes the UI into internal/webui/dist
go build -o cic ./cmd/cic
```

`scripts/build.sh` cross-compiles the three Linux targets into `dist/` (`VERSION=2026.11.0 scripts/build.sh`); it does not build the web UI, so run the `npm` step first. Without the UI build the Go code still compiles, but the binary serves an empty UI.

## Configuration

Precedence, highest first: **command-line flags > environment variables > YAML file > built-in defaults.**

- Environment variables use the prefix `CIC_`, with `.` in the key replaced by `_` (`mqtt.broker.host` becomes `CIC_MQTT_BROKER_HOST`). List values are comma separated.
- The YAML file is optional. Point to it with `--config <path>` or `CIC_CONFIG`. Unknown keys are rejected at startup. A commented template is in [`config.example.yaml`](config.example.yaml).
- Durations use Go syntax (`5m`, `24h`, `2160h`).
- `cic serve` and every `cic user ...` command also load a `.env` file from the **current working directory** if one exists. Real environment variables win over `.env`. `cic healthcheck` and `cic version` do not read it.

### Settings

| YAML key | Environment variable | Flag | Default | Description |
|---|---|---|---|---|
| `server.listen` | `CIC_SERVER_LISTEN` | `--listen` | `:8080` | Listen address. |
| `server.base_url` | `CIC_SERVER_BASE_URL` | | empty | Public URL. An `https://` value enables HSTS and makes the Origin check expect https. |
| `server.insecure_cookies` | `CIC_SERVER_INSECURE_COOKIES` | `--insecure-cookies` | `false` | Drops the `Secure` flag and the `__Host-` cookie prefix. Plain-HTTP development only. |
| `server.trusted_proxies` | `CIC_SERVER_TRUSTED_PROXIES` | | empty | IPs or CIDRs whose `X-Forwarded-For` / `X-Forwarded-Proto` are trusted. |
| `storage.path` | `CIC_STORAGE_PATH` | `--db-path` | `/data/cic.db` | SQLite database file. |
| `mqtt.prefixes` | `CIC_MQTT_PREFIXES` | | `dradis` | Topic prefixes to subscribe to; each matches `[A-Za-z0-9_-]{1,64}`. |
| `mqtt.broker.host` | `CIC_MQTT_BROKER_HOST` | | empty | Broker host. Empty disables MQTT. |
| `mqtt.broker.port` | `CIC_MQTT_BROKER_PORT` | | `1883` | Broker port. |
| `mqtt.broker.tls` | `CIC_MQTT_BROKER_TLS` | | `false` | Use TLS (1.2 or newer). |
| `mqtt.broker.ca_file` | `CIC_MQTT_BROKER_CA_FILE` | | empty | PEM CA bundle for the broker; the file must exist when TLS is on. |
| `mqtt.broker.insecure_skip_verify` | `CIC_MQTT_BROKER_INSECURE_SKIP_VERIFY` | | `false` | Skip broker certificate verification (a warning is logged). |
| `mqtt.broker.username` | `CIC_MQTT_BROKER_USERNAME` | | empty | Broker user name. |
| `mqtt.broker.password` | `CIC_MQTT_BROKER_PASSWORD` | | empty | Broker password (redacted in logs). |
| `devices.offline_after` | `CIC_DEVICES_OFFLINE_AFTER` | | `5m` | A device with no live message for this long is offline. |
| `devices.max_devices` | `CIC_DEVICES_MAX_DEVICES` | | `500` | Cap on auto-discovered devices; `0` is unlimited. |
| `commands.ping_seconds` | `CIC_COMMANDS_PING_SECONDS` | | `3` | Default ring duration (1-600). |
| `commands.sms_daily_cap` | `CIC_COMMANDS_SMS_DAILY_CAP` | | `50` | Billed SMS segments per UTC day; `0` disables SMS. |
| `geocode.enabled` | `CIC_GEOCODE_ENABLED` | | `true` | Reverse geocoding; only active with a key. |
| `geocode.api_key` | `CIC_GEOCODE_API_KEY` | | empty | LatLng **server** key (`latlng_...`). A `pk_` key disables geocoding with an error. |
| `geocode.min_move_meters` | `CIC_GEOCODE_MIN_MOVE_METERS` | | `150` | Re-geocode after the device moved this far. |
| `geocode.max_age` | `CIC_GEOCODE_MAX_AGE` | | `24h` | Re-geocode when the stored name is older than this. |
| `geocode.daily_cap` | `CIC_GEOCODE_DAILY_CAP` | | `250` | Local limit on provider calls per UTC day. |
| `geocode.reserve` | `CIC_GEOCODE_RESERVE` | | `20` | Stop while the provider reports fewer calls remaining; must not exceed `daily_cap`. |
| `map.provider` | `CIC_MAP_PROVIDER` | | `openfreemap` | `openfreemap` or `latlng`. |
| `map.latlng_maps_key` | `CIC_MAP_LATLNG_MAPS_KEY` | | empty | LatLng **public** map key (`pk_...`). |
| `map.style_light` | `CIC_MAP_STYLE_LIGHT` | | empty | MapLibre style URL override (https). Required with provider `latlng`. |
| `map.style_dark` | `CIC_MAP_STYLE_DARK` | | empty | Same, for dark mode. |
| `map.extra_origins` | `CIC_MAP_EXTRA_ORIGINS` | | empty | Extra https origins allowed in the Content-Security-Policy for map assets. |
| `audit.retention` | `CIC_AUDIT_RETENTION` | | `2160h` | Audit log retention (90 days). |
| `locations.enabled` | `CIC_LOCATIONS_ENABLED` | | `true` | Record location history. |
| `locations.retention` | `CIC_LOCATIONS_RETENTION` | | `2160h` | History retention (90 days). |
| `locations.min_distance_m` | `CIC_LOCATIONS_MIN_DISTANCE_M` | | `25` | Thinning: minimum distance from the last stored point. |
| `locations.min_interval` | `CIC_LOCATIONS_MIN_INTERVAL` | | `5m` | Thinning: minimum time since the last stored point. |
| `locations.max_points_per_device` | `CIC_LOCATIONS_MAX_POINTS_PER_DEVICE` | | `200000` | Per-device cap, oldest deleted first; `0` is no cap. |
| `log.level` | `CIC_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn`, `error`. |
| `log.format` | `CIC_LOG_FORMAT` | `--log-format` | `text` | `text` or `json`. |
| (file path) | `CIC_CONFIG` | `--config` | none | Path of the YAML file. |

The config also contains `auth.oidc.*` keys. OIDC login is **not implemented**; the keys are accepted and validated but have no effect.

Two more environment variables are not config keys: `CIC_ADMIN_PASSWORD` (see [First login](#first-login)) and `CIC_SECRET_KEY` (below).

### Secret key

TOTP secrets are encrypted at rest with AES-256-GCM. The key comes from, in order:

1. `CIC_SECRET_KEY`: 32 bytes as 64 hex characters or base64. If set it wins, and an invalid value stops startup.
2. A file `secret.key` in the **same directory as the database** (`/data/secret.key` in Docker). It is created on first start with random bytes, mode `0600`.

**Back up `secret.key` together with the database.** Without the original key, encrypted TOTP secrets cannot be read and 2FA logins fail. If neither source can be used (for example an unwritable data directory), 2FA is disabled and its endpoints answer 503, unless some account already has 2FA enabled: then `cic serve` refuses to start until the key is restored.

### CLI

| Command | Purpose |
|---|---|
| `cic serve` | Run the server (flags `--listen`, `--db-path`, `--insecure-cookies`). Shuts down gracefully on SIGINT/SIGTERM. |
| `cic version`, `cic --version` | Print the version. |
| `cic healthcheck` | Request `/healthz` on the local listen address; exit 1 on failure. Used by the Docker `HEALTHCHECK`. |
| `cic user add <name> [--admin] [--password-stdin]` | Create a user (viewer unless `--admin`). Prompts for the password, or reads one line from stdin. |
| `cic user reset-password <name> [--password-stdin]` | Set a new password; forces a change at next login and ends the user's sessions. |
| `cic user list` | List users, roles, disabled state and last login. |
| `cic user disable <name>` / `enable <name>` | Disable (also ends sessions) or re-enable a user. |
| `cic user disable-2fa <name>` | Turn off 2FA, delete backup codes and end all sessions. |

The `user` commands open the SQLite file directly (`--db-path`, default `/data/cic.db`), so they work while the server is running. In Docker: `docker exec -it cic /cic user list`. User names match `[A-Za-z0-9._-]{3,64}`; passwords need at least 12 characters.

## MQTT contract

CIC talks to DRADIS devices with topics of the form `<prefix>/<device>/<leaf>`, where the prefix is one of `mqtt.prefixes` (default `dradis`) and the device segment matches `[A-Za-z0-9_-]{1,64}`. Devices are discovered automatically from their traffic (up to `devices.max_devices`). Payloads larger than 8 KiB are rejected.

**Subscribed (device to CIC), QoS 1.** CIC subscribes to explicit topics, not `#`.

| Topic leaf | Payload |
|---|---|
| `status` | `1` (online) or `0` (offline / last will) |
| `version` | App version, plain text (1-64 characters) |
| `battery` | `{"battery_level": 0-100, "charging": bool, "charge_type": "..."}` |
| `wifi` | `{"connected": bool, "ssid": "..."}` |
| `location` | `{"latitude": n, "longitude": n, "gps_accuracy": n?, "time": epoch-seconds?}` |
| `device_info` | `{"device_info": "...", "screen_locked": bool, "time": ...}` |
| `sendsms/result` | `{"phone": "...", "ok": bool, "error": "..."}` (outcome of an SMS command) |

**Published (CIC to device), QoS 1, `retain=false`.** Commands are events and are never retained.

| Topic leaf | Payload | Notes |
|---|---|---|
| `ping` | `{"seconds": N}` | Rings the phone. `{"seconds": 0}` stops it. |
| `notify` | `{"title": "...", "text": "..."}` | Title defaults to `CIC`, at most 100 characters; text at most 1000. |
| `sendsms` | `{"phone": "+...", "text": "..."}` | E.164 number, text at most 1600 characters. |
| `getstatus` | `{}` | Ask the device to republish its status. |
| `getlocation` | `{}` | Ask the device to publish its location. |

Connection behaviour: keep-alive 45 s; reconnect backoff from 1 s up to 20 s; after an authentication rejection CIC retries only every 60 s and shows "not authorized, check credentials".

### Liveness

- Any **live** (non-retained) message on any leaf marks the device as seen.
- Retained messages never refresh "last seen". A retained `status` only seeds the state of a device that has never been seen live.
- A device is **online** only if `status` is `1`, it was seen live, and the last live message is no older than `devices.offline_after` (default 5 minutes).
- A location older than the one already stored is ignored.

## Usage

### First login

When the user table is empty, CIC creates the user `admin`:

- If `CIC_ADMIN_PASSWORD` is set (it must satisfy the password policy), that password is used and no change is forced.
- Otherwise a random 20-character password is printed **once to stderr** (`cic: initial admin password (shown once, ...)`) and you must change it at first login. If you lose it, run `cic user reset-password admin`.

Open `http://localhost:8080` (or your public URL) and sign in. Until the password is changed, every API call except `me`, `logout` and `change-password` answers 403.

### Roles

- **admin**: everything, including commands, history, activity log, safe places, device settings and user management.
- **viewer**: read-only. Viewers do not see hidden devices or command results.

The last active admin cannot be disabled or demoted. Create users in Settings, or with `cic user add`.

### Two-factor authentication

Each user can enable TOTP (6 digits, 30 s) under Settings, Account:

1. Enter your current password to start setup; scan the QR code or enter the secret in an authenticator app (issuer `CIC`).
2. Confirm with a code from the app. CIC then shows **10 backup codes once**. They are single-use; store them safely. Enabling 2FA ends the user's other sessions.
3. From then on, login asks for an authenticator code or a backup code after the password. You can regenerate backup codes or disable 2FA from the same page (both need your password and a code).

If a user loses both the device and the backup codes, an operator can run `cic user disable-2fa <name>`; this also ends all of that user's sessions.

### Safe places

Admins define named circles (name 1-40 characters, radius 10 m to 50 km, up to 100 places) under Settings, Safe places. A device whose position is inside a circle shows that place name; with overlaps the smallest relative distance wins.

### Geocoding

With a LatLng server key, CIC resolves a device position into an address when the device has no name yet, moved more than `geocode.min_move_meters`, or the name is older than `geocode.max_age`. To save quota, **provider calls only happen while at least one browser is connected** to the live stream; queued lookups resume when the first browser connects. Results are cached in SQLite. Calls stop at `geocode.daily_cap` per UTC day or when the provider reports fewer than `geocode.reserve` calls remaining, and a rate-limit response pauses lookups until the provider's retry time or the next UTC midnight. Today's usage is shown under Settings, Geocoding.

### Location history

When `locations.enabled` is true, CIC stores device fixes (admin-only access). To keep the database small a fix is skipped if it is within `locations.min_distance_m` (25 m) of the last stored point **and** newer by less than `locations.min_interval` (5 min). Points older than `locations.retention` (90 days) are purged hourly, and each device is capped at `locations.max_points_per_device`. The History page shows points and per-day routes (gaps over 30 minutes split a route; ranges are limited to 31 days).

### Behind a reverse proxy

Terminate TLS in the proxy and forward to CIC on `:8080`. Then:

- Set `server.base_url` to the public `https://` URL. CIC enables HSTS and checks that the `Origin` of state-changing requests matches.
- Set `server.trusted_proxies` to the proxy's IP or CIDR, so `X-Forwarded-For` (client address for login throttling) and `X-Forwarded-Proto` are honoured. A warning is logged if `base_url` is https and no proxy is trusted.
- Forward the `Host` header unchanged, and disable response buffering for `/api/events` (CIC sends `X-Accel-Buffering: no`).
- Session cookies are `Secure` and use the `__Host-` prefix, so they only work over HTTPS. Use `--insecure-cookies` / `server.insecure_cookies` only for local plain-HTTP trials.

## API

The UI uses the same REST API, under `/api`. The machine-readable spec is `GET /api/openapi.yaml` (login required; there is no Swagger UI).

Authentication: log in with `POST /api/auth/login` to get a session cookie (`__Host-cic_session`, HttpOnly, SameSite=Strict) and a `csrf_token`. Every non-GET request must send the token in an `X-CSRF-Token` header and must be same-origin (`Origin` or `Sec-Fetch-Site` check). There is no API token mechanism. `/healthz` and `/readyz` are unauthenticated: `/readyz` returns 200 only when the database responds and the broker is configured and connected.

| Method and path | Access | Purpose |
|---|---|---|
| `POST /api/auth/login`, `POST /api/auth/login/2fa` | public | Sign in (second step when 2FA is on) |
| `POST /api/auth/logout`, `GET /api/auth/me`, `POST /api/auth/change-password` | any user | Session and password |
| `GET /api/auth/2fa`, `POST /api/auth/2fa/setup`, `/enable`, `/disable`, `/backup-codes` | any user | Manage own 2FA |
| `GET /api/devices`, `GET /api/devices/{prefix}/{device}` | any user | Device state (`?include_hidden=1` is admin only) |
| `GET /api/broker`, `GET /api/ui-config` | any user | Broker state, UI/map settings |
| `GET /api/events` | any user | Server-Sent Events stream |
| `POST /api/devices/{prefix}/{device}/ping`, `/ping/stop`, `/notify`, `/sms`, `/refresh-status`, `/refresh-location` | admin | Commands |
| `PATCH` / `DELETE /api/devices/{prefix}/{device}` | admin | Rename, colour, hide, remove |
| `GET /api/devices/{prefix}/{device}/history`, `/routes` | admin | Location history and per-day routes |
| `GET /api/activity` | admin | Audit log |
| `GET /api/geocode/usage` | admin | Geocoding counters |
| `GET`, `POST /api/places`; `PATCH`, `DELETE /api/places/{id}` | admin | Safe places |
| `GET`, `POST /api/users`; `PATCH /api/users/{id}`; `POST /api/users/{id}/reset-password` | admin | User management |

Command errors return `{"error": "...", "code": "..."}` with codes such as `invalid` (400), `unknown_device` (404), `rate_limited`, `sms_cap` and `recipient_limited` (429), `sms_disabled` (403), `not_connected` and `unavailable` (503) and `outcome_unknown` (504; the command may have been delivered, do not retry blindly).

Command rate limits, per user per minute: ping and stop 10, notify 10, SMS 5, refresh status 20, refresh location 20. SMS additionally allows 3 per recipient per hour.

Server-Sent Events: `hello`, `device.updated`, `device.removed`, `devices.refresh` (re-fetch the device list), `broker.state` and, for admins, `command.result`. A user may hold at most 5 streams.

## Security notes

- Passwords are hashed with argon2id; the minimum length is 12 characters.
- Sessions are stored server-side: idle timeout 12 hours, absolute lifetime 7 days, at most 10 per user, new session on login. Password change, disable and 2FA enable end sessions.
- CSRF token on every state-changing request plus Origin / `Sec-Fetch-Site` validation; strict CSP, `frame-ancestors 'none'`, `nosniff`, `no-referrer`.
- Login throttling: a user name is locked temporarily after 5 failures (30 s, doubling up to 15 min) and a source address after 30 failures in 15 minutes, answered with 429 and `Retry-After`. The lock is global per user name, so someone can lock out a known user name for short periods; a restart clears it.
- TOTP secrets are encrypted at rest; backup codes are stored hashed.
- Commands are audited. Notification and SMS text is logged only as a length and a 20-character prefix, and phone numbers are masked to the last three digits.
- Location history is sensitive personal data. It is admin-only and expires after 90 days by default; lower `locations.retention` or set `locations.enabled: false` if you do not need it.
- Secrets (broker password, API keys) are redacted in logs. Do not commit `.env` or `secret.key`.
- Expose CIC only over HTTPS, preferably behind a reverse proxy; the broker should use TLS and credentials as well.

## Troubleshooting

- **No devices show up.** `mqtt.broker.host` is empty (MQTT is disabled and the log says telemetry ingest is disabled), or the device topic prefix is not listed in `mqtt.prefixes`. Check `GET /api/broker` or `/readyz`.
- **"not authorized, check credentials" for the broker.** CIC retries once a minute after an auth rejection; fix `mqtt.broker.username` / `password`, then restart.
- **Device shows offline although it published.** Retained messages do not count; only live messages keep a device online, and it must have `status` `1`. See [Liveness](#liveness).
- **Cannot log in over plain HTTP.** Cookies are `Secure` with a `__Host-` prefix. Use HTTPS, or `--insecure-cookies` for local testing.
- **Login or actions answer 403 "cross-site request rejected".** The proxy altered `Host`, or `server.base_url` does not match the public URL scheme.
- **Login answers 429.** Too many failed attempts; wait for `Retry-After`.
- **Commands answer 503 `not_connected`.** The broker is not connected.
- **SMS answers 403 `sms_disabled` or 429 `sms_cap`.** `commands.sms_daily_cap` is `0` or has been used up for the UTC day.
- **Geocoding does nothing.** It needs a `latlng_` server key (a `pk_` key disables it), at least one open browser, and remaining quota.
- **2FA endpoints answer 503, or 2FA codes fail after a restore.** The secret key could not be loaded or does not match the database; restore `secret.key` or set the original `CIC_SECRET_KEY`.
- **Password forgotten.** `cic user reset-password <name>`.

## Development

```bash
# backend (needs web build or the .gitkeep placeholder in internal/webui/dist)
go run ./cmd/cic serve --listen 127.0.0.1:8787 --db-path ./cic.db --insecure-cookies

# frontend with hot reload; /api is proxied to VITE_API_TARGET (default http://127.0.0.1:8787)
cd web && npm ci && npm run dev
```

Checks (these mirror CI, which also runs `govulncheck` and `npm audit --omit=dev`; CI does not run the Playwright e2e tests):

```bash
go vet ./cmd/... ./internal/...
go test -race -count=1 ./cmd/... ./internal/...
cd web && npm run lint && npm test && npm run build
npm run e2e            # Playwright
```

Layout:

| Path | Contents |
|---|---|
| `cmd/cic` | Entry point, `serve`, `user`, `healthcheck`, `version`, wiring |
| `internal/api` | HTTP handlers, SSE, security headers, OpenAPI spec |
| `internal/auth` | Users, sessions, argon2id, throttling, TOTP, bootstrap admin |
| `internal/audit`, `internal/commands` | Audit log; command validation, rate limits, SMS caps |
| `internal/config` | Configuration loading and validation |
| `internal/devices`, `internal/mqtt`, `internal/events` | Device registry and liveness, broker client, SSE hub |
| `internal/geocode`, `internal/places`, `internal/history` | Geocoding, safe places, location history |
| `internal/store`, `internal/secret` | SQLite and migrations, encryption key |
| `internal/webui`, `web/` | Embedded UI and its React + TypeScript + Vite + Tailwind source |
| `scripts/` | `build.sh`, `next-version.sh` |

Releases use the date-based version `YYYY.M.PATCH` (for example `2026.11.0`; no leading zero in the month), computed by `scripts/next-version.sh` from git tags. The `Release` workflow builds the Linux binaries and creates the tag and GitHub release; the `Docker` workflow publishes `techblog/cic:latest` and `techblog/cic:<version>`.

## Contributing

Issues and pull requests are welcome at <https://github.com/t0mer/cic>. Please keep changes focused, run the checks above, and add tests for behaviour changes.

## License

Apache License 2.0. See [LICENSE](LICENSE).
