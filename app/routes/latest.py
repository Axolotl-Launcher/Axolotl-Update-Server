import hashlib
import json
from datetime import timezone
from pathlib import Path

from flask import Blueprint, Response, current_app, jsonify, request
from semver import Version as SemVersion

from ..errors import ApiError
from ..models import Version

latest_bp = Blueprint("latest", __name__)


def iso_utc(value):
    if value.tzinfo is None:
        value = value.replace(tzinfo=timezone.utc)
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


@latest_bp.get("/latest")
def latest():
    channel = request.headers.get("X-Axolotl-Channel") or request.args.get("channel", "release")
    platform = request.headers.get("X-Axolotl-Platform") or request.args.get("platform", "")
    current = request.headers.get("X-Axolotl-Version") or request.args.get("current_version", "")
    if channel not in ("release", "beta"):
        raise ApiError("invalid_channel", "Only release and beta channels are supported.")
    candidates = Version.query.filter_by(channel=channel, status="published").all()
    if channel == "release": candidates = [v for v in candidates if not SemVersion.parse(v.version).prerelease]
    if current:
        try: current_sv = SemVersion.parse(current); candidates = [v for v in candidates if SemVersion.parse(v.version) > current_sv]
        except ValueError as exc:
            raise ApiError("invalid_version", "Current version must be valid SemVer.") from exc
    if not candidates: return Response(status=204)
    chosen = max(candidates, key=lambda v: SemVersion.parse(v.version))
    artifacts = [a for a in chosen.artifacts if a.platform == platform] if platform else chosen.artifacts
    if platform and not artifacts: return Response(status=204)
    platforms = {a.platform: {"signature": a.signature, "url": f"{current_app.config['PUBLIC_BASE_URL']}/{a.relative_path}"} for a in artifacts if a.signature}
    if platform and platform not in platforms: return Response(status=204)
    payload = {"version": chosen.version, "notes": chosen.notes or "", "pub_date": iso_utc(chosen.published_at), "published_at": iso_utc(chosen.published_at), "platforms": platforms}
    body = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    etag = hashlib.sha256(body.encode()).hexdigest()
    if request.if_none_match and request.if_none_match.contains(etag): return Response(status=304, headers={"ETag": etag})
    response = jsonify(payload); response.headers["ETag"] = etag; response.headers["Cache-Control"] = "no-cache, max-age=0, must-revalidate"; return response


@latest_bp.get("/dist/<version>/<path:filename>")
def dist_file(version, filename):
    # Development fallback; production Caddy should serve /dist directly.
    root = Path(current_app.config["DIST_ROOT"]).resolve(); target = (root / version / filename).resolve()
    if root not in target.parents or not target.is_file(): raise ApiError("artifact_not_found", "The requested artifact does not exist.", 404)
    from flask import send_from_directory
    response = send_from_directory(root / version, filename, conditional=True)
    response.headers["Cache-Control"] = "public, max-age=31536000, immutable"; return response
