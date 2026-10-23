# Axolotl Update Server API

Base URL: `https://update.axlmc.org`

This document describes the API implemented by the Flask Update Server. GitHub Release and CNB Release remain publishing records and manual-download sources; Launcher automatic updates use this Update Server's `/latest` endpoint.

## Conventions

### Authentication

Administrative endpoints use:

```http
Authorization: Bearer <ADMIN_TOKEN>
```

The maintenance upload endpoint uses:

```http
X-Upload-Token: <UPLOAD_TOKEN>
```

The upload token is optional for deployment and is not required by the normal GitHub Actions release workflow.

### Errors

Errors use this shape:

```json
{
  "error": {
    "code": "invalid_version",
    "message": "Version must be a normalized SemVer.",
    "request_id": "8d9c..."
  }
}
```

Every response includes `X-Request-ID`. Version values must be normalized SemVer, for example `1.9.4` or `1.9.5-beta.1`.

## Update and Static File Endpoints

### `GET /latest`

Returns a Tauri-compatible updater manifest.

Headers override query parameters:

```http
X-Axolotl-Channel: release | beta
X-Axolotl-Platform: windows-x86_64 | linux-x86_64 | linux-aarch64 | darwin-x86_64 | darwin-aarch64
X-Axolotl-Version: 1.9.3
```

Equivalent query parameters:

```text
/latest?channel=release&platform=windows-x86_64&current_version=1.9.3
```

Behavior:

- `release` returns only published stable Release versions.
- `beta` returns the highest published stable Release or published beta prerelease.
- Revoked, uploading, draft, invalid, or incomplete versions are excluded.
- Only `kind=updater` artifacts with a non-empty Tauri signature are returned.
- When no compatible update exists, the response is `204 No Content`.
- Responses include an `ETag`; a matching `If-None-Match` may return `304 Not Modified`.

Example response:

```json
{
  "version": "1.9.4",
  "notes": "Release notes",
  "pub_date": "2026-08-29T15:19:00Z",
  "published_at": "2026-08-29T15:19:00Z",
  "force_update": false,
  "platforms": {
    "windows-x86_64": {
      "signature": "base64-tauri-signature",
      "url": "https://update.axlmc.org/dist/1.9.4/Axolotl.Launcher_1.9.4_x64-setup.nsis.zip"
    }
  }
}
```

`url` values always point to this server's `/dist/<version>/...` path, never directly to GitHub or CNB.

### `GET /dist/<version>/<filename>`

Downloads an updater package, installer, portable package, `.sig` file, or `manifest.json`.

The development Flask fallback supports `GET`, `HEAD`, conditional requests, and Range requests. In production, Caddy serves existing files directly with immutable caching.

When the artifact belongs to a GitHub-imported published version that has been removed by retention (newest three Release plus newest three Beta versions are retained), Flask returns:

```http
302 Found
Location: https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.8.0/setup.exe
```

Unknown files, missing files from retained versions, revoked versions, and manual uploads without GitHub import metadata return `404`.

## Version and Package APIs

### `GET /api/health`

Unauthenticated health check.

```json
{
  "status": "ok",
  "service": "axolotl-update-server"
}
```

### `GET /api/versions`

Returns all versions and artifact metadata, sorted by SemVer descending.

```json
{
  "versions": [
    {
      "version": "1.9.4",
      "channel": "release",
      "status": "published",
      "notes": "Release notes",
      "release_tag": "v1.9.4",
      "release_id": "123",
      "published_at": "2026-08-29T15:19:00+00:00",
      "minimum_version": null,
      "force_update": false,
      "artifacts": []
    }
  ]
}
```

### `GET /api/versions/<version>`

Returns one version in the same structure as an item from `/api/versions`. Unknown versions return `404` with code `version_not_found`.

### `GET /api/downloads/latest`

Returns the newest complete package version for a channel.

Query parameters:

| Parameter | Default | Description |
| --- | --- | --- |
| `channel` | `release` | `release` or `beta` |
| `include_updater` | `false` | Include updater artifacts when `true` |
| `include_revoked` | `false` | Include a revoked version; admin authentication required |

By default, the response includes only `installer` and `portable` artifacts. Signature-only files are never returned as package entries.

### `GET /api/downloads/<version>`

Returns the package catalog for an explicit version. It accepts the same `include_updater` and `include_revoked` query parameters. Unknown or unpublished versions return `404`.

Example response:

