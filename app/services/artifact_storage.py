import hashlib
from pathlib import Path

from flask import current_app


def metadata_only(version):
    return version.channel == "release" and version.artifact_storage == "metadata"


def artifact_available(version, artifact, verify_hash=False):
    if artifact.relative_path != f"dist/{version.version}/{artifact.filename}":
        return False
    if metadata_only(version):
        return artifact.size >= 0 and len(artifact.sha256) == 64
    path = Path(current_app.config["DIST_ROOT"]) / version.version / artifact.filename
    if not path.is_file() or path.stat().st_size != artifact.size:
        return False
    if verify_hash:
        hasher = hashlib.sha256()
        with path.open("rb") as source:
            while chunk := source.read(1024 * 1024):
                hasher.update(chunk)
        return hasher.hexdigest() == artifact.sha256
    return True
