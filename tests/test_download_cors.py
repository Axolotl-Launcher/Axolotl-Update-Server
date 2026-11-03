from pathlib import Path

import pytest

from app.extensions import db
from app.models import Artifact, Version, utcnow


@pytest.mark.parametrize("origin", ["http://localhost:3000", "https://axlmc.org"])
@pytest.mark.parametrize("channel,version", [("release", "1.10.0"), ("beta", "1.11.0-beta.1")])
def test_download_catalog_cors_with_artifacts(client, app, origin, channel, version):
    filename = "Axolotl.Launcher_x64-setup.exe"
    directory = Path(app.config["DIST_ROOT"]) / version
    directory.mkdir()
    (directory / filename).write_bytes(b"installer")
    with app.app_context():
        release = Version(version=version, channel=channel, status="published", published_at=utcnow())
        release.artifacts.append(Artifact(
            filename=filename, relative_path=f"dist/{version}/{filename}",
            platform="windows-x86_64", kind="installer", size=9,
            sha256="a" * 64, content_type="application/octet-stream",
        ))
        db.session.add(release)
        db.session.commit()

    for path in (f"/api/downloads/latest?channel={channel}", f"/api/downloads/{version}"):
        response = client.get(path, headers={"Origin": origin})
        assert response.status_code == 200
        assert response.json["version"] == version
        assert response.json["downloads"][0]["filename"] == filename
        assert response.headers["Access-Control-Allow-Origin"] == "*"
        assert "Access-Control-Allow-Credentials" not in response.headers
        assert "ETag" in response.headers["Access-Control-Expose-Headers"]


@pytest.mark.parametrize("method", ["GET", "HEAD", "OPTIONS"])
def test_empty_catalog_and_preflight_allow_public_reads(client, method):
    response = client.open("/api/downloads/latest?channel=beta", method=method, headers={
        "Origin": "http://localhost:3000",
        "Access-Control-Request-Method": "GET",
        "Access-Control-Request-Headers": "content-type",
    })
    assert response.status_code == (200 if method == "OPTIONS" else 204)
    assert response.headers["Access-Control-Allow-Origin"] == "*"
    assert response.headers["Access-Control-Allow-Methods"] == "GET, HEAD, OPTIONS"
    assert response.headers["Access-Control-Allow-Headers"] == "Content-Type"


@pytest.mark.parametrize("path,status", [
    ("/api/downloads/latest?channel=invalid", 400),
    ("/api/downloads/1.10.0", 404),
    ("/api/downloads/latest?include_revoked=true", 401),
])
def test_catalog_errors_remain_readable_cross_origin(client, path, status):
    response = client.get(path, headers={"Origin": "http://localhost:3000"})
    assert response.status_code == status
    assert response.headers["Access-Control-Allow-Origin"] == "*"


def test_catalog_rate_limit_remains_readable_cross_origin(client, app):
    app.config["RATE_LIMIT_PER_MINUTE"] = 0
    response = client.get("/api/downloads/latest", headers={"Origin": "http://localhost:3000"})
    assert response.status_code == 429
    assert response.headers["Access-Control-Allow-Origin"] == "*"


@pytest.mark.parametrize("method,path", [
    ("GET", "/api/admin/stats"),
    ("OPTIONS", "/api/webhook/release"),
    ("POST", "/api/downloads/latest"),
])
def test_cors_is_not_enabled_for_admin_webhooks_or_writes(client, method, path):
    response = client.open(path, method=method, headers={"Origin": "http://localhost:3000"})
    assert "Access-Control-Allow-Origin" not in response.headers
