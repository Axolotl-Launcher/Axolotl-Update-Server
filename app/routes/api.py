import hashlib
import json
import mimetypes
import re
import secrets
import tempfile
from datetime import datetime, timezone
from pathlib import Path

from flask import Blueprint, current_app, jsonify, request
from semver import Version as SemVersion

from ..auth import require_admin, require_upload_token
from ..errors import ApiError
from ..extensions import db
from ..models import Artifact, AuditLog, ChannelPointer, Version, WebhookEvent, utcnow

api_bp = Blueprint("api", __name__, url_prefix="/api")
PLATFORMS = {"windows-x86_64", "linux-x86_64", "linux-aarch64", "darwin-x86_64", "darwin-aarch64"}
SEMVER_RE = re.compile(r"^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$")


def parse_version(value: str) -> SemVersion:
    if not SEMVER_RE.fullmatch(value):
        raise ApiError("invalid_version", "Version must be a normalized SemVer.")
    try:
        return SemVersion.parse(value)
    except ValueError as exc:
        raise ApiError("invalid_version", "Version must be a normalized SemVer.") from exc


def json_version(v: Version):
    return {"version": v.version, "channel": v.channel, "status": v.status, "notes": v.notes or "", "release_tag": v.release_tag, "release_id": v.release_id, "published_at": v.published_at.isoformat() if v.published_at else None, "minimum_version": v.minimum_version, "force_update": bool(v.force_update), "artifacts": [{"platform": a.platform, "architecture": a.architecture, "filename": a.filename, "relative_path": a.relative_path, "size": a.size, "sha256": a.sha256, "signature": a.signature, "content_type": a.content_type} for a in v.artifacts]}


def validate_filename(filename: str):
    if not filename or "/" in filename or "\\" in filename or ".." in filename or any(ord(c) < 32 for c in filename):
        raise ApiError("invalid_filename", "Filename must be a single safe path component.")


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
    if not request.content_length and not request.get_data(cache=True):
        raise ApiError("empty_artifact", "Artifact body must not be empty.")
    data = request.get_data(cache=True)
    digest = hashlib.sha256(data).hexdigest()
    size = len(data)
    expected_hash = request.headers.get("X-Axolotl-SHA256")
    expected_size = request.headers.get("X-Axolotl-Size")
    if expected_hash and not secrets.compare_digest(expected_hash.lower(), digest):
        raise ApiError("hash_mismatch", "Uploaded content hash does not match.")
    if expected_size:
        try:
            size_matches = int(expected_size) == size
        except ValueError as exc:
            raise ApiError("invalid_size", "X-Axolotl-Size must be an integer.") from exc
        if not size_matches:
            raise ApiError("size_mismatch", "Uploaded content size does not match.")
    v = Version.query.filter_by(version=version).first()
    if not v:
        v = Version(version=version, channel="beta" if "-" in version else "release", status="uploading")
        db.session.add(v); db.session.flush()
    path = Path(current_app.config["DIST_ROOT"]) / version / filename
    path.parent.mkdir(parents=True, exist_ok=True)
    existing = Artifact.query.filter_by(relative_path=f"dist/{version}/{filename}").first()
    if existing:
        if existing.sha256 == digest and existing.size == size:
            return jsonify({"artifact": artifact_json(existing), "idempotent": True})
        raise ApiError("artifact_exists", "An artifact with a different hash already exists.", 409)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as tmp:
        tmp.write(data); temp_name = tmp.name
    Path(temp_name).replace(path)
    artifact = Artifact(version_id=v.id, platform=request.headers.get("X-Axolotl-Platform", ""), architecture=request.headers.get("X-Axolotl-Architecture", ""), filename=filename, relative_path=f"dist/{version}/{filename}", size=size, sha256=digest, signature=request.headers.get("X-Axolotl-Signature"), content_type=request.headers.get("Content-Type") or mimetypes.guess_type(filename)[0] or "application/octet-stream")
    db.session.add(artifact); db.session.commit()
    return jsonify({"artifact": artifact_json(artifact), "idempotent": False}), 201


