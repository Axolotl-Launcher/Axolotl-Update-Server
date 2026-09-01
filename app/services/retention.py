import shutil
from pathlib import Path

from flask import current_app
from semver import Version as SemVersion

from ..models import Version


def _retained_versions(channel: str, count: int) -> set[str]:
    versions = [
        version for version in Version.query.filter_by(channel=channel, status="published").all()
        if (channel == "release" and not SemVersion.parse(version.version).prerelease)
        or (channel == "beta" and SemVersion.parse(version.version).prerelease)
    ]
    versions.sort(key=lambda version: SemVersion.parse(version.version), reverse=True)
    return {version.version for version in versions[:count]}


def retained_versions() -> set[str]:
    return _retained_versions("release", current_app.config.get("RELEASE_RETENTION_COUNT", 3)) | _retained_versions(
        "beta", current_app.config.get("BETA_RETENTION_COUNT", 3)
    )


def prune_dist(retention_release: int = 3, retention_beta: int = 3) -> list[str]:
    """Remove old published version directories while retaining DB metadata.

    Release and beta retention sets are unioned so a release still used by beta
    clients is not removed prematurely.
    """
    retained = _retained_versions("release", retention_release) | _retained_versions("beta", retention_beta)
    root = Path(current_app.config["DIST_ROOT"]).resolve()
    removed: list[str] = []
    for version_dir in root.iterdir():
        if not version_dir.is_dir():
            continue
        try:
            version = SemVersion.parse(version_dir.name)
        except ValueError:
            continue
        record = Version.query.filter_by(version=str(version), status="published").first()
        if record and record.version not in retained:
            target = version_dir.resolve()
            if root not in target.parents:
                continue
            shutil.rmtree(target)
            removed.append(record.version)
    return removed
