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
- New Release and Beta imports use metadata-only storage and do not require local
  package files for selection. Unconverted legacy versions still require
  local file availability. Response URLs keep their `/dist` paths;
  configuring delivery of those files is a separate deployment task.
- An updater can advertise multiple `targetPlatforms`; universal macOS updater artifacts are returned for both
  `darwin-aarch64` and `darwin-x86_64`.
- When no compatible update exists, the response is `204 No Content`.
- Responses include an `ETag`; a matching `If-None-Match` may return `304 Not Modified`.
- Responses are cached at the origin for `LATEST_CACHE_SECONDS` (default 30),
  separately by channel, platform and current version. Successful version changes
  invalidate all workers through a shared revision file. This endpoint performs
  no usage/statistics writes. Only Gunicorn emits HTTP access log lines.
- Successful `200`, `204` and `304` responses include cache policy and `Vary` for
  all three X-Axolotl variant headers. Query-only requests permit short shared
  caching via `s-maxage` and `Cloudflare-CDN-Cache-Control`. Header-based requests
  default to private/no-store at the CDN because Cloudflare does not key on
  arbitrary `Vary` headers. Enable `LATEST_CDN_HEADER_CACHE_KEY=true` only after
  configuring a native Cloudflare cache key containing all three headers plus
  all query parameters. See README for native Cache Rules. Errors and requests
  with Authorization/Cookie use no-store. Caddy must not override these headers.

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

The development Flask fallback supports `GET`, `HEAD`, conditional requests, and Range requests. In production, Caddy serves existing files directly with immutable caching. New metadata-only Release and Beta imports create only `manifest.json` under this path. The Flask fallback returns `404` for their absent payload/signature assets and never redirects them to GitHub; external delivery is configured separately.

For unconverted legacy local versions only, when a GitHub-imported published version has been removed by retention, Flask returns:

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

Both download catalog endpoints support cross-origin `GET`, `HEAD`, and `OPTIONS`
with `Access-Control-Allow-Origin: *`, including empty and error responses. No
cross-origin credentials are allowed. Cloudflare challenge/WAF rules must allow
these public catalog requests to reach Flask; an edge challenge response cannot
be fixed by the application CORS headers.

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
      "target_platforms": ["windows"],
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

This endpoint is retired for both Release and Beta. After checking the upload
token and SemVer, it returns `409 release_metadata_only` without reading/storing
package bytes. Use the release webhook for metadata publication.

Required headers:

```http
X-Upload-Token: <UPLOAD_TOKEN>
```

Missing/invalid tokens return `401`; invalid SemVer returns `400`. Authenticated
valid-version requests return `409 release_metadata_only` for both channels.

## GitHub Release Webhook

### `POST /api/webhook/release`

The Launcher workflow publishes a public GitHub Release first, then sends this lightweight webhook. For both `channel=release` and `channel=beta`, the server validates all catalog/asset metadata without downloading packages, temporarily fetches only updater signatures (64 KiB limit), and writes artifact metadata plus a local manifest before publishing. Signature temporary files are deleted after success/failure.

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
        "targetPlatforms": ["linux-x86_64"],
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
- Release and Beta main package hashes/sizes are validated against the signed webhook's
  GitHub asset metadata; bytes are not downloaded or independently rehashed.
  Updater signatures are fetched privately, hash/size checked and stored as text
  in the database, with no permanent `.sig` under `/dist`. Installers' `.sig`
  attachments are cataloged without downloading their content.
- `catalog.artifacts` contains main `updater`, `installer`, and `portable` entries only. Signature-only physical files are stored separately as private `kind=signature` rows.
- Updater artifacts may provide `targetPlatforms`, an optional non-empty array of updater platform identifiers. The
  primary `platform` must be present in the list, and effective target platforms must not overlap between updater
  artifacts in the same release. `target_platforms` is accepted as a snake_case alias.
- Import validation also compares incoming updater targets with updater artifacts already stored for the same
  unpublished version, excluding files being replaced by the incoming catalog.
- Universal macOS updater artifacts use `platform: "darwin-aarch64"` with
  `targetPlatforms: ["darwin-aarch64", "darwin-x86_64"]`.
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

### `DELETE /api/admin/versions/<version>`

Deletes an Update Server version so the same SemVer can be published again.
Accepts any version state, including revoked, failed, and uploading versions
whose import operation is no longer running. Optional JSON body:

```bash
curl -X DELETE 'https://update.axlmc.org/api/admin/versions/1.9.4' \
  -H 'Authorization: Bearer <ADMIN_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"operator":"release-admin","reason":"Remove broken release for republishing"}'
```

`operator` defaults to `admin`; `reason` defaults to an empty string. The server:

- Deletes the Version and all associated Artifact database records.
- Removes `DIST_ROOT/<version>/` (packages, signatures and manifest), plus any
  staging directories identified by the associated webhook events.
