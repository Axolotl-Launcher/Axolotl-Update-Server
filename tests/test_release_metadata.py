import hashlib
import io
import json
from pathlib import Path

import pytest

from app.extensions import db
from app.models import Artifact, ChannelPointer, Version
from app.services.retention import prune_dist
from tests.test_api import configure_downloads, payload_for, signed_post

AUTH = {"Authorization": "Bearer admin"}


def test_release_fetches_only_verified_signature_and_keeps_dist_urls(client, app):
    bodies = {"update.tar.gz": b"not downloaded", "update.tar.gz.sig": b" tauri signature \n", "installer.exe": b"not downloaded"}
    requests = []

    def signature_only(url, timeout):
        requests.append(url)
        assert url.endswith("update.tar.gz.sig"), "Release binaries must never be requested"
        return io.BytesIO(bodies["update.tar.gz.sig"])

    app.config["GITHUB_DOWNLOAD_OPEN"] = signature_only
    response = signed_post(client, payload_for(bodies))
    assert response.status_code == 200
    assert len(requests) == 1
    root = Path(app.config["DIST_ROOT"])
    assert {path.relative_to(root).as_posix() for path in root.rglob("*") if path.is_file()} == {"1.9.4/manifest.json"}
    latest = client.get("/latest?platform=linux-x86_64").json
    assert latest["platforms"]["linux-x86_64"] == {
        "url": "https://update.axlmc.org/dist/1.9.4/update.tar.gz", "signature": "tauri signature",
    }
    assert client.get("/api/downloads/latest").json["downloads"][0]["url"] == "https://update.axlmc.org/dist/1.9.4/installer.exe"
    assert client.get("/latest?channel=beta&platform=linux-x86_64").json["version"] == "1.9.4"
    assert signed_post(client, payload_for(bodies)).json["idempotent"] is True
    assert len(requests) == 1
    for name in bodies:
        result = client.get(f"/dist/1.9.4/{name}")
        assert result.status_code == 404 and "Location" not in result.headers
    with app.app_context():
        assert Version.query.one().artifact_storage == "metadata"
        assert Artifact.query.count() == 3
        assert db.session.get(ChannelPointer, "release").current_version == "1.9.4"


def test_metadata_release_restore_force_update_retention_and_old_dist_do_not_redirect(client, app):
    for patch in range(4, 8):
        version = f"1.9.{patch}"
        bodies = {"update.tar.gz": b"package", "update.tar.gz.sig": b"sig", "installer.exe": b"installer"}
        configure_downloads(app, bodies, version)
        assert signed_post(client, payload_for(bodies, version=version)).status_code == 200
    with app.app_context():
        prune_dist()
    path = Path(app.config["DIST_ROOT"], "1.9.4/manifest.json")
    assert path.is_file()
    assert client.get("/dist/1.9.4/update.tar.gz").status_code == 404
    assert client.get("/api/downloads/1.9.4").json["downloads"]
    assert client.post("/api/admin/versions/1.9.7/revoke", headers=AUTH).status_code == 200
    assert client.post("/api/admin/versions/1.9.7/restore", headers=AUTH).status_code == 200
    response = client.patch("/api/admin/versions/1.9.4/force-update", headers=AUTH, json={"force_update": True})
    assert response.status_code == 200
    assert json.loads(path.read_text())["force_update"] is True


@pytest.mark.parametrize("body,expected", [(b"wrong", "github_asset_integrity_failed"), (b"\xff", "invalid_signature"), (b"  ", "invalid_signature")])
def test_invalid_release_signature_does_not_publish(client, app, body, expected):
    bodies = {"update.tar.gz": b"package", "update.tar.gz.sig": body, "installer.exe": b"installer"}
    payload = payload_for(bodies)
    if body == b"wrong":
        payload["release"]["assets"][1]["digest"] = "sha256:" + hashlib.sha256(b"other").hexdigest()
        payload["catalog"]["files"][1]["sha256"] = hashlib.sha256(b"other").hexdigest()
    configure_downloads(app, bodies)
    response = signed_post(client, payload)
    assert response.status_code == 400 and response.json["error"]["code"] == expected
    assert not list(Path(app.config["DIST_ROOT"]).rglob("*"))
    with app.app_context():
        assert Version.query.count() == Artifact.query.count() == ChannelPointer.query.count() == 0


def test_release_signature_size_is_bounded_and_main_metadata_is_still_validated(client, app):
    bodies = {"update.tar.gz": b"package", "update.tar.gz.sig": b"x" * 65537, "installer.exe": b"installer"}
    app.config["GITHUB_DOWNLOAD_OPEN"] = lambda *args: pytest.fail("Oversized signature must be rejected before downloading")
    response = signed_post(client, payload_for(bodies))
    assert response.json["error"]["code"] == "github_asset_too_large"
    payload = payload_for()
    payload["event_id"] = "bad-main"
    payload["catalog"]["files"][0]["size"] += 1
    assert signed_post(client, payload).json["error"]["code"] == "github_asset_metadata_mismatch"


def test_beta_also_stores_only_metadata_and_validates_signature(client, app):
    version = "1.9.5-beta.1"
    bodies = {"update.tar.gz": b"package", "update.tar.gz.sig": b"sig", "installer.exe": b"installer"}
    configure_downloads(app, bodies, version)
    assert signed_post(client, payload_for(bodies, version=version, channel="beta")).status_code == 200
    assert {path.name for path in Path(app.config["DIST_ROOT"], version).iterdir()} == {"manifest.json"}
    assert client.get(f"/dist/{version}/update.tar.gz").status_code == 404
    with app.app_context():
        assert Version.query.one().artifact_storage == "metadata"

    broken = payload_for(bodies, version="1.9.6-beta.1", channel="beta")
    app.config["GITHUB_DOWNLOAD_OPEN"] = lambda url, timeout: io.BytesIO(b"corrupt package")
    response = signed_post(client, broken)
    assert response.status_code == 400
    assert response.json["error"]["code"] == "github_asset_integrity_failed"


def test_metadata_release_requires_corresponding_signature_before_fetch(client, app):
    payload = payload_for()
    payload["catalog"]["artifacts"][0]["signatureFilename"] = "installer.exe"
    app.config["GITHUB_DOWNLOAD_OPEN"] = lambda *args: pytest.fail("Invalid signature association must be rejected")
    assert signed_post(client, payload).json["error"]["code"] == "missing_signature"


def test_signature_temporary_directory_is_removed_after_success(client, app, monkeypatch, tmp_path):
    import tempfile

    original = tempfile.TemporaryDirectory
    directories = []

    def isolated_signatures(*args, **kwargs):
        directory = original(*args, dir=tmp_path, **kwargs)
        directories.append(Path(directory.name))
        return directory

    monkeypatch.setattr("app.routes.api.tempfile.TemporaryDirectory", isolated_signatures)
    configure_downloads(app, {"update.tar.gz.sig": b"tauri-signature\n"})
    assert signed_post(client, payload_for()).status_code == 200
    assert directories and all(not directory.exists() for directory in directories)


@pytest.mark.parametrize("version", ["1.9.4", "1.9.5-beta.1"])
def test_manual_upload_is_rejected_before_storing_bytes(client, app, version):
    response = client.put(f"/api/artifacts/{version}/setup.exe", headers={"X-Upload-Token": "upload"}, data=b"package")
    assert response.status_code == 409 and response.json["error"]["code"] == "release_metadata_only"
    assert list(Path(app.config["DIST_ROOT"]).iterdir()) == []
