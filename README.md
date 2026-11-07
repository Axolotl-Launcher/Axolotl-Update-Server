# Axolotl Update Server

Independent Flask service for publishing signed Axolotl Launcher release and beta updates.

See the deployment and API documentation below.

## Local setup

Python 3.11+ is required. Create a virtual environment, install dependencies, copy `.env.example` to `.env`, and set non-default `SECRET_KEY`, `WEBHOOK_SECRET`, and `ADMIN_TOKEN` values. `UPLOAD_TOKEN` is retained for the retired upload endpoint; publication uses the HMAC webhook.

```bash
python -m venv .venv
.venv/bin/pip install -r requirements.txt
flask --app wsgi:create_app run --host 127.0.0.1 --port 8082
```

The Windows equivalent is `.venv\\Scripts\\python -m pip install -r requirements.txt`. SQLite is used by default; `DATABASE_URL` accepts any SQLAlchemy URL. Development/test runs may create tables automatically; production schema changes must use Flask-Migrate.

## API

Complete endpoint documentation is available in [docs/API.md](docs/API.md).

To remove a version for republishing, use `DELETE /api/admin/versions/<version>`
with the admin Bearer token and optional `{"operator":"admin","reason":"Republish"}`.
It removes local version files, artifact/version records and associated webhook
events, recomputes both channel pointers, and records an audit log. Historical
statistics and GitHub/CNB Releases are retained. Apply migrations through
`0008_artifact_target_platforms` before startup. See the API reference for cleanup
retry behavior, legacy webhook IDs and immutable CDN cache considerations.

* `GET /api/health`, `GET /api/versions`, and `GET /api/versions/<version>` expose service/version data.
* Usage collection is disabled: API/update checks and file downloads do not insert statistics or commit a per-request database transaction. `GET /api/admin/stats` remains a read-only legacy endpoint for old data; run `flask --app wsgi:app purge-usage` to delete history and compact SQLite.
* `PUT /api/artifacts/<version>/<filename>` is retired for both channels. After upload authentication it returns `409 release_metadata_only`; package bytes are never read or stored.
* Publish with `POST /api/webhook/release`. The workflow publishes the public GitHub Release then sends a small HMAC-authenticated payload. Both Release and Beta imports store catalog/asset metadata and a local manifest only: no updater, installer, portable or signature assets are persisted under `DIST_ROOT`. Only updater `.sig` contents are temporarily fetched (up to 64 KiB each), hash/size checked, decoded and stored as signatures in the database. CI does not upload large files through `/api/artifacts/*` and does not need `UPLOAD_TOKEN`.
* Check updates with `/latest`; headers `X-Axolotl-Channel`, `X-Axolotl-Platform`, and `X-Axolotl-Version` override query parameters. Release selects only stable published Release versions. Beta selects the highest compatible stable Release or published beta prerelease.
* Browse complete public packages with `GET /api/downloads/latest?channel=release|beta` or `GET /api/downloads/<version>`. These endpoints return installer and portable artifacts with generated `/dist/<version>/...` URLs, labels, variants, sizes, and SHA-256 hashes. They intentionally exclude updater artifacts unless `include_updater=true` is supplied; they never expose signature-only attachments as packages.
* Admin endpoints require `Authorization: Bearer <ADMIN_TOKEN>`: `POST /api/admin/versions/<version>/revoke`, `POST /api/admin/versions/<version>/restore`, `PATCH /api/admin/versions/<version>/force-update`, and `GET /api/admin/audit-logs`. The force-update endpoint accepts `{"force_update":true,"operator":"admin","reason":"Urgent fix"}` (or `false` to disable), synchronizes the local manifest, and records an audit log.

`/latest` returns a Tauri-compatible manifest with UTC `pub_date`, `published_at`, and `force_update`, server-generated ETags, and only `https://update.axlmc.org/dist/...` URLs. `force_update=true` tells Launcher to bypass its normal 24-hour Release delay; it never bypasses pause settings or signature verification. It returns `204` when no compatible update exists. ETag/304 is optional optimization and not required by the Launcher. Flask's development `/dist/<version>/<filename>` fallback supports GET, HEAD, and Range; revoked files remain downloadable by direct URL, but are never returned by `/latest`.

