# Axolotl Update Server

Independent Flask service for publishing signed Axolotl Launcher release and beta updates.

See the deployment and API documentation below.

## Local setup

Python 3.11+ is required. Create a virtual environment, install dependencies, copy `.env.example` to `.env`, and set non-default `SECRET_KEY`, `WEBHOOK_SECRET`, and `ADMIN_TOKEN` values. `UPLOAD_TOKEN` is optional and only used for local/admin artifact maintenance.

```bash
python -m venv .venv
.venv/bin/pip install -r requirements.txt
flask --app wsgi:create_app run --host 127.0.0.1 --port 8082
```

The Windows equivalent is `.venv\\Scripts\\python -m pip install -r requirements.txt`. SQLite is used by default; `DATABASE_URL` accepts any SQLAlchemy URL. Development/test runs may create tables automatically; production schema changes must use Flask-Migrate.

## API

* `GET /api/health`, `GET /api/versions`, and `GET /api/versions/<version>` expose service/version data.
* `GET /api/admin/stats?start=<ISO-8601>&end=<ISO-8601>` (admin token required) reports API call totals, API calls grouped by `release`/`beta`, and download count/bytes for a range of at most 30 days. The default range is the previous 24 hours. Requests served entirely by Caddy's static `/dist` handler do not pass through Flask; aggregate Caddy access logs separately for production-wide static-download metrics. Flask's `/dist` fallback requests are included automatically.
* Upload with `PUT /api/artifacts/<version>/<filename>` and `X-Upload-Token`. The body is hashed server-side and stored below `DIST_ROOT/<version>/` (with the default `DIST_ROOT=./dist`, this is `./dist/<version>/`).
  Package metadata is supplied with `X-Axolotl-Platform` (`windows`, `macos`, `linux` or an updater platform), `X-Axolotl-Architecture` (`x86_64`, `aarch64`, `universal`), `X-Axolotl-Kind` (`updater`, `installer`, `portable`, `signature`, `manifest`, `other`), optional `X-Axolotl-Variant`, and `X-Axolotl-Display-Name`. Existing uploads default to `kind=updater` for compatibility.
* Publish with `POST /api/webhook/release`. The normal GitHub Actions flow first publishes the GitHub Release, then sends a small HMAC-authenticated payload. The server validates the public GitHub metadata and catalog, streams every catalog file (including `.sig`) to `DIST_ROOT/<version>/`, validates SHA-256 and size, then publishes atomically. The workflow must not upload large files through `/api/artifacts/*` and does not need `UPLOAD_TOKEN`.
* Check updates with `/latest`; headers `X-Axolotl-Channel`, `X-Axolotl-Platform`, and `X-Axolotl-Version` override query parameters. Release selects only stable published Release versions. Beta selects the highest compatible stable Release or published beta prerelease.
* Browse complete public packages with `GET /api/downloads/latest?channel=release|beta` or `GET /api/downloads/<version>`. These endpoints return installer and portable artifacts with generated `/dist/<version>/...` URLs, labels, variants, sizes, and SHA-256 hashes. They intentionally exclude updater artifacts unless `include_updater=true` is supplied; they never expose signature-only attachments as packages.
* Admin endpoints require `Authorization: Bearer <ADMIN_TOKEN>`: `POST /api/admin/versions/<version>/revoke`, `POST /api/admin/versions/<version>/restore`, and `GET /api/admin/audit-logs`.

`/latest` returns a Tauri-compatible manifest with UTC `pub_date`, `published_at`, and `force_update`, server-generated ETags, and only `https://update.axlmc.org/dist/...` URLs. `force_update=true` tells Launcher to bypass its normal 24-hour Release delay; it never bypasses pause settings or signature verification. It returns `204` when no compatible update exists. ETag/304 is optional optimization and not required by the Launcher. Flask's development `/dist/<version>/<filename>` fallback supports GET, HEAD, and Range; revoked files remain downloadable by direct URL, but are never returned by `/latest`.

