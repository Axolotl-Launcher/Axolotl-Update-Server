from ..errors import ApiError
from ..models import artifact_platforms


def updater_manifest_platforms(artifacts, public_base_url: str, platform: str | None = None):
    platforms: dict[str, dict[str, str]] = {}
    for artifact in artifacts:
        if artifact.kind != "updater" or not artifact.signature:
            continue
        for target in artifact_platforms(artifact):
            if platform and target != platform:
                continue
            entry = {
                "signature": artifact.signature,
                "url": f"{public_base_url}/{artifact.relative_path}",
            }
            existing = platforms.get(target)
            if existing is not None and existing != entry:
                raise ApiError(
                    "ambiguous_updater_platform",
                    f"Multiple updater artifacts target {target}.",
                    500,
                )
            platforms[target] = entry
    return platforms
