# Axolotl Update Server

Independent Flask service for publishing signed Axolotl Launcher release and beta updates.

See the deployment and API documentation below.

## Local setup

Python 3.11+ is required. Create a virtual environment, install dependencies, copy `.env.example` to `.env`, and set non-default `SECRET_KEY`, `UPLOAD_TOKEN`, `WEBHOOK_SECRET`, and `ADMIN_TOKEN` values:

```bash
python -m venv .venv
.venv/bin/pip install -r requirements.txt
flask --app wsgi:create_app run --host 127.0.0.1 --port 8082
```

The Windows equivalent is `.venv\\Scripts\\python -m pip install -r requirements.txt`. SQLite is used by default; `DATABASE_URL` accepts any SQLAlchemy URL. Development/test runs may create tables automatically; production schema changes must use Flask-Migrate.

## API

* `GET /api/health`, `GET /api/versions`, and `GET /api/versions/<version>` expose service/version data.
* Upload with `PUT /api/artifacts/<version>/<filename>` and `X-Upload-Token`. The body is hashed server-side and stored below `DIST_ROOT/<version>/` (with the default `DIST_ROOT=./dist`, this is `./dist/<version>/`).
* Publish with `POST /api/webhook/release`. Send JSON described in the objective, `X-Webhook-Timestamp` (Unix seconds), and `X-Webhook-Signature: sha256=<HMAC-SHA256 of timestamp + '.' + raw body>`.
* Check updates with `/latest`; headers `X-Axolotl-Channel`, `X-Axolotl-Platform`, and `X-Axolotl-Version` override query parameters. Release selects only stable published Release versions. Beta selects the highest compatible stable Release or published beta prerelease.
* Admin endpoints require `Authorization: Bearer <ADMIN_TOKEN>`: `POST /api/admin/versions/<version>/revoke`, `POST /api/admin/versions/<version>/restore`, and `GET /api/admin/audit-logs`.

`/latest` returns a Tauri-compatible manifest with UTC `pub_date`, `published_at`, and `force_update`, server-generated ETags, and only `https://update.axlmc.org/dist/...` URLs. `force_update=true` tells Launcher to bypass its normal 24-hour Release delay; it never bypasses pause settings or signature verification. It returns `204` when no compatible update exists. ETag/304 is optional optimization and not required by the Launcher. Flask's development `/dist/<version>/<filename>` fallback supports GET, HEAD, and Range; revoked files remain downloadable by direct URL, but are never returned by `/latest`.

Webhook payloads may include `"force_update": true` (default is `false`):

```json
{"event_id":"release-1.10.0","tag":"v1.10.0","version":"1.10.0","channel":"release","force_update":true,"artifacts":[{"platform":"windows-x86_64","filename":"update.zip","size":123,"sha256":"...","signature":"..."}]}
```

Webhook retries with the same event ID and payload hash are safe; a failed event may be retried after fixing artifacts. Reusing an event ID with a different payload is rejected.

## Caddy and production

Copy `Caddyfile.example` to the Caddy configuration. It routes `/dist/*` to `/www/wwwroot/update.axlmc.org/dist`, and `/latest` plus `/api/*` to Flask on `127.0.0.1:8082`; Caddy automatically manages HTTPS certificates. Set `DIST_ROOT=/www/wwwroot/update.axlmc.org/dist` so files are stored as `/dist/<version>/...`. Run Flask behind Gunicorn:

```bash
gunicorn --workers 2 --bind 127.0.0.1:8082 wsgi:app
```

Back up both the SQLite database (`instance/update-server.db` by default) and the complete `DIST_ROOT` tree. Never commit `.env`, database files, or artifacts. `MAX_UPLOAD_SIZE` limits request bodies and `WEBHOOK_MAX_AGE_SECONDS` limits replay windows.

### Database migrations

Initialize or upgrade the schema before production startup:

```bash
flask --app wsgi:app db upgrade
```

The repository includes an idempotent baseline migration and `0002_force_update`. For future model changes, run `flask --app wsgi:app db migrate -m "describe change"`, review the generated migration, then run `flask --app wsgi:app db upgrade`. Back up the database before upgrades; rollback is performed with `flask --app wsgi:app db downgrade <revision>` only after verifying a tested backup.

## Example calls

```bash
curl -X PUT --data-binary @Axolotl.tar.gz -H 'X-Upload-Token: upload-secret' -H 'X-Axolotl-Platform: linux-x86_64' -H 'X-Axolotl-Signature: base64-signature' https://update.axlmc.org/api/artifacts/1.10.0/Axolotl.tar.gz
curl -H 'X-Axolotl-Channel: release' -H 'X-Axolotl-Platform: windows-x86_64' https://update.axlmc.org/latest
curl -X POST -H 'Authorization: Bearer admin-secret' -H 'Content-Type: application/json' -d '{"reason":"regression","operator":"admin"}' https://update.axlmc.org/api/admin/versions/1.10.0/revoke
curl -X POST -H 'Authorization: Bearer admin-secret' https://update.axlmc.org/api/admin/versions/1.10.0/restore
```

Quality checks: `python -m pytest -q`, `ruff check app wsgi.py tests`, and `mypy app`.
