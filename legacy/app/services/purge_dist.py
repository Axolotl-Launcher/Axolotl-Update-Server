import json
import os
import shutil
from datetime import timezone
from pathlib import Path

from flask import current_app
from semver import Version as SemVersion

from ..errors import ApiError
from ..extensions import db
from ..models import AuditLog, Version
from .force_update import _stage_manifest
from .updater_manifest import updater_manifest_platforms
from .version_lock import version_lock


def _directory(root: Path, version: str) -> Path:
    try:
        parsed = SemVersion.parse(version)
    except (ValueError, TypeError) as exc:
        raise ApiError("unsafe_storage_path", "Version must be normalized SemVer.", 409) from exc
    path = root / version
    if str(parsed) != version or path.is_symlink() or path.resolve().parent != root:
        raise ApiError("unsafe_storage_path", "Version storage path is unsafe.", 409)
    if path.exists() and not path.is_dir():
        raise ApiError("unsafe_storage_path", "Version storage path is not a directory.", 409)
    return path


def _payload_entries(directory: Path):
    return [path for path in directory.iterdir() if path.name != "manifest.json"] if directory.exists() else []


def convert_and_purge(version: str, dry_run: bool = False) -> dict:
    """Persist metadata/signatures before removing package files. Retry after interruption."""
    with version_lock(version):
        item = Version.query.filter_by(version=version).with_for_update().one()
        parsed = SemVersion.parse(version)
        if item.channel not in ("release", "beta") or bool(parsed.prerelease) != (item.channel == "beta"):
            raise ApiError("invalid_channel", "Version and channel do not match.", 409)
        root = Path(current_app.config["DIST_ROOT"]).resolve()
        directory = _directory(root, version)
        for artifact in item.artifacts:
            if (
                artifact.relative_path != f"dist/{version}/{artifact.filename}"
                or not artifact.filename or "/" in artifact.filename or "\\" in artifact.filename
                or ".." in artifact.filename or artifact.filename == "manifest.json"
                or artifact.size < 0 or len(artifact.sha256) != 64
                or any(character not in "0123456789abcdefABCDEF" for character in artifact.sha256)
            ):
                raise ApiError("invalid_artifact_metadata", "Artifact metadata is incomplete; no files were removed.", 409)
            if artifact.kind == "updater" and not (artifact.signature or "").strip():
                filename = artifact.signature_filename or f"{artifact.filename}.sig"
                signature_path = directory / filename
                if (
                    filename != f"{artifact.filename}.sig" or signature_path.is_symlink()
                    or not signature_path.is_file() or signature_path.stat().st_size > 65536
                ):
                    raise ApiError("missing_signature", "Updater signature is unavailable; no files were removed.", 409)
                artifact.signature = signature_path.read_text(encoding="utf-8").strip()
                if not artifact.signature:
                    raise ApiError("missing_signature", "Updater signature is empty; no files were removed.", 409)
        platforms = updater_manifest_platforms(item.artifacts, current_app.config["PUBLIC_BASE_URL"])
        manifest_path = directory / "manifest.json"
        if manifest_path.is_symlink():
            raise ApiError("unsafe_storage_path", "Manifest storage path is unsafe.", 409)
        entries = _payload_entries(directory)
        result = {"version": version, "channel": item.channel, "entries": len(entries), "dry_run": dry_run}
        if dry_run:
            db.session.rollback()
            return result

        published = item.published_at or item.created_at
        if published.tzinfo is None:
            published = published.replace(tzinfo=timezone.utc)
        date = published.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")
        manifest = {"version": version, "notes": item.notes or "", "pub_date": date,
                    "published_at": date, "force_update": bool(item.force_update), "platforms": platforms}
        original = manifest_path.read_bytes() if manifest_path.is_file() else None
        staged = None
        written = False
        try:
            # Mark metadata mode durably before deleting anything. If cleanup
            # is interrupted the same command safely completes the remainder.
            was_local = item.artifact_storage != "metadata"
            item.artifact_storage = "metadata"
            db.session.flush()
            directory.mkdir(parents=True, exist_ok=True)
            staged = _stage_manifest(directory, json.dumps(manifest, indent=2).encode("utf-8"))
            os.replace(staged, manifest_path)
            written = True
            if was_local or entries:
                db.session.add(AuditLog(operator="maintenance", action="purge_dist_assets", version=version,
                                        channel=item.channel, reason="Convert existing version to metadata-only storage"))
            db.session.commit()
        except Exception:
            db.session.rollback()
            if written:
                if original is not None:
                    restored = _stage_manifest(directory, original)
                    os.replace(restored, manifest_path)
                else:
                    manifest_path.unlink(missing_ok=True)
            raise
        finally:
            if staged:
                staged.unlink(missing_ok=True)

        for path in entries:
            # Paths are immediate children of the verified version directory.
            # Unlink symlinks; never follow them into external directories.
            if path.is_symlink():
                path.unlink()
            elif path.is_dir():
                if path.resolve().parent != directory.resolve():
                    raise ApiError("unsafe_storage_path", "Payload directory is unsafe.", 409)
                shutil.rmtree(path)
            else:
                path.unlink(missing_ok=True)
        return result
