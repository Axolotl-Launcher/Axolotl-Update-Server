import hashlib
import json
from pathlib import Path

import pytest

from app.extensions import db
from app.models import Artifact, AuditLog, ChannelPointer, Version, utcnow
from app.services import force_update as service

URL = "/api/admin/versions/1.9.4/force-update"
AUTH = {"Authorization": "Bearer admin"}


@pytest.fixture()
def published(app):
    directory = Path(app.config["DIST_ROOT"], "1.9.4")
    directory.mkdir()
    (directory / "update.zip").write_bytes(b"package")
    manifest = {
        "version": "1.9.4", "force_update": False,
        "published_at": "2026-08-29T15:19:00Z", "notes": "Keep notes",
        "platforms": {"linux-x86_64": {"signature": "sig", "url": "https://update.axlmc.org/dist/1.9.4/update.zip"}},
    }
    (directory / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    with app.app_context():
        item = Version(version="1.9.4", channel="release", status="published", published_at=utcnow())
        db.session.add(item)
        db.session.flush()
        db.session.add(Artifact(
            version_id=item.id, platform="linux-x86_64", filename="update.zip",
            relative_path="dist/1.9.4/update.zip", size=7,
            sha256=hashlib.sha256(b"package").hexdigest(), signature="sig",
            content_type="application/zip", kind="updater",
        ))
        db.session.add(ChannelPointer(channel="release", current_version="1.9.4"))
        db.session.commit()
    return directory / "manifest.json"


def test_toggle_force_update_updates_responses_manifest_etag_and_audit(client, app, published):
    before = client.get("/latest?platform=linux-x86_64")
    original_manifest = json.loads(published.read_text())
    for enabled in (True, False):
        response = client.patch(URL, headers=AUTH, json={"force_update": enabled, "operator": "release-admin", "reason": "Urgent fix"})
        assert response.status_code == 200
        assert response.json == {"version": "1.9.4", "status": "published", "force_update": enabled, "idempotent": False}
        latest = client.get("/latest?platform=linux-x86_64", headers={"If-None-Match": '"' + before.headers["ETag"] + '"'})
        assert latest.status_code == 200
        assert latest.json["force_update"] is enabled
        assert latest.headers["ETag"] != before.headers["ETag"]
        assert latest.json["published_at"] == before.json["published_at"]
        assert client.get("/api/versions/1.9.4").json["force_update"] is enabled
        assert client.get("/api/downloads/1.9.4").json["force_update"] is enabled
        assert json.loads(published.read_text()) == {**original_manifest, "force_update": enabled}
        before = latest
    repeat = client.patch(URL, headers=AUTH, json={"force_update": False})
    assert repeat.json["idempotent"] is True
    with app.app_context():
        logs = AuditLog.query.order_by(AuditLog.id).all()
        assert [log.action for log in logs] == ["force_update_enabled", "force_update_disabled"]
        assert all(log.operator == "release-admin" and log.reason == "Urgent fix" and log.request_id for log in logs)
        assert db.session.get(ChannelPointer, "release").current_version == "1.9.4"


@pytest.mark.parametrize("body", [None, [], {}, {"force_update": "true"}, {"force_update": 1}, {"force_update": None}])
def test_force_update_requires_boolean(client, published, body):
    assert client.patch(URL, headers=AUTH, json=body).status_code == 400


def test_force_update_authentication_and_missing_version(client):
    assert client.patch(URL, json={"force_update": True}).status_code == 401
    assert client.patch(URL, headers={"Authorization": "Bearer wrong"}, json={"force_update": True}).status_code == 401
    assert client.patch(URL, headers=AUTH, json={"force_update": True}).status_code == 404
    assert client.patch(URL.replace("1.9.4", "bad"), headers=AUTH, json={"force_update": True}).status_code == 400


@pytest.mark.parametrize("state", ["draft", "uploading", "ready", "failed"])
def test_force_update_rejects_unpublished_version(client, app, published, state):
    with app.app_context():
        Version.query.first().status = state
        db.session.commit()
    assert client.patch(URL, headers=AUTH, json={"force_update": True}).status_code == 409


def test_force_update_keeps_revoked_status(client, app, published):
    with app.app_context():
        Version.query.first().status = "revoked"
        db.session.commit()
    response = client.patch(URL, headers=AUTH, json={"force_update": True})
    assert response.status_code == 200 and response.json["status"] == "revoked"
    assert client.get("/latest?platform=linux-x86_64").status_code == 204


def test_force_update_missing_manifest(client, app, published):
    published.unlink()
    response = client.patch(URL, headers=AUTH, json={"force_update": True})
    assert response.status_code == 409 and response.json["error"]["code"] == "manifest_unavailable"
    with app.app_context():
        assert Version.query.first().force_update is False
        assert AuditLog.query.count() == 0


@pytest.mark.parametrize("failure", ["stage", "replace", "commit", "invalid_manifest"])
def test_force_update_failure_rolls_back(client, app, published, monkeypatch, failure):
    if failure == "invalid_manifest":
        published.write_text('{"version":"wrong"}', encoding="utf-8")
    original = published.read_bytes()

    def fail(*args, **kwargs):
        raise OSError("sensitive server path")

    if failure == "stage":
        monkeypatch.setattr(service, "_stage_manifest", fail)
    elif failure == "replace":
        monkeypatch.setattr(service.os, "replace", fail)
    elif failure == "commit":
        commit = db.session.commit
        attempts = []

        def fail_once():
            if not attempts:
                attempts.append(True)
                fail()
            return commit()

        monkeypatch.setattr(db.session, "commit", fail_once)
    response = client.patch(URL, headers=AUTH, json={"force_update": True})
    assert response.status_code in (409, 500)
    assert "sensitive server path" not in response.get_data(as_text=True)
    assert published.read_bytes() == original
    assert {path.name for path in published.parent.iterdir()} == {"manifest.json", "update.zip"}
    with app.app_context():
        assert Version.query.first().force_update is False
        assert AuditLog.query.count() == 0
