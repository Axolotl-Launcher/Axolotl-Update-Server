import hashlib
import json
import mimetypes
import os
import re
import secrets
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

from flask import Blueprint, Response, current_app, jsonify, request
from semver import Version as SemVersion

from ..auth import require_admin, require_upload_token
from ..errors import ApiError
from ..extensions import db
from ..models import Artifact, AuditLog, ChannelPointer, UsageEvent, Version, WebhookEvent, utcnow
from ..services.github_release import download_file, prepare_catalog
from ..services.retention import prune_dist

api_bp = Blueprint("api", __name__, url_prefix="/api")
PLATFORMS = {"windows-x86_64", "linux-x86_64", "linux-aarch64", "darwin-x86_64", "darwin-aarch64"}
PACKAGE_PLATFORMS = {"windows", "macos", "linux"}
ARCHITECTURES = {"x86_64", "aarch64", "universal"}
ARTIFACT_KINDS = {"updater", "installer", "portable", "signature", "manifest", "other"}
SEMVER_RE = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$")


def parse_version(value: str) -> SemVersion:
    if not SEMVER_RE.fullmatch(value):
        raise ApiError("invalid_version", "Version must be a normalized SemVer.")
    try:
        return SemVersion.parse(value)
    except ValueError as exc:
        raise ApiError("invalid_version", "Version must be a normalized SemVer.") from exc


def json_version(v: Version):
    return {"version": v.version, "channel": v.channel, "status": v.status, "notes": v.notes or "", "release_tag": v.release_tag, "release_id": v.release_id, "published_at": v.published_at.isoformat() if v.published_at else None, "minimum_version": v.minimum_version, "force_update": bool(v.force_update), "artifacts": [{"platform": a.platform, "architecture": a.architecture, "kind": a.kind, "variant": a.variant, "display_name": a.display_name, "sort_order": a.sort_order, "is_public": a.is_public, "filename": a.filename, "relative_path": a.relative_path, "size": a.size, "sha256": a.sha256, "signature": a.signature, "signature_filename": a.signature_filename, "content_type": a.content_type} for a in v.artifacts]}


def validate_filename(filename: str):
    if not filename or "/" in filename or "\\" in filename or ".." in filename or any(ord(c) < 32 for c in filename):
        raise ApiError("invalid_filename", "Filename must be a single safe path component.")


def pointer_candidate(channel: str):
    versions = Version.query.filter_by(status="published").all()
    if channel == "release":
        versions = [v for v in versions if v.channel == "release" and not SemVersion.parse(v.version).prerelease]
    else:
        versions = [v for v in versions if (v.channel == "release" and not SemVersion.parse(v.version).prerelease) or (v.channel == "beta" and SemVersion.parse(v.version).prerelease)]
    versions = [
        v for v in versions
        if v.published_at is not None and any(
            a.platform in PLATFORMS
            and a.kind == "updater"
            and a.signature
            and a.relative_path == f"dist/{v.version}/{a.filename}"
            and (Path(current_app.config["DIST_ROOT"]) / v.version / a.filename).is_file()
            and (Path(current_app.config["DIST_ROOT"]) / v.version / a.filename).stat().st_size == a.size
            and hashlib.sha256((Path(current_app.config["DIST_ROOT"]) / v.version / a.filename).read_bytes()).hexdigest() == a.sha256
            for a in v.artifacts
        )
    ]
    return max(versions, key=lambda v: SemVersion.parse(v.version)).version if versions else None


@api_bp.get("/health")
def health():
    return jsonify({"status": "ok", "service": "axolotl-update-server"})


@api_bp.get("/versions")
def versions():
    items = sorted(Version.query.all(), key=lambda item: SemVersion.parse(item.version), reverse=True)
    return jsonify({"versions": [json_version(v) for v in items]})


@api_bp.get("/versions/<version>")
def version_detail(version):
    v = Version.query.filter_by(version=version).first()
    if not v:
        raise ApiError("version_not_found", "Version does not exist.", 404)
    return jsonify(json_version(v))


