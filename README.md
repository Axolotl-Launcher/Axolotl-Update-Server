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

The Windows equivalent is `.venv\\Scripts\\python -m pip install -r requirements.txt`. SQLite is used by default; `DATABASE_URL` accepts any SQLAlchemy URL. Tables are created on startup (use a migration tool before production schema changes).

## API

* `GET /api/health`, `GET /api/versions`, and `GET /api/versions/<version>` expose service/version data.
* Upload with `PUT /api/artifacts/<version>/<filename>` and `X-Upload-Token`. The body is hashed server-side and stored below `DIST_ROOT/<version>/` (with the default `DIST_ROOT=./dist`, this is `./dist/<version>/`).
* Publish with `POST /api/webhook/release`. Send JSON described in the objective, `X-Webhook-Timestamp` (Unix seconds), and `X-Webhook-Signature: sha256=<HMAC-SHA256 of timestamp + '.' + raw body>`.
* Check updates with `/latest`; headers `X-Axolotl-Channel`, `X-Axolotl-Platform`, and `X-Axolotl-Version` override query parameters. Release selects stable published versions. Beta selects published beta prereleases; stable versions are intentionally not mixed into beta.
* Admin endpoints require `Authorization: Bearer <ADMIN_TOKEN>`: `POST /api/admin/versions/<version>/revoke`, `POST /api/admin/versions/<version>/restore`, and `GET /api/admin/audit-logs`.

`/latest` returns a Tauri-compatible manifest with UTC `pub_date` and `published_at`, server-generated ETags, and only `https://update.axlmc.org/dist/...` URLs. It returns `204` when no compatible update exists. Flask's development `/dist/<version>/<filename>` fallback supports GET, HEAD, and Range; production should serve these files directly from Caddy.

## Caddy and production

Copy `Caddyfile.example` to the Caddy configuration. It routes `/dist/*` to `/www/wwwroot/update.axlmc.org/dist`, and `/latest` plus `/api/*` to Flask on `127.0.0.1:8082`; Caddy automatically manages HTTPS certificates. Set `DIST_ROOT=/www/wwwroot/update.axlmc.org/dist` so files are stored as `/dist/<version>/...`. Run Flask behind Gunicorn:

```bash
gunicorn --workers 2 --bind 127.0.0.1:8082 wsgi:app
```

Back up both the SQLite database (`instance/update-server.db` by default) and the complete `DIST_ROOT` tree. Never commit `.env`, database files, or artifacts. `MAX_UPLOAD_SIZE` limits request bodies and `WEBHOOK_MAX_AGE_SECONDS` limits replay windows.

## Example calls

```bash
curl -X PUT --data-binary @Axolotl.tar.gz -H 'X-Upload-Token: upload-secret' -H 'X-Axolotl-Platform: linux-x86_64' -H 'X-Axolotl-Signature: base64-signature' https://update.axlmc.org/api/artifacts/1.10.0/Axolotl.tar.gz
curl -H 'X-Axolotl-Channel: release' -H 'X-Axolotl-Platform: windows-x86_64' https://update.axlmc.org/latest
curl -X POST -H 'Authorization: Bearer admin-secret' -H 'Content-Type: application/json' -d '{"reason":"regression","operator":"admin"}' https://update.axlmc.org/api/admin/versions/1.10.0/revoke
curl -X POST -H 'Authorization: Bearer admin-secret' https://update.axlmc.org/api/admin/versions/1.10.0/restore
```

Quality checks: `python -m pytest -q`, `ruff check app wsgi.py tests`, and `mypy app`.