```json
{
  "version": "1.9.4",
  "channel": "release",
  "status": "published",
  "published_at": "2026-08-29T15:19:00Z",
  "force_update": false,
  "downloads": [
    {
      "id": 42,
      "kind": "installer",
      "platform": "windows",
      "architecture": "x86_64",
      "variant": "modern",
      "label": "Windows x64 Modern Installer",
      "display_name": "Windows x64 Modern Installer",
      "sort_order": 0,
      "filename": "Axolotl_Launcher_1.9.4_x64_modern-setup.exe",
      "url": "https://update.axlmc.org/dist/1.9.4/Axolotl_Launcher_1.9.4_x64_modern-setup.exe",
      "size": 50331648,
      "sha256": "...",
      "signature": null,
      "signature_filename": "Axolotl_Launcher_1.9.4_x64_modern-setup.exe.sig"
    }
  ]
}
```

No matching package version returns `204 No Content` for the `latest` endpoint.

## Maintenance Upload API

### `PUT /api/artifacts/<version>/<filename>`

Uploads one artifact for local or administrator maintenance. The normal GitHub Release workflow must not use this endpoint for large files.

Required headers:

```http
X-Upload-Token: <UPLOAD_TOKEN>
X-Axolotl-Platform: windows | macos | linux | <updater-platform>
X-Axolotl-Architecture: x86_64 | aarch64 | universal
```

Optional headers:

```http
X-Axolotl-Kind: updater | installer | portable | signature | manifest | other
X-Axolotl-Variant: modern
X-Axolotl-Display-Name: Windows x64 Modern Installer
X-Axolotl-Public: true | false
X-Axolotl-SHA256: <expected-sha256>
X-Axolotl-Size: <expected-byte-count>
X-Axolotl-Signature: <tauri-signature>
Content-Type: application/octet-stream
```

The request body is streamed to a temporary file. The server computes SHA-256 and size itself, validates safe filenames, then atomically stores the file at `DIST_ROOT/<version>/<filename>`.

- New upload: `201 Created`.
- Same path and same hash/size: `200 OK` with `idempotent: true`.
- Different content for an existing path: `409 Conflict`.
- Empty body: `400`.
- Exceeding `MAX_UPLOAD_SIZE`: `413`.

## GitHub Release Webhook

### `POST /api/webhook/release`

The Launcher workflow publishes a public GitHub Release first, then sends this lightweight webhook. The server validates metadata, streams all catalog files from GitHub, validates hashes and sizes, writes artifact records and a manifest, and only then publishes the version and channel pointer.

Required headers:

```http
Content-Type: application/json
X-Webhook-Timestamp: <unix-seconds>
X-Webhook-Signature: sha256=<HMAC_SHA256(WEBHOOK_SECRET, timestamp + "." + raw_body)>
```

Payload shape:

```json
{
  "event_id": "github-v1.9.4-123",
  "tag": "v1.9.4",
  "version": "1.9.4",
  "channel": "release",
  "force_update": false,
  "release": {
    "id": 123,
    "tag_name": "v1.9.4",
    "body": "Release notes",
    "published_at": "2026-08-29T15:19:00Z",
    "draft": false,
    "assets": [
      {
        "name": "update.tar.gz",
        "size": 134244283,
        "digest": "sha256:d6fdd896...",
        "browser_download_url": "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz"
      }
    ]
  },
  "catalog": {
    "version": "1.9.4",
    "files": [
      {
        "filename": "update.tar.gz",
        "size": 134244283,
        "sha256": "d6fdd896...",
        "downloadUrl": "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz"
      }
    ],
    "artifacts": [
      {
        "filename": "update.tar.gz",
        "platform": "linux-x86_64",
        "architecture": "x86_64",
        "kind": "updater",
        "signatureFilename": "update.tar.gz.sig"
      }
    ]
  }
}
```

Validation rules:

- `tag` must equal `v<version>`.
- `release.draft` must be `false` and `release.tag_name` must match `tag`.
- `release.id` and `release.published_at` are required; GitHub `published_at` is retained.
- `release` channel requires stable SemVer; `beta` channel requires a prerelease.
- Every `catalog.files` entry must match one unique GitHub asset by filename, size, SHA-256 digest, and exact download URL.
- Only HTTPS URLs for `github.com/Axolotl-Launcher/Axolotl/releases/download/<tag>/<filename>` are accepted.
- The server streams every physical file, including `.sig`, with size limits, timeout, retry, hash verification, fsync, and atomic rename.
- `catalog.artifacts` contains main `updater`, `installer`, and `portable` entries only. Signature-only physical files are stored separately as private `kind=signature` rows.
- Every updater artifact must reference a `.sig` file through `signatureFilename`. Its trimmed UTF-8 contents become `Artifact.signature`; the `.sig` SHA-256 is not used as the Tauri signature.
- Installers and portable packages may have signature attachments but do not require a Tauri signature.

Success:

```json
{
  "status": "published",
  "version": "1.9.4"
}
```

Repeated request with the same `event_id` and payload hash:

```json
{
  "status": "processed",
  "idempotent": true
}
```

The same event ID with a different payload returns `409 webhook_event_conflict`. Failed events may be retried with the same payload. Failed publication does not change the existing channel pointer and removes temporary files.