@api_bp.put("/artifacts/<version>/<filename>")
def upload_artifact(version, filename):
    require_upload_token()
    parse_version(version)
    validate_filename(filename)
    platform = request.headers.get("X-Axolotl-Platform", "")
    architecture = request.headers.get("X-Axolotl-Architecture", "") or ("aarch64" if "aarch64" in platform else "x86_64")
    kind = request.headers.get("X-Axolotl-Kind", "updater")
    variant = request.headers.get("X-Axolotl-Variant", "")
    display_name = request.headers.get("X-Axolotl-Display-Name", "")
    if kind not in ARTIFACT_KINDS:
        raise ApiError("invalid_artifact_kind", "Artifact kind is not supported.")
    if platform not in PLATFORMS and platform not in PACKAGE_PLATFORMS:
        raise ApiError("invalid_artifact_platform", "Artifact platform is not supported.")
    if architecture not in ARCHITECTURES:
        raise ApiError("invalid_artifact_architecture", "Artifact architecture is not supported.")
    if len(display_name) > 160 or len(variant) > 32:
        raise ApiError("invalid_artifact_metadata", "Artifact display metadata is too long.")
    hasher = hashlib.sha256()
    size = 0
    path = Path(current_app.config["DIST_ROOT"]) / version / filename
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as tmp:
        temp_name = tmp.name
        while chunk := request.stream.read(1024 * 1024):
            size += len(chunk)
            if size > current_app.config["MAX_CONTENT_LENGTH"]:
                Path(temp_name).unlink(missing_ok=True)
                raise ApiError("payload_too_large", "Artifact exceeds the configured upload limit.", 413)
            hasher.update(chunk)
            tmp.write(chunk)
    if size == 0:
        Path(temp_name).unlink(missing_ok=True)
        raise ApiError("empty_artifact", "Artifact body must not be empty.")
    digest = hasher.hexdigest()
    expected_hash = request.headers.get("X-Axolotl-SHA256")
    expected_size = request.headers.get("X-Axolotl-Size")
    if expected_hash and not secrets.compare_digest(expected_hash.lower(), digest):
        Path(temp_name).unlink(missing_ok=True)
        raise ApiError("hash_mismatch", "Uploaded content hash does not match.")
    if expected_size:
        try:
            size_matches = int(expected_size) == size
        except ValueError as exc:
            Path(temp_name).unlink(missing_ok=True)
            raise ApiError("invalid_size", "X-Axolotl-Size must be an integer.") from exc
        if not size_matches:
            Path(temp_name).unlink(missing_ok=True)
            raise ApiError("size_mismatch", "Uploaded content size does not match.")
    v = Version.query.filter_by(version=version).first()
    if not v:
        v = Version(version=version, channel="beta" if "-" in version else "release", status="uploading")
        db.session.add(v); db.session.flush()
    existing = Artifact.query.filter_by(relative_path=f"dist/{version}/{filename}").first()
    if existing:
        if existing.sha256 == digest and existing.size == size:
            if not path.exists():
                os.link(temp_name, path)
            elif path.stat().st_size != size or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
                Path(temp_name).unlink(missing_ok=True)
                raise ApiError("artifact_corrupt", "The existing artifact failed integrity verification.", 409)
            Path(temp_name).unlink(missing_ok=True)
            return jsonify({"artifact": artifact_json(existing), "idempotent": True})
        Path(temp_name).unlink(missing_ok=True)
        raise ApiError("artifact_exists", "An artifact with a different hash already exists.", 409)
    duplicate_metadata = Artifact.query.filter_by(
        version_id=v.id, kind=kind, platform=platform, architecture=architecture, variant=variant
    ).first()
    if duplicate_metadata and kind != "signature":
        Path(temp_name).unlink(missing_ok=True)
        raise ApiError("duplicate_artifact", "Artifact metadata already exists for this version.", 409)
    if v.status == "published":
        Path(temp_name).unlink(missing_ok=True)
        raise ApiError("version_immutable", "Published versions cannot receive new artifacts.", 409)
    try:
        os.link(temp_name, path)
    except FileExistsError:
        Path(temp_name).unlink(missing_ok=True)
        raise ApiError("artifact_exists", "An artifact file already exists.", 409) from None
    finally:
        Path(temp_name).unlink(missing_ok=True)
    artifact = Artifact(version_id=v.id, platform=platform, architecture=architecture, kind=kind, variant=variant, display_name=display_name or filename, is_public=request.headers.get("X-Axolotl-Public", "true").lower() == "true", filename=filename, relative_path=f"dist/{version}/{filename}", size=size, sha256=digest, signature=request.headers.get("X-Axolotl-Signature"), content_type=request.headers.get("Content-Type") or mimetypes.guess_type(filename)[0] or "application/octet-stream")
    db.session.add(artifact); db.session.commit()
    return jsonify({"artifact": artifact_json(artifact), "idempotent": False}), 201


