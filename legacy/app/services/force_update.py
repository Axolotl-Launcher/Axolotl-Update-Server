import json
import os
import tempfile
from pathlib import Path

from flask import current_app

from ..errors import ApiError
from ..extensions import db
from ..models import AuditLog, Version


def _stage_manifest(directory: Path, content: bytes) -> Path:
    with tempfile.NamedTemporaryFile(dir=directory, delete=False) as output:
        path = Path(output.name)
        try:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        except Exception:
            output.close()
            path.unlink(missing_ok=True)
            raise
    return path


def set_force_update(version: str, enabled: bool, operator: str, reason: str,
                     request_id: str | None, ip_address: str | None) -> dict:
    item = Version.query.filter_by(version=version).with_for_update().first()
    if not item:
        raise ApiError("version_not_found", "Version does not exist.", 404)
    if item.status not in ("published", "revoked"):
        raise ApiError("invalid_version_state", "Only published or revoked versions can change force_update.", 409)
    manifest_path = Path(current_app.config["DIST_ROOT"]) / version / "manifest.json"
    if not manifest_path.is_file():
        raise ApiError("manifest_unavailable", "The local version manifest is unavailable.", 409)
    previous = bool(item.force_update)
    result = {"version": version, "status": item.status, "force_update": enabled, "idempotent": previous == enabled}
    if previous == enabled:
        return result

    staged = None
    backup = None
    replaced = False
    try:
        item.force_update = enabled
        # Flush takes the database write lock before reading/replacing the file.
        db.session.flush()
        original = manifest_path.read_bytes()
        manifest = json.loads(original)
        if not isinstance(manifest, dict) or manifest.get("version") != version:
            raise ApiError("invalid_manifest", "The local version manifest is invalid.", 409)
        manifest["force_update"] = enabled
        backup = _stage_manifest(manifest_path.parent, original)
        staged = _stage_manifest(manifest_path.parent, json.dumps(manifest, indent=2).encode("utf-8"))
        db.session.add(AuditLog(
            operator=operator, action="force_update_enabled" if enabled else "force_update_disabled",
            version=version, channel=item.channel, reason=reason,
            request_id=request_id, ip_address=ip_address,
        ))
        db.session.flush()
        os.replace(staged, manifest_path)
        replaced = True
        db.session.commit()
    except Exception as exc:
        db.session.rollback()
        if replaced and backup:
            os.replace(backup, manifest_path)
        if isinstance(exc, ApiError):
            raise
        raise ApiError("force_update_failed", "Could not update the version force_update setting.", 500) from exc
    finally:
        if staged:
            staged.unlink(missing_ok=True)
        if backup:
            backup.unlink(missing_ok=True)
    return result