- Deletes webhook events explicitly associated with the version so their IDs
  can be reused with a new payload. For older events without a version field,
  only an exact `github-<release_tag>-<release_id>` match is removed. Legacy
  custom event IDs cannot be reliably associated; use a new event ID for those.
- Recomputes both Release and Beta pointers using available published versions.
- Writes a `delete_version` audit record; existing audit and usage history is retained.

```json
{
  "status": "deleted",
  "version": "1.9.4",
  "idempotent": false,
  "deleted_artifacts": 15,
  "deleted_webhook_events": 1,
  "channel_pointers": {"release": "1.9.3", "beta": "1.9.3"},
  "files_cleanup_pending": false
}
```

`deleted_artifacts` counts physical-file database records, including signature
attachments; it is not necessarily the main-package count. A pointer is `null`
if no eligible local version remains. Existing-version deletion returns `200`;
unknown versions return `404 version_not_found`.

Files are first moved to a sibling `.<dist-directory-name>-deletions` directory
outside `DIST_ROOT`; if the database transaction fails they are moved back.
The quarantine is removed after commit. If cleanup fails, the version is already
deleted but `files_cleanup_pending` is `true`. Retry the same DELETE to remove
remaining files; successful cleanup returns `idempotent: true`. A subsequent
DELETE after cleanup is finished returns `404`. Finish pending cleanup before
republishing. If rollback itself fails, `500 version_restore_failed` requires
manual recovery of the quarantined files.

Other errors: `400 invalid_version`, `400 invalid_admin_metadata`, `401` for
invalid credentials, `409 version_busy` for a concurrent same-version operation,
`409 unsafe_storage_path` for unsafe directory links, `409 version_cleanup_pending`
for leftover quarantine conflicting with an existing version,
`500 version_delete_failed`, or `500 version_cleanup_failed`.
Mutating operations use a shared instance-directory file lock across workers
on the same server; separate servers need shared coordination.

Apply migration `0007_webhook_event_version` before deploying this endpoint.
Deleting a version does not delete GitHub/CNB Releases or purge CDN/browser
caches. Replacing content at the same immutable URL may require CDN cache purge;
already-cached client copies cannot be recalled by this API.

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

Restores a revoked version after verifying local artifact integrity for unconverted legacy versions, or stored artifact metadata for metadata-only Release/Beta versions. Updater artifacts must still have a non-empty Tauri signature. Restoring does not download payloads.

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
      "ip_address": "203.0.113.12",
      "created_at": "2026-09-05T12:00:00+00:00"
    }
  ]
}
```

### `GET /api/admin/stats`

Legacy read-only history endpoint for ranges of at most 30 days. Usage collection
is disabled. Run `flask --app wsgi:app purge-usage` with the server stopped to
delete existing statistics and VACUUM SQLite; after cleanup, this endpoint
returns empty groups and zero totals. It remains available for compatibility.

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

Historical rows used client/request version for API calls and artifact version,
platform and channel for downloads. No new rows are collected.

## Operational Notes

- Migration `0009_version_artifact_storage` adds the storage mode. Existing
  versions remain `local` with files untouched until `purge-dist-assets` runs;
  new Release and Beta webhook imports use `metadata`. Their manifests are retained beyond the three-version
  physical-file window. Force Update, revoke/restore and delete still work on
  metadata-only versions. No new Release asset redirects are introduced.
- To convert old Release and Beta versions and delete local packages/signature
  files, stop the server and run `flask --app wsgi:app purge-dist-assets`.
  `--dry-run` validates/lists without deleting; `--version <semver>` restricts
  the scope. Metadata and manifest are kept, and partial file cleanup is safely
  retryable. See README for deployment commands and recovery details.
- Admin audit `ip_address`, rate limiting and Gunicorn access logs use
  the resolved client IP behind trusted Cloudflare/Caddy proxies. Forwarded
  headers from untrusted socket peers are ignored. Configure
  `TRUSTED_PROXY_CIDRS` for a different proxy topology; see README's Cloudflare
  deployment instructions. Duplicate Flask JSON access logging is disabled.
  Historical audit IPs remain unchanged.
- Caddy serves existing `/dist` files directly. Usage/database statistics are
  disabled for both Flask API calls and download fallback requests.
- Apply database migrations before production startup: `flask --app wsgi:app db upgrade`.
- Migration `0008_artifact_target_platforms` backfills existing universal macOS updater artifacts to both
  `darwin-aarch64` and `darwin-x86_64` unless another updater already claims Intel macOS. Stop the old service
  workers before running the migration so no legacy worker can insert a universal updater after the backfill.
- A manifest that would map one target platform to two different updater artifacts fails with
  `500 ambiguous_updater_platform` instead of silently choosing one.
- The default production Gunicorn command uses two sync workers, a 1800-second timeout, and a 30-second graceful timeout.