def artifact_json(a):
    return {"version": a.version_ref.version, "platform": a.platform, "architecture": a.architecture, "kind": a.kind, "variant": a.variant, "display_name": a.display_name, "is_public": a.is_public, "filename": a.filename, "size": a.size, "sha256": a.sha256, "signature": a.signature, "url": f"{current_app.config['PUBLIC_BASE_URL']}/{a.relative_path}"}


def download_json(a):
    return {"id": a.id, "kind": a.kind, "platform": a.platform, "architecture": a.architecture, "variant": a.variant, "label": a.display_name or a.filename, "display_name": a.display_name or a.filename, "sort_order": a.sort_order, "filename": a.filename, "url": f"{current_app.config['PUBLIC_BASE_URL']}/{a.relative_path}", "size": a.size, "sha256": a.sha256, "signature": a.signature, "signature_filename": a.signature_filename}


def eligible_versions(channel: str):
    versions = Version.query.filter_by(status="published").all()
    if channel == "release":
        return [v for v in versions if v.channel == "release" and not SemVersion.parse(v.version).prerelease]
    return [v for v in versions if (v.channel == "release" and not SemVersion.parse(v.version).prerelease) or (v.channel == "beta" and SemVersion.parse(v.version).prerelease)]


def download_payload(v: Version, include_updater: bool = False):
    kind_order = {"installer": 0, "portable": 1, "updater": 2}
    platform_order = {"windows": 0, "macos": 1, "linux": 2}
    artifacts = [
        a for a in v.artifacts
        if a.is_public
        and a.kind != "signature"
        and (include_updater or a.kind != "updater")
        and a.relative_path == f"dist/{v.version}/{a.filename}"
        and (Path(current_app.config["DIST_ROOT"]) / v.version / a.filename).is_file()
        and (Path(current_app.config["DIST_ROOT"]) / v.version / a.filename).stat().st_size == a.size
    ]
    artifacts.sort(key=lambda a: (platform_order.get(a.platform, 99), kind_order.get(a.kind, 99), a.sort_order, a.display_name or a.filename))
    return {"version": v.version, "channel": v.channel, "status": v.status, "published_at": v.published_at.isoformat().replace("+00:00", "Z") if v.published_at else None, "force_update": bool(v.force_update), "downloads": [download_json(a) for a in artifacts]}


def select_download_version(channel: str, include_updater: bool = False):
    allowed = {"installer", "portable", "updater"} if include_updater else {"installer", "portable"}
    candidates = [
        v for v in eligible_versions(channel)
        if v.published_at is not None
        and any(
            a.is_public and a.kind in allowed
            and a.relative_path == f"dist/{v.version}/{a.filename}"
            and (Path(current_app.config["DIST_ROOT"]) / v.version / a.filename).is_file()
            and (Path(current_app.config["DIST_ROOT"]) / v.version / a.filename).stat().st_size == a.size
            for a in v.artifacts
        )
    ]
    return max(candidates, key=lambda v: SemVersion.parse(v.version)) if candidates else None


@api_bp.get("/downloads/latest")
def downloads_latest():
    channel = request.args.get("channel", "release")
    if channel not in ("release", "beta"):
        raise ApiError("invalid_channel", "Only release and beta channels are supported.")
    include_updater = request.args.get("include_updater", "false").lower() == "true"
    include_revoked = request.args.get("include_revoked", "false").lower() == "true"
    if include_revoked:
        require_admin()
    version = select_download_version(channel, include_updater)
    if not version:
        return Response(status=204)
    payload = download_payload(version, include_updater)
    body = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    etag = hashlib.sha256(body.encode()).hexdigest()
    response = jsonify(payload)
    response.headers["ETag"] = etag
    response.headers["Cache-Control"] = "no-cache, max-age=0, must-revalidate"
    return response