def artifact_json(a):
    return {"version": a.version_ref.version, "platform": a.platform, "architecture": a.architecture, "filename": a.filename, "size": a.size, "sha256": a.sha256, "signature": a.signature, "url": f"{current_app.config['PUBLIC_BASE_URL']}/{a.relative_path}"}


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
    try:
        version = payload.get("version", ""); tag = payload.get("tag", ""); channel = payload.get("channel", "")
        parsed = parse_version(version)
        if tag not in (version, f"v{version}") or channel not in ("release", "beta"):
            raise ApiError("invalid_webhook", "Version, tag, and channel are inconsistent.")
        if channel == "release" and parsed.prerelease:
            raise ApiError("prerelease_release", "Release channel cannot publish prerelease versions.")
        if channel == "beta" and not parsed.prerelease:
            raise ApiError("stable_beta", "Beta channel requires a prerelease version.")
        v = Version.query.filter_by(version=version).first() or Version(version=version, channel=channel)
        v.channel, v.notes, v.release_tag, v.release_id = channel, payload.get("notes", ""), tag, str(payload.get("release_id", ""))
        v.force_update = bool(payload.get("force_update", False))
        v.published_at = datetime.fromisoformat(payload.get("published_at", utcnow().isoformat()).replace("Z", "+00:00"))
        artifacts = payload.get("artifacts", [])
        seen_platforms = set()
        required = {a.get("platform") for a in artifacts}
        for a_data in artifacts:
            if a_data.get("platform") not in PLATFORMS or a_data.get("platform") in seen_platforms:
                raise ApiError("invalid_artifact_platform", "Artifact platform is invalid or duplicated.")
            seen_platforms.add(a_data.get("platform"))
            if not a_data.get("filename"):
                raise ApiError("invalid_filename", "Artifact filename is required.")
            validate_filename(a_data["filename"])
            a = Artifact.query.filter_by(relative_path=f"dist/{version}/{a_data.get('filename')}").first()
            path = Path(current_app.config["DIST_ROOT"]) / version / (a_data.get("filename") or "")
            actual_hash = hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else ""
            actual_size = path.stat().st_size if path.is_file() else -1
            if (not a or a.version_id != v.id or a.relative_path != f"dist/{version}/{a.filename}" or actual_hash != a.sha256 or actual_size != a.size or a.sha256 != a_data.get("sha256") or a.size != a_data.get("size") or not a_data.get("signature")):
                raise ApiError("artifact_validation_failed", "Webhook artifact validation failed.")
            a.platform, a.architecture, a.signature = a_data.get("platform", a.platform), a_data.get("architecture", a.architecture), a_data.get("signature")
        if not artifacts or not required:
            raise ApiError("missing_artifacts", "At least one complete artifact is required.")
        v.status = "published"; db.session.add(v)
        manifest = {
            "version": v.version,
            "notes": v.notes or "",
            "pub_date": v.published_at.isoformat().replace("+00:00", "Z"),
            "published_at": v.published_at.isoformat().replace("+00:00", "Z"),
            "force_update": bool(v.force_update),
            "platforms": {a.platform: {"signature": a.signature, "url": f"{current_app.config['PUBLIC_BASE_URL']}/{a.relative_path}"} for a in v.artifacts if a.signature},
        }
        manifest_path = Path(current_app.config["DIST_ROOT"]) / version / "manifest.json"
        with tempfile.NamedTemporaryFile(dir=manifest_path.parent, mode="w", encoding="utf-8", delete=False) as tmp:
            json.dump(manifest, tmp, indent=2)
            temp_manifest = tmp.name
        Path(temp_manifest).replace(manifest_path)
        pointer = db.session.get(ChannelPointer, channel) or ChannelPointer(channel=channel)
        pointer.current_version = version; db.session.add(pointer)
        event.status, event.processed_at = "processed", utcnow()
        db.session.commit()
        return jsonify({"status": "published", "version": version})
    except ApiError as exc:
        db.session.rollback(); event = db.session.get(WebhookEvent, event_id) or event; event.status, event.error_message = "failed", exc.message; db.session.add(event); db.session.commit(); raise


def _admin_change(version, restore=False):
    require_admin()
    v = Version.query.filter_by(version=version).first()
    if not v: raise ApiError("version_not_found", "Version does not exist.", 404)
    body = request.get_json(silent=True) or {}; operator = body.get("operator", "admin"); reason = body.get("reason", "")
    if restore:
        for a in v.artifacts:
            path = Path(current_app.config["DIST_ROOT"]) / version / a.filename
            if not path.exists() or path.stat().st_size != a.size or hashlib.sha256(path.read_bytes()).hexdigest() != a.sha256 or not a.signature:
                raise ApiError("artifact_validation_failed", "Cannot restore with incomplete artifacts.")
        v.status, v.revoked_at, v.revoke_reason = "published", None, None
        action = "restore"
    else:
        v.status, v.revoked_at, v.revoke_reason = "revoked", utcnow(), reason
        action = "revoke"
    pointer = db.session.get(ChannelPointer, v.channel)
    if restore or (pointer and pointer.current_version == version):
        all_versions = Version.query.filter(Version.status == "published").all()
        if v.channel == "release":
            candidates = [x for x in all_versions if x.channel == "release" and not SemVersion.parse(x.version).prerelease]
        else:
            candidates = [x for x in all_versions if (x.channel == "release" and not SemVersion.parse(x.version).prerelease) or (x.channel == "beta" and SemVersion.parse(x.version).prerelease)]
        pointer = pointer or ChannelPointer(channel=v.channel)
        pointer.current_version = max(candidates, key=lambda x: SemVersion.parse(x.version)).version if candidates else None
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