Webhook payloads include public GitHub Release metadata and a catalog. `release.published_at` remains the update publication time. `catalog.files` lists all physical assets (including signatures), while `catalog.artifacts` lists only main updater/installer/portable artifacts. Updater artifacts must name their `.sig` companion with `signatureFilename`; its trimmed UTF-8 content becomes the Tauri signature. An updater may also provide `targetPlatforms`; universal macOS artifacts map one file to both `darwin-aarch64` and `darwin-x86_64`, and an unpublished version cannot retain another updater for an overlapping target. Complete packages may have an attachment but do not require a Tauri signature. Payloads may include `"force_update": true` (default is `false`):

```json
{"event_id":"github-v1.10.0-123","tag":"v1.10.0","version":"1.10.0","channel":"release","force_update":true,"release":{"id":123,"tag_name":"v1.10.0","draft":false,"published_at":"2026-08-30T10:00:00Z","assets":[]},"catalog":{"version":"1.10.0","files":[],"artifacts":[]}}
```

Webhook retries with the same event ID and payload hash are safe; a failed event may be retried after fixing artifacts. Reusing an event ID with a different payload is rejected.

The automatic updater and the complete package catalog are separate: `/latest` contains only signed Tauri updater artifacts, while `/api/downloads/*` is for website/manual downloads. GitHub Release and CNB Release remain publishing records and manual-download sources; Launcher automatic updates use only this Update Server.

Both Release and Beta now use `Version.artifact_storage=metadata`. Their `/latest` and package catalogs retain Update Server `/dist/<version>/<filename>` URLs without requiring local payload files. Payload distribution is intentionally left to separate deployment arrangements: missing metadata-only assets return `404` at Flask, including after more than three versions; no new GitHub redirect behavior is added. Local manifest metadata remains available for Force Update changes. Legacy versions keep their existing behavior until explicitly converted with `purge-dist-assets`.

Apply migration `0009_version_artifact_storage` before starting the new code.
Existing versions default to `local`; the migration neither deletes their files
nor converts their delivery behavior. `PUT /api/artifacts/*` requests for both
channels now return `409 release_metadata_only`. Main-package hash/size values are
cross-checked against signed-webhook GitHub asset metadata, not recomputed from
package bytes, because those bytes are no longer downloaded.

Revoking a version removes it from `/latest` and download-directory latest selection while retaining its immutable `/dist/<version>/...` URLs for in-progress or previously published links. A separate `blocked` artifact state is intentionally not enabled yet; urgent takedowns should be handled at the Caddy/object-storage layer until that policy is introduced with an audited migration.

## Caddy and production

Copy `Caddyfile.example` to the Caddy configuration. It routes `/dist/*` to `/www/wwwroot/update.axlmc.org/dist`, and `/latest` plus `/api/*` to Flask on `127.0.0.1:8082`; Caddy automatically manages HTTPS certificates. Set `DIST_ROOT=/www/wwwroot/update.axlmc.org/dist` so files are stored as `/dist/<version>/...`. Run Flask behind Gunicorn:

```bash
gunicorn \
  --workers 2 \
  --worker-class sync \
  --timeout 1800 \
  --graceful-timeout 30 \
  --bind 127.0.0.1:8082 \
  wsgi:app
```

Back up both the SQLite database (`instance/update-server.db` by default) and the complete `DIST_ROOT` tree. Never commit `.env`, database files, or artifacts. `MAX_UPLOAD_SIZE` limits manual request bodies and `WEBHOOK_MAX_AGE_SECONDS` limits replay windows. GitHub imports are bounded by `GITHUB_DOWNLOAD_CONNECT_TIMEOUT_SECONDS`, `GITHUB_DOWNLOAD_READ_TIMEOUT_SECONDS`, `GITHUB_DOWNLOAD_RETRIES`, and `GITHUB_DOWNLOAD_MAX_SIZE`.

