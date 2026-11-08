import hashlib
import shutil
from pathlib import Path
from typing import Callable

from flask import current_app
from sqlalchemy import false, or_

from ..errors import ApiError
from ..extensions import db
from ..models import AuditLog, ChannelPointer, Version, WebhookEvent


def _safe_directory(path: Path, parent: Path) -> bool:
    if path.is_symlink() or path.resolve().parent != parent.resolve():
        raise ApiError("unsafe_storage_path", "Version storage path is unsafe.", 409)
    if path.exists() and not path.is_dir():
        raise ApiError("unsafe_storage_path", "Version storage path is not a directory.", 409)
    return path.is_dir()


def ensure_cleanup_complete(version: str) -> None:
    root = Path(current_app.config["DIST_ROOT"]).resolve()
    quarantine = root.parent / f".{root.name}-deletions" / hashlib.sha256(version.encode()).hexdigest()
    if quarantine.exists() or quarantine.is_symlink():
        raise ApiError("version_cleanup_pending", "Retry version deletion to finish file cleanup before republishing.", 409)


def delete_version(version: str, operator: str, reason: str,
                   request_id: str | None, ip_address: str | None,
                   pointer_candidate: Callable[[str], str | None]) -> dict:
    root = Path(current_app.config["DIST_ROOT"]).resolve()
    # Quarantine is outside the public /dist tree and on the same filesystem.
    quarantine_root = root.parent / f".{root.name}-deletions"
    _safe_directory(quarantine_root, root.parent)
    quarantine = quarantine_root / hashlib.sha256(version.encode()).hexdigest()
    _safe_directory(quarantine, quarantine_root)
    item = Version.query.filter_by(version=version).with_for_update().first()
    if not item:
        if quarantine.is_dir():
            try:
                shutil.rmtree(quarantine)
            except OSError as exc:
                raise ApiError("version_cleanup_failed", "Could not remove remaining version files; retry deletion.", 500) from exc
            return {"status": "deleted", "version": version, "idempotent": True, "files_cleanup_pending": False}
        raise ApiError("version_not_found", "Version does not exist.", 404)
    if quarantine.exists():
        raise ApiError("version_cleanup_pending", "An earlier deletion still has files pending cleanup.", 409)

    legacy_id = f"github-{item.release_tag}-{item.release_id}" if item.release_tag and item.release_id else None
    events = WebhookEvent.query.filter(or_(
        WebhookEvent.version == version,
        (WebhookEvent.version.is_(None)) & (WebhookEvent.event_id == legacy_id) if legacy_id else false(),
    )).all()
    moved: list[tuple[Path, Path]] = []
    artifact_count = len(item.artifacts)
    pointers: dict[str, str | None] = {}
    try:
        # Acquire the database write lock before moving public files.
        db.session.delete(item)
        db.session.flush()
        paths = [root / version]
        staging_root = root / ".staging"
        _safe_directory(staging_root, root)
        for event in events:
            # Older webhook IDs were used as directory names. Never traverse them.
            event_id = event.event_id
            if event_id and event_id not in (".", "..") and "/" not in event_id and "\\" not in event_id:
                paths.append(staging_root / event_id)
        for index, source in enumerate(paths):
            if _safe_directory(source, source.parent):
                quarantine.mkdir(parents=True, exist_ok=True)
                destination = quarantine / str(index)
                source.rename(destination)
                moved.append((source, destination))
        for event in events:
            db.session.delete(event)
        for channel in ("release", "beta"):
            pointer = db.session.get(ChannelPointer, channel) or ChannelPointer(channel=channel)
            pointer.current_version = pointer_candidate(channel)
            pointers[channel] = pointer.current_version
            db.session.add(pointer)
        db.session.add(AuditLog(
            operator=operator, action="delete_version", version=version,
            channel=item.channel, reason=reason, request_id=request_id, ip_address=ip_address,
        ))
        db.session.commit()
    except Exception as exc:
        db.session.rollback()
        try:
            for source, destination in reversed(moved):
                destination.rename(source)
            if quarantine.is_dir():
                quarantine.rmdir()
        except OSError as restore_error:
            current_app.logger.exception("Could not restore quarantined version files")
            raise ApiError("version_restore_failed", "Deletion failed and version files require recovery from quarantine.", 500) from restore_error
        if isinstance(exc, ApiError):
            raise
        raise ApiError("version_delete_failed", "Could not delete the version.", 500) from exc

    cleanup_pending = False
    if quarantine.is_dir():
        try:
            shutil.rmtree(quarantine)
        except OSError:
            current_app.logger.exception("Version deleted; quarantined file cleanup must be retried")
            cleanup_pending = True
    return {
        "status": "deleted", "version": version, "idempotent": False,
        "deleted_artifacts": artifact_count, "deleted_webhook_events": len(events),
        "channel_pointers": pointers, "files_cleanup_pending": cleanup_pending,
    }