Webhook payloads include public GitHub Release metadata and a catalog. `release.published_at` remains the update publication time. `catalog.files` lists all physical assets (including signatures), while `catalog.artifacts` lists only main updater/installer/portable artifacts. Updater artifacts must name their `.sig` companion with `signatureFilename`; its trimmed UTF-8 content becomes the Tauri signature. Complete packages may have an attachment but do not require a Tauri signature. Payloads may include `"force_update": true` (default is `false`):

```json
{"event_id":"github-v1.10.0-123","tag":"v1.10.0","version":"1.10.0","channel":"release","force_update":true,"release":{"id":123,"tag_name":"v1.10.0","draft":false,"published_at":"2026-08-30T10:00:00Z","assets":[]},"catalog":{"version":"1.10.0","files":[],"artifacts":[]}}
```

Webhook retries with the same event ID and payload hash are safe; a failed event may be retried after fixing artifacts. Reusing an event ID with a different payload is rejected.

The automatic updater and the complete package catalog are separate: `/latest` contains only signed Tauri updater artifacts, while `/api/downloads/*` is for website/manual downloads. GitHub Release and CNB Release remain publishing records and manual-download sources; Launcher automatic updates use only this Update Server.

The newest three published Release versions and three published Beta versions remain in `DIST_ROOT`. When a request targets an artifact from a GitHub-imported version pruned by this retention policy, `/dist/<version>/<filename>` returns `302 Found` to its fixed GitHub Release asset URL. Existing local files continue to be served directly by Caddy; missing unknown, current, or manually uploaded artifacts return `404`.

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

### Artifact retention

To limit disk usage, a successful release webhook prunes published version directories after the database transaction completes. By default, `RELEASE_RETENTION_COUNT=3` and `BETA_RETENTION_COUNT=3`; the retained set is the union of the newest three stable Release versions and newest three Beta prerelease versions. Database version and artifact metadata is preserved, while removed files are no longer eligible for `/latest` or `/api/downloads/*`. Configure the counts with environment variables when a different retention window is required. Draft/uploading versions and non-version directories are not removed.

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

The unit runs `flask --app wsgi:app db upgrade` before Gunicorn starts. Back up the SQLite database and `DIST_ROOT` before deploying a schema migration. If migration fails, systemd does not start Gunicorn.

### Database migrations

Initialize or upgrade the schema before production startup:

```bash
flask --app wsgi:app db upgrade
```

The repository includes an idempotent baseline migration and `0002_force_update`. For future model changes, run `flask --app wsgi:app db migrate -m "describe change"`, review the generated migration, then run `flask --app wsgi:app db upgrade`. Back up the database before upgrades; rollback is performed with `flask --app wsgi:app db downgrade <revision>` only after verifying a tested backup.

## Example calls

```bash
curl -X PUT --data-binary @Axolotl.tar.gz -H 'X-Upload-Token: upload-secret' -H 'X-Axolotl-Platform: linux-x86_64' -H 'X-Axolotl-Signature: base64-signature' https://update.axlmc.org/api/artifacts/1.10.0/Axolotl.tar.gz
curl -X PUT --data-binary @Axolotl-setup.exe -H 'X-Upload-Token: upload-secret' -H 'X-Axolotl-Platform: windows' -H 'X-Axolotl-Architecture: x86_64' -H 'X-Axolotl-Kind: installer' -H 'X-Axolotl-Variant: modern' -H 'X-Axolotl-Display-Name: Windows x64 Modern Installer' https://update.axlmc.org/api/artifacts/1.10.0/Axolotl-setup.exe
curl -H 'X-Axolotl-Channel: release' -H 'X-Axolotl-Platform: windows-x86_64' https://update.axlmc.org/latest
curl 'https://update.axlmc.org/api/downloads/latest?channel=release'
curl -X POST -H 'Authorization: Bearer admin-secret' -H 'Content-Type: application/json' -d '{"reason":"regression","operator":"admin"}' https://update.axlmc.org/api/admin/versions/1.10.0/revoke
curl -X POST -H 'Authorization: Bearer admin-secret' https://update.axlmc.org/api/admin/versions/1.10.0/restore
```

Quality checks: `python -m pytest -q`, `ruff check app wsgi.py tests`, and `mypy app`.