The built-in rate limiter is process-local and intended as a baseline safeguard. With multiple Gunicorn workers, use a shared proxy/Redis limiter for consistent enforcement.

### Cloudflare client IP and access logs

The normal request path is client -> Cloudflare -> Caddy -> Gunicorn. The app
resolves `request.remote_addr` through trusted proxy addresses before Flask runs,
so admin audit records and rate limiting use the client IP. Gunicorn's existing
`%(h)s` access-log field also uses this resolved address.

The default trusted networks are `127.0.0.1/32`, `::1/128`, and the official
Cloudflare IPv4/IPv6 ranges. `X-Forwarded-For` is read from right to left, stopping
at the first untrusted address. `CF-Connecting-IP` is used only when a Cloudflare
hop has been verified in that trusted chain. Invalid chains fall back to the
connection IP; a direct internet request cannot select an IP using these headers.
Keep Gunicorn bound to loopback. Docker/extra proxies need an explicit
comma-separated `TRUSTED_PROXY_CIDRS` containing only their actual addresses;
setting it empty disables forwarded-IP handling. It replaces the default list.

The updated `Caddyfile.example` supports older Caddy versions: it sets
`X-Forwarded-For` to `{http.request.remote.host}` and preserves
`CF-Connecting-IP`. Flask verifies that socket peer is a Cloudflare address
before using the CF header. Direct requests to Caddy use the socket IP.
Review the lists against [Cloudflare IPv4](https://www.cloudflare.com/ips-v4/)
and [IPv6](https://www.cloudflare.com/ips-v6/) when updating deployment.
See [Caddy trusted proxies](https://caddyserver.com/docs/caddyfile/options#trusted-proxies)
and [Cloudflare headers](https://developers.cloudflare.com/fundamentals/reference/http-headers/).
Cloudflare Tunnel is a different proxy topology: configure its trusted local
connector explicitly rather than trusting arbitrary CF headers from loopback.

Only Gunicorn emits application HTTP access logs; Flask's duplicate JSON request
logger has been removed. Its standard log line uses the real client IP:

```text
203.0.113.12 - - [01/Nov/2026:20:41:39 +0800] "GET /latest HTTP/1.1" 200 1455 "-" "Axolotl-Launcher/1.10.4 (windows)"
```

Caddy emits JSON access logs to stderr for static `/dist` traffic that bypasses
Flask. Old Caddy versions may show the Cloudflare node IP in those separate logs.
Gunicorn access logs remain enabled by the systemd unit.
Historical audit rows are not rewritten because their original IP cannot be recovered.

After deploying the code, merge the example's proxy settings into your Caddyfile
and validate before reload:

```bash
sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
sudo systemctl reload caddy
sudo systemctl restart axolotl-update-server
sudo journalctl -u axolotl-update-server -f
# Static file requests:
sudo journalctl -u caddy -f
```

This IP/logging change adds no database migration. If updating from an older
checkout, the existing systemd ExecStartPre runs any previously pending migrations.

### Latest response cache and Cloudflare CDN

`LATEST_CACHE_SECONDS=30` enables a bounded per-process cache of `/latest` results,
including empty `204` responses. Keys contain the effective channel, platform and
client version. Committed Version/Artifact/ChannelPointer changes invalidate the
local cache and a shared instance-directory revision file invalidates other
workers. Rollbacks do not publish a new revision. A value of `0` disables caching.

The `200`, `204` and `304` responses all include cache policy and `Vary` for
`X-Axolotl-Channel`, `X-Axolotl-Platform`, and `X-Axolotl-Version`. Query-only
requests return `Cache-Control: public, max-age=0, s-maxage=30, must-revalidate`
and `Cloudflare-CDN-Cache-Control: public, max-age=30`. Invalid requests and
requests with Authorization/Cookie are not shared-cacheable. Caddy must forward
these origin headers unchanged: remove its old `/latest` `header Cache-Control`
override. The sample Caddyfile already does so.

Cloudflare does not cache JSON by default and its default key does not separate
these custom headers. `Vary` alone is insufficient for Cloudflare. To cache
query-only requests, add a native Cache Rule limited to `/latest` **without any
of the three variant headers**, mark it Eligible for cache, retain the full query
string in the cache key and respect origin cache-control/TTL. Do not configure
a blanket Cache Everything rule ignoring query strings or variant headers.

Header-based clients use the origin cache by default (`private` and Cloudflare
`no-store` prevent cross-channel/platform leakage). If your Cloudflare plan
supports a native custom cache key, first include all three X-Axolotl headers
and all query parameters in that key, then set
`LATEST_CDN_HEADER_CACHE_KEY=true` and enable caching for `/latest` through a
native Cache Rule respecting origin TTL. No Worker deployment is needed.
See [Cloudflare cache keys](https://developers.cloudflare.com/cache/how-to/cache-keys/)
and [default cache behavior](https://developers.cloudflare.com/cache/concepts/default-cache-behavior/).

Origin invalidation is immediate after successful admin changes; CDN copies may
remain until the short TTL expires. Purge the CDN URL for an urgent revocation.
Use `CF-Cache-Status` and `Age` to verify actual edge hits; origin headers alone
do not establish that a Cloudflare rule is active.

### Delete old usage history and reclaim SQLite disk space

Usage collection is disabled in code. To remove accumulated history, stop the
service and run the maintenance command with the same environment/database as
the service (the normal `.env` is loaded by the app):

```bash
cd /www/server/Axolotl-Update-Server
sudo systemctl stop axolotl-update-server
.venv/bin/flask --app wsgi:app purge-usage
sudo systemctl start axolotl-update-server
```

`purge-usage` deletes **only UsageEvent statistics**, commits, runs SQLite VACUUM
and truncates the WAL checkpoint. Versions, artifact records/files, webhook
events and admin audit history remain intact. VACUUM rebuilds the database;
allow free disk space for temporary copies (up to twice the database size).
For a custom DATABASE_URL supplied outside `.env`, export the same value before
invoking Flask. Stop other processes accessing the database while compacting it.
If compaction fails, deletion is already committed and rerunning the command is
safe. Existing migration tables/schema are retained; no new migration is required.

### Artifact retention

For legacy local-storage versions, successful webhooks prune old published directories after commit. Defaults remain `RELEASE_RETENTION_COUNT=3` and `BETA_RETENTION_COUNT=3`. Metadata-only Release and Beta manifests are excluded from physical retention; their update/catalog selection uses stored metadata. Legacy local versions still need available files until explicitly converted. Draft/uploading versions and non-version directories are not removed.

### Remove existing Release and Beta packages

Stop the service, apply the existing storage migration, then run the maintenance
command against the same `.env` and database used by production:

```bash
cd /www/server/Axolotl-Update-Server
sudo systemctl stop axolotl-update-server
.venv/bin/flask --app wsgi:app db upgrade
.venv/bin/flask --app wsgi:app purge-dist-assets --dry-run
.venv/bin/flask --app wsgi:app purge-dist-assets
sudo systemctl start axolotl-update-server
```

By default it converts all database-registered Release and Beta versions to
metadata-only storage and removes every entry under their version directories
except `manifest.json`. `--version 1.9.4` restricts it to one version. The command
keeps Version/Artifact/Webhook records, status, publication date, Force Update,
channel pointers and signatures. A missing DB updater signature can be recovered
from its corresponding local `.sig` (64 KiB maximum) before removal. Existing
pruned directories can be recreated with just the manifest using stored signatures.
Missing signatures or incomplete artifact metadata stop the affected conversion
without deleting its files. Unregistered directories are not touched.

The storage-mode commit occurs before file deletion, so an interrupted cleanup
can be rerun to remove remaining files without losing version metadata. Earlier
versions may already be converted if a later version fails. Each conversion is
audited as `purge_dist_assets`; database commit failures preserve the package files.
No packages are downloaded, and converted missing `/dist` assets return `404`
instead of falling back to GitHub. Removed package bytes can only be recovered
from your backups or original publishing sources. This does not remove remote
GitHub/CNB assets or purge existing CDN caches.

### systemd

`deploy/axolotl-update-server.service` runs as root from `/www/server/Axolotl-Update-Server`, loads `/www/server/Axolotl-Update-Server/.env`, runs migrations with `.venv/bin/flask`, and starts `.venv/bin/gunicorn`.

Create the virtual environment and install dependencies:

```bash
cd /www/server/Axolotl-Update-Server
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
```

Copy `.env.example` to `.env` and set unique production secrets:

```bash
cd /www/server/Axolotl-Update-Server
cp .env.example .env
chmod 600 .env
```

The `.env` file is consumed by systemd's `EnvironmentFile`, so use plain `KEY=value` lines. Do not use `export`, shell substitutions, multiline values, or quotes that need shell interpretation. Use absolute production paths:

```dotenv
FLASK_ENV=production
SECRET_KEY=<unique-secret>
DATABASE_URL=sqlite:////www/server/Axolotl-Update-Server/instance/update-server.db
DIST_ROOT=/www/wwwroot/update.axlmc.org/dist
PUBLIC_BASE_URL=https://update.axlmc.org
UPDATE_SERVER_HOST=127.0.0.1
UPDATE_SERVER_PORT=8082
UPLOAD_TOKEN=<unique-upload-token>
WEBHOOK_SECRET=<unique-webhook-secret>
ADMIN_TOKEN=<unique-admin-token>
MAX_UPLOAD_SIZE=536870912
WEBHOOK_MAX_AGE_SECONDS=300
RATE_LIMIT_PER_MINUTE=120
```

Ensure the database directory and `DIST_ROOT` exist:

```bash
mkdir -p /www/server/Axolotl-Update-Server/instance
mkdir -p /www/wwwroot/update.axlmc.org/dist
```

Install and start the service:

```bash
sudo install -m 644 deploy/axolotl-update-server.service /etc/systemd/system/axolotl-update-server.service
sudo systemctl daemon-reload
sudo systemctl enable --now axolotl-update-server
sudo systemctl status axolotl-update-server
```

View logs and restart after changing `.env`, dependencies, or source files:

```bash
sudo journalctl -u axolotl-update-server -f
sudo systemctl restart axolotl-update-server
```

The unit runs `flask --app wsgi:app db upgrade` before Gunicorn starts. Back up the SQLite database and `DIST_ROOT` before deploying a schema migration. Stop or drain old workers before applying `0008_artifact_target_platforms`; a legacy worker must not publish a universal updater after the backfill. If migration fails, systemd does not start Gunicorn.

### Database migrations

Initialize or upgrade the schema before production startup:

```bash
flask --app wsgi:app db upgrade
```

The repository includes an idempotent baseline migration and `0002_force_update`. For future model changes, run `flask --app wsgi:app db migrate -m "describe change"`, review the generated migration, then run `flask --app wsgi:app db upgrade`. Back up the database before upgrades; rollback is performed with `flask --app wsgi:app db downgrade <revision>` only after verifying a tested backup.

## Example calls

```bash
curl -H 'X-Axolotl-Channel: release' -H 'X-Axolotl-Platform: windows-x86_64' https://update.axlmc.org/latest
curl 'https://update.axlmc.org/api/downloads/latest?channel=release'
curl -X POST -H 'Authorization: Bearer admin-secret' -H 'Content-Type: application/json' -d '{"reason":"regression","operator":"admin"}' https://update.axlmc.org/api/admin/versions/1.10.0/revoke
curl -X POST -H 'Authorization: Bearer admin-secret' https://update.axlmc.org/api/admin/versions/1.10.0/restore
```

Quality checks: `python -m pytest -q`, `ruff check app wsgi.py tests`, and `mypy app`.