@api_bp.get("/downloads/<version>")
def downloads_version(version):
    parse_version(version)
    item = Version.query.filter_by(version=version).first()
    include_revoked = request.args.get("include_revoked", "false").lower() == "true"
    if include_revoked:
        require_admin()
    if not item or (item.status != "published" and not (include_revoked and item.status == "revoked")):
        raise ApiError("version_not_found", "Published version does not exist.", 404)
    include_updater = request.args.get("include_updater", "false").lower() == "true"
    payload = download_payload(item, include_updater)
    body = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    response = jsonify(payload)
    response.headers["ETag"] = hashlib.sha256(body.encode()).hexdigest()
    response.headers["Cache-Control"] = "no-cache, max-age=0, must-revalidate"
    return response


@api_bp.post("/webhook/release")
def release_webhook():
    raw = request.get_data(cache=True)
    timestamp = request.headers.get("X-Webhook-Timestamp", "")
    signature = request.headers.get("X-Webhook-Signature", "")
    try:
        ts = int(timestamp)
    except ValueError as exc:
        raise ApiError("invalid_webhook_timestamp", "Webhook timestamp is required.") from exc
    if abs(datetime.now(timezone.utc).timestamp() - ts) > current_app.config["WEBHOOK_MAX_AGE_SECONDS"]:
        raise ApiError("stale_webhook", "Webhook timestamp is outside the accepted window.")
    import hmac
    if not current_app.config["WEBHOOK_SECRET"]:
        raise ApiError("auth_not_configured", "Webhook authentication is not configured.", 503)
    expected = hmac.new(current_app.config["WEBHOOK_SECRET"].encode(), (timestamp + ".").encode() + raw, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(signature.removeprefix("sha256="), expected):
        raise ApiError("invalid_webhook_signature", "Webhook signature is invalid.", 401)
    payload = request.get_json(silent=True) or {}
    event_id = payload.get("event_id")
    if not event_id:
        raise ApiError("invalid_webhook", "event_id is required.")
    phash = hashlib.sha256(raw).hexdigest()
    existing_event = db.session.get(WebhookEvent, event_id)
    event = existing_event
    if existing_event:
        if existing_event.payload_hash != phash:
            raise ApiError("webhook_event_conflict", "event_id was already used with a different payload.", 409)
        if existing_event.status == "processed":
            return jsonify({"status": "processed", "idempotent": True})
        existing_event.status = "received"
        existing_event.error_message = None
        db.session.commit()
    if event is None:
        event = WebhookEvent(event_id=event_id, payload_hash=phash)
        db.session.add(event)
    staging = None
    moved_files = []
    manifest_path = None
    manifest_temp = None
    manifest_written = False
    try:
        version = payload.get("version", "")
        tag = payload.get("tag", "")
        channel = payload.get("channel", "")
        parsed = parse_version(version)
        release = payload.get("release")
        catalog = payload.get("catalog")
        if not isinstance(release, dict) or not isinstance(catalog, dict):
            raise ApiError("invalid_webhook", "release and catalog are required.")
        if tag != f"v{version}" or channel not in ("release", "beta"):
            raise ApiError("invalid_webhook", "Version, tag, and channel are inconsistent.")
        if channel == "release" and parsed.prerelease:
            raise ApiError("prerelease_release", "Release channel cannot publish prerelease versions.")
        if channel == "beta" and not parsed.prerelease:
            raise ApiError("stable_beta", "Beta channel requires a prerelease version.")
        if not isinstance(payload.get("force_update", False), bool):
            raise ApiError("invalid_force_update", "force_update must be a boolean.")
        if not release.get("id"):
            raise ApiError("invalid_github_release", "GitHub Release id is required.")
        published_raw = release.get("published_at")
        if not isinstance(published_raw, str):
            raise ApiError("invalid_github_release", "GitHub Release published_at is required.")
        try:
            published_at = datetime.fromisoformat(published_raw.replace("Z", "+00:00"))
        except ValueError as exc:
            raise ApiError("invalid_github_release", "GitHub Release published_at is invalid.") from exc
        if published_at.tzinfo is None:
            published_at = published_at.replace(tzinfo=timezone.utc)
        artifacts, files = prepare_catalog(release, catalog, version, tag)
        if not artifacts:
            raise ApiError("missing_artifacts", "At least one complete artifact is required.")

        existing_version = Version.query.filter_by(version=version).first()
        if existing_version and existing_version.status == "published":
            raise ApiError("version_immutable", "Published versions cannot be imported again.", 409)
        v = existing_version or Version(version=version, channel=channel, status="uploading")
        v.channel, v.notes, v.release_tag, v.release_id = channel, release.get("body", ""), tag, str(release["id"])
        v.force_update, v.published_at, v.status = bool(payload.get("force_update", False)), published_at, "uploading"
        db.session.add(v)
        db.session.flush()

        staging = Path(current_app.config["DIST_ROOT"]) / ".staging" / event_id
        staging.mkdir(parents=True, exist_ok=True)
        for filename, info in files.items():
            validate_filename(filename)
            download_file(info["url"], staging / filename, info["size"], info["sha256"])

        by_filename = {a.get("filename"): a for a in artifacts if isinstance(a, dict)}
        if len(by_filename) != len(artifacts):
            raise ApiError("duplicate_artifact", "Catalog artifact filenames must be unique.")
        seen_updater_platforms = set()
        primary_rows = []
        referenced_signatures = set()
        for a_data in artifacts:
            artifact_filename = a_data.get("filename")
            if not isinstance(artifact_filename, str) or artifact_filename not in files:
                raise ApiError("invalid_catalog", "Every artifact must reference a catalog file.")
            validate_filename(artifact_filename)
            kind = a_data.get("kind", "updater")
            platform = a_data.get("platform")
            architecture = a_data.get("architecture") or ("aarch64" if "aarch64" in (platform or "") else "x86_64")
            variant = a_data.get("variant", "")
            if kind not in ARTIFACT_KINDS - {"signature", "manifest", "other"}:
                raise ApiError("invalid_artifact_kind", "Catalog artifact kind is not supported.")
            if platform not in (PLATFORMS | PACKAGE_PLATFORMS) or architecture not in ARCHITECTURES:
                raise ApiError("invalid_artifact_platform", "Artifact platform or architecture is invalid.")
            signature_filename: str | None = None
            tauri_signature: str | None = None
            if kind == "updater":
                if platform not in PLATFORMS or platform in seen_updater_platforms:
                    raise ApiError("duplicate_artifact", "Updater platforms must be unique.")
                seen_updater_platforms.add(platform)
                signature_filename = a_data.get("signatureFilename") or a_data.get("signature_filename")
                if not isinstance(signature_filename, str) or signature_filename not in files:
                    raise ApiError("missing_signature", "Updater artifacts require a catalog signature file.")
                referenced_signatures.add(signature_filename)
                try:
                    tauri_signature = (staging / signature_filename).read_text(encoding="utf-8").strip()
                except (OSError, UnicodeError) as exc:
                    raise ApiError("invalid_signature", "Updater signature file is invalid.") from exc
                if not tauri_signature:
                    raise ApiError("invalid_signature", "Updater signature file is empty.")
            else:
                signature_filename = a_data.get("signatureFilename") or a_data.get("signature_filename")
                if signature_filename is not None and not isinstance(signature_filename, str):
                    raise ApiError("invalid_signature", "Artifact signature filename is invalid.")
                if signature_filename:
                    if signature_filename not in files:
                        raise ApiError("invalid_signature", "Artifact signature file is not in the catalog.")
                    referenced_signatures.add(signature_filename)
            if len(str(variant)) > 32 or len(str(a_data.get("display_name", ""))) > 160:
                raise ApiError("invalid_artifact_metadata", "Artifact display metadata is too long.")
            info = files[artifact_filename]
            primary_rows.append({"data": a_data, "filename": artifact_filename, "kind": kind, "platform": platform, "architecture": architecture, "variant": variant, "signature_filename": signature_filename, "signature": tauri_signature, "info": info})

        # Move only validated downloads into the public version directory.
        version_dir = Path(current_app.config["DIST_ROOT"]) / version
        version_dir.mkdir(parents=True, exist_ok=True)
        for filename in files:
            destination = version_dir / filename
            if destination.exists():
                if destination.stat().st_size != files[filename]["size"] or hashlib.sha256(destination.read_bytes()).hexdigest() != files[filename]["sha256"]:
                    raise ApiError("artifact_exists", "A stored artifact has different content.", 409)
                (staging / filename).unlink(missing_ok=True)
            else:
                os.replace(staging / filename, destination)
                moved_files.append(destination)

        existing_rows = {a.filename: a for a in v.artifacts}
        for row in primary_rows:
            info = row["info"]
            artifact = existing_rows.get(row["filename"])
            if artifact and (artifact.sha256 != info["sha256"] or artifact.size != info["size"]):
                raise ApiError("artifact_exists", "Artifact metadata has changed.", 409)
            if not artifact:
                artifact = Artifact(version_id=v.id, filename=row["filename"], relative_path=f"dist/{version}/{row['filename']}")
                db.session.add(artifact)
            data = row["data"]
            is_public = data.get("is_public", True)
            if not isinstance(is_public, bool):
                raise ApiError("invalid_artifact_metadata", "is_public must be a boolean.")
            artifact.platform, artifact.architecture, artifact.kind = row["platform"], row["architecture"], row["kind"]
            artifact.variant, artifact.display_name = row["variant"], data.get("display_name") or row["filename"]
            artifact.sort_order, artifact.is_public = int(data.get("sort_order", 0)), is_public
            artifact.size, artifact.sha256 = info["size"], info["sha256"]
            artifact.signature_filename, artifact.signature = row["signature_filename"], row["signature"]
            artifact.content_type = mimetypes.guess_type(row["filename"])[0] or "application/octet-stream"
        for filename, info in files.items():
            if filename in by_filename:
                continue
            if filename not in referenced_signatures:
                raise ApiError("invalid_catalog", "Every catalog file must be an artifact or signature attachment.")
            sig = Artifact.query.filter_by(version_id=v.id, filename=filename).first()
            if not sig:
                sig = Artifact(version_id=v.id, filename=filename, relative_path=f"dist/{version}/{filename}")
                db.session.add(sig)
            sig.platform, sig.architecture, sig.kind, sig.variant = "", "", "signature", ""
            sig.display_name, sig.is_public = filename, False
            sig.size, sig.sha256 = info["size"], info["sha256"]
            sig.signature, sig.signature_filename = None, None
            sig.content_type = "application/octet-stream"
        db.session.flush()
        db.session.expire(v, ["artifacts"])
        manifest = {"version": v.version, "notes": v.notes or "", "pub_date": published_at.astimezone(timezone.utc).isoformat().replace("+00:00", "Z"), "published_at": published_at.astimezone(timezone.utc).isoformat().replace("+00:00", "Z"), "force_update": bool(v.force_update), "platforms": {a.platform: {"signature": a.signature, "url": f"{current_app.config['PUBLIC_BASE_URL']}/{a.relative_path}"} for a in v.artifacts if a.kind == "updater" and a.signature}}
        if not manifest["platforms"]:
            raise ApiError("missing_artifacts", "At least one signed updater artifact is required.")
        manifest_path = Path(current_app.config["DIST_ROOT"]) / version / "manifest.json"
        with tempfile.NamedTemporaryFile(dir=manifest_path.parent, mode="w", encoding="utf-8", delete=False) as tmp:
            json.dump(manifest, tmp, indent=2)
            tmp.flush(); os.fsync(tmp.fileno())
            manifest_temp = Path(tmp.name)
        os.replace(manifest_temp, manifest_path)
        manifest_written = True
        v.status = "published"
        pointer = db.session.get(ChannelPointer, channel) or ChannelPointer(channel=channel)
        pointer.current_version = pointer_candidate(channel)
        db.session.add(pointer)
        event.status, event.processed_at = "processed", utcnow()
        db.session.commit()
        try:
            prune_dist(
                current_app.config.get("RELEASE_RETENTION_COUNT", 3),
                current_app.config.get("BETA_RETENTION_COUNT", 3),
            )
        except Exception:
            current_app.logger.exception("Artifact retention cleanup failed")
        if staging:
            import shutil
            shutil.rmtree(staging, ignore_errors=True)
        return jsonify({"status": "published", "version": version})
    except ApiError as exc:
        db.session.rollback()
        if manifest_temp:
            manifest_temp.unlink(missing_ok=True)
        for path in moved_files:
            path.unlink(missing_ok=True)
        if manifest_written and manifest_path:
            manifest_path.unlink(missing_ok=True)
        if staging:
            import shutil
            shutil.rmtree(staging, ignore_errors=True)
        event = db.session.get(WebhookEvent, event_id) or event
        event.status, event.error_message = "failed", exc.message
        db.session.add(event); db.session.commit(); raise
    except (ValueError, OSError) as exc:
        db.session.rollback()
        if manifest_temp:
            manifest_temp.unlink(missing_ok=True)
        for path in moved_files:
            path.unlink(missing_ok=True)
        if manifest_written and manifest_path:
            manifest_path.unlink(missing_ok=True)
        if staging:
            import shutil
            shutil.rmtree(staging, ignore_errors=True)
        event = db.session.get(WebhookEvent, event_id) or event
        event.status, event.error_message = "failed", "Webhook validation or publication failed."
        db.session.add(event); db.session.commit()
        raise ApiError("webhook_failed", "Webhook validation or publication failed.") from exc


def _admin_change(version, restore=False):
    require_admin()
    v = Version.query.filter_by(version=version).first()
    if not v: raise ApiError("version_not_found", "Version does not exist.", 404)
    body = request.get_json(silent=True) or {}; operator = body.get("operator", "admin"); reason = body.get("reason", "")
    if restore:
        for a in v.artifacts:
            path = Path(current_app.config["DIST_ROOT"]) / version / a.filename
            if not path.exists() or path.stat().st_size != a.size or hashlib.sha256(path.read_bytes()).hexdigest() != a.sha256 or (a.kind == "updater" and not a.signature):
                raise ApiError("artifact_validation_failed", "Cannot restore with incomplete artifacts.")
        v.status, v.revoked_at, v.revoke_reason = "published", None, None
        action = "restore"
    else:
        v.status, v.revoked_at, v.revoke_reason = "revoked", utcnow(), reason
        action = "revoke"
    pointer = db.session.get(ChannelPointer, v.channel)
    if restore or (pointer and pointer.current_version == version):
        pointer = pointer or ChannelPointer(channel=v.channel)
        pointer.current_version = pointer_candidate(v.channel)
        db.session.add(pointer)
    db.session.add(AuditLog(operator=operator, action=action, channel=v.channel, version=version, reason=reason, request_id=request.environ.get("request_id"), ip_address=request.remote_addr))
    db.session.commit()
    return jsonify({"status": v.status, "version": version})


@api_bp.post("/admin/versions/<version>/revoke")
def revoke(version): return _admin_change(version)


@api_bp.post("/admin/versions/<version>/restore")
def restore(version): return _admin_change(version, True)


@api_bp.get("/admin/audit-logs")
def audit_logs():
    require_admin()
    return jsonify({"logs": [{"operator": x.operator, "action": x.action, "channel": x.channel, "version": x.version, "reason": x.reason, "request_id": x.request_id, "ip_address": x.ip_address, "created_at": x.created_at.isoformat()} for x in AuditLog.query.order_by(AuditLog.created_at.desc()).all()]})


def _stats_datetime(value: str, default: datetime) -> datetime:
    if not value:
        return default
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise ApiError("invalid_stats_range", "start and end must be ISO-8601 timestamps.") from exc
    return (parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)).astimezone(timezone.utc)


@api_bp.get("/admin/stats")
def usage_stats():
    require_admin()
    end = _stats_datetime(request.args.get("end", ""), utcnow())
    start = _stats_datetime(request.args.get("start", ""), end - timedelta(days=1))
    if end <= start:
        raise ApiError("invalid_stats_range", "end must be after start.")
    if end - start > timedelta(days=30):
        raise ApiError("invalid_stats_range", "The maximum statistics range is 30 days.")
    events = UsageEvent.query.filter(UsageEvent.occurred_at >= start, UsageEvent.occurred_at < end).all()
    api_events = [event for event in events if event.event_type == "api"]
    downloads = [event for event in events if event.event_type == "download"]
    by_channel: dict[str, int] = {}
    for event in api_events:
        by_channel[event.channel] = by_channel.get(event.channel, 0) + 1
    return jsonify({
        "start": start.isoformat().replace("+00:00", "Z"),
        "end": end.isoformat().replace("+00:00", "Z"),
        "api_calls": {"total": len(api_events), "by_channel": by_channel},
        "downloads": {"count": len(downloads), "bytes": sum(event.bytes_sent or 0 for event in downloads)},
    })