## Administration APIs

All endpoints in this section require `Authorization: Bearer <ADMIN_TOKEN>`.

### `PATCH /api/admin/versions/<version>/force-update`

Enables or disables `force_update` on an existing published or revoked version.
The JSON boolean is required; strings such as `"true"` and numbers are rejected.
`operator` defaults to `admin` and `reason` defaults to an empty string.

```bash
curl -X PATCH 'https://update.axlmc.org/api/admin/versions/1.9.4/force-update' \
  -H 'Authorization: Bearer <ADMIN_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"force_update":true,"operator":"release-admin","reason":"Urgent security update"}'
```

```json
{
  "version": "1.9.4",
  "status": "published",
  "force_update": true,
  "idempotent": false
}
```

Send `false` to disable the flag. Repeating the current value returns
`idempotent: true` and does not create another audit record. Changes update the
database and local manifest and record `force_update_enabled` or
`force_update_disabled` in the audit log. `/latest`, version and download APIs
reflect the flag; ETags on `/latest` and the download APIs change accordingly.
Publication time, version status, artifacts and channel pointers are preserved.
Updating a revoked version does not restore it. Clients still select versions
using the normal channel and SemVer rules; the flag tells Launcher to bypass
its Release waiting period.

Errors: `400 invalid_force_update` for missing/non-boolean values,
`400 invalid_admin_metadata` for invalid operator/reason, `401` for invalid
admin credentials, `404 version_not_found` for unknown versions,
`409 invalid_version_state` for unfinished versions, `409 manifest_unavailable`
for a missing/pruned local manifest, `409 invalid_manifest` for a mismatched
manifest, and `500 force_update_failed` for file/database update failures.
Handled update failures roll back the database and restore the previous manifest.
Cached static `/dist/.../manifest.json` copies may remain stale due to immutable
caching; use the dynamic `/latest` endpoint for current update policy.
This endpoint requires no new database migration.

### `POST /api/admin/versions/<version>/revoke`

Marks a published version as revoked and removes it from update selection.

Optional JSON body:

```json
{
  "operator": "release-admin",
  "reason": "Critical regression"
}
```

Response:

```json
{
  "status": "revoked",
  "version": "1.9.4"
}
```

### `POST /api/admin/versions/<version>/restore`

Restores a revoked version after verifying local artifact integrity. Updater artifacts must still have a valid Tauri signature.

Response:

```json
{
  "status": "published",
  "version": "1.9.4"
}
```

### `GET /api/admin/audit-logs`

Returns revoke and restore audit records:

```json
{
  "logs": [
    {
      "operator": "release-admin",
      "action": "revoke",
      "channel": "release",
      "version": "1.9.4",
      "reason": "Critical regression",
      "request_id": "...",
      "ip_address": "127.0.0.1",
      "created_at": "2026-09-05T12:00:00+00:00"
    }
  ]
}
```

### `GET /api/admin/stats`

Returns usage metrics for a time range of at most 30 days.

Query parameters:

| Parameter | Default | Description |
| --- | --- | --- |
| `start` | `end - 24 hours` | ISO-8601 UTC or offset timestamp |
| `end` | current UTC time | ISO-8601 UTC or offset timestamp |

The interval is `[start, end)`. `end` must be after `start`. Authentication is required.

Response dimensions are intentionally separate:

- `api_calls.groups`: grouped by client/request `version` and `channel`.
- `downloads.groups`: grouped by artifact `version`, `platform`, and `channel`, with request count and bytes.

Example:

```json
{
  "start": "2026-09-04T00:00:00Z",
  "end": "2026-09-05T00:00:00Z",
  "api_calls": {
    "total": 2,
    "groups": [
      {"version": "1.9.3", "channel": "release", "count": 1},
      {"version": "1.9.4", "channel": "beta", "count": 1}
    ]
  },
  "downloads": {
    "total_count": 2,
    "total_bytes": 200,
    "groups": [
      {"version": "1.9.4", "platform": "windows-x86_64", "channel": "release", "count": 1, "bytes": 123},
      {"version": "1.9.4", "platform": "linux-x86_64", "channel": "beta", "count": 1, "bytes": 77}
    ]
  }
}
```

`/latest` uses `X-Axolotl-Version` as the client version. Versioned API paths use their path version. For `/dist/<version>/<filename>`, the version comes from the path and platform is taken from the request header or artifact metadata. Missing or invalid channels are recorded as `unknown`.

## Operational Notes

- Caddy serves existing `/dist` files directly, so those static hits do not enter Flask usage logging. Use Caddy access logs for complete production download statistics.
- Flask fallback `/dist` requests are recorded in the application usage table.
- Apply database migrations before production startup: `flask --app wsgi:app db upgrade`.
- The default production Gunicorn command uses two sync workers, a 1800-second timeout, and a 30-second graceful timeout.
