import hashlib
from pathlib import Path

import pytest

from app.extensions import db
from app.models import Artifact, AuditLog, ChannelPointer, UsageEvent, Version, WebhookEvent, utcnow
from app.services import delete_version as service
from app.services.version_lock import version_lock
from tests.test_api import configure_downloads, payload_for, signed_post

AUTH = {"Authorization": "Bearer admin"}
URL = "/api/admin/versions/1.9.4"


def add_version(app, version, channel="release", status="published"):
    record = Version(version=version, channel=channel, status=status, published_at=utcnow(),
                     release_tag=f"v{version}", release_id="123")
    db.session.add(record)
    db.session.flush()
    directory = Path(app.config["DIST_ROOT"], version)
    directory.mkdir()
    (directory / "update.zip").write_bytes(b"package")
    (directory / "manifest.json").write_text("{}", encoding="utf-8")
    db.session.add(Artifact(
        version_id=record.id, filename="update.zip", platform="linux-x86_64", kind="updater",
        relative_path=f"dist/{version}/update.zip", size=7,
        sha256=hashlib.sha256(b"package").hexdigest(), signature="sig", content_type="application/zip",
    ))
    return record


@pytest.fixture()
def versions(app):
    with app.app_context():
        add_version(app, "1.9.3")
        add_version(app, "1.9.4")
        db.session.add_all([
            ChannelPointer(channel="release", current_version="1.9.4"),
            ChannelPointer(channel="beta", current_version="1.9.4"),
            WebhookEvent(event_id="custom-target", version="1.9.4", payload_hash="a" * 64, status="processed"),
            WebhookEvent(event_id="github-v1.9.4-123", payload_hash="b" * 64, status="processed"),
            WebhookEvent(event_id="other-event", version="1.9.3", payload_hash="c" * 64, status="processed"),
            WebhookEvent(event_id="unknown-legacy", payload_hash="d" * 64, status="processed"),
            UsageEvent(path="/dist/1.9.4/update.zip", version="1.9.4", event_type="download"),
            AuditLog(operator="admin", action="old_action", version="1.9.4"),
        ])
        db.session.commit()
    staging = Path(app.config["DIST_ROOT"], ".staging", "custom-target")
    staging.mkdir(parents=True)
    (staging / "download.tmp").write_bytes(b"unfinished")
    return Path(app.config["DIST_ROOT"])


def test_delete_removes_files_metadata_events_and_recomputes_both_pointers(client, app, versions):
    response = client.delete(URL, headers=AUTH, json={"operator": "release-admin", "reason": "Republish"})
    assert response.status_code == 200
    assert response.json == {
        "status": "deleted", "version": "1.9.4", "idempotent": False,
        "deleted_artifacts": 1, "deleted_webhook_events": 2,
        "channel_pointers": {"release": "1.9.3", "beta": "1.9.3"}, "files_cleanup_pending": False,
    }
    assert not (versions / "1.9.4").exists()
    assert not (versions / ".staging/custom-target").exists()
    assert (versions / "1.9.3/update.zip").read_bytes() == b"package"
    with app.app_context():
        assert Version.query.filter_by(version="1.9.4").first() is None
        assert Artifact.query.count() == 1
        assert {event.event_id for event in WebhookEvent.query.all()} == {"other-event", "unknown-legacy"}
        assert UsageEvent.query.filter_by(version="1.9.4", event_type="download").count() == 1
        log = AuditLog.query.filter_by(action="delete_version").one()
        assert (log.version, log.operator, log.reason) == ("1.9.4", "release-admin", "Republish")
        assert log.request_id and log.ip_address
        assert AuditLog.query.filter_by(action="old_action").count() == 1
    assert client.get("/api/versions/1.9.4").status_code == 404
    assert client.get("/api/downloads/1.9.4").status_code == 404
    assert client.get("/dist/1.9.4/update.zip").status_code == 404
    assert client.get("/latest?platform=linux-x86_64").json["version"] == "1.9.3"


def test_deleted_version_can_be_republished_with_same_event_id(client, app):
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"sig", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    payload = payload_for(bodies)
    assert signed_post(client, payload).status_code == 200
    assert client.delete(URL, headers=AUTH).status_code == 200
    # A changed payload can reuse the original event_id after explicit deletion.
    bodies["update.tar.gz"] = b"new package"
    configure_downloads(app, bodies)
    response = signed_post(client, payload_for(bodies))
    assert response.status_code == 200 and response.json["status"] == "published"
    assert Path(app.config["DIST_ROOT"], "1.9.4/update.tar.gz").read_bytes() == b"new package"
    with app.app_context():
        assert WebhookEvent.query.one().version == "1.9.4"


def test_delete_requires_auth_and_valid_metadata(client, versions):
    assert client.delete(URL).status_code == 401
    assert client.delete(URL, headers={"Authorization": "Bearer bad"}).status_code == 401
    assert client.delete(URL, headers=AUTH, json={"operator": 1}).status_code == 400
    assert client.delete(URL, headers=AUTH, json={"reason": False}).status_code == 400
    assert client.delete(URL, headers=AUTH, data="invalid", content_type="application/json").status_code == 400
    assert client.delete(URL.replace("1.9.4", "invalid"), headers=AUTH).status_code == 400
    assert client.delete(URL.replace("1.9.4", "9.9.9"), headers=AUTH).status_code == 404
    assert (versions / "1.9.4/update.zip").exists()


@pytest.mark.parametrize("status", ["draft", "uploading", "ready", "failed", "revoked"])
def test_delete_any_inactive_state_and_pruned_files(client, app, versions, status):
    with app.app_context():
        Version.query.filter_by(version="1.9.4").one().status = status
        db.session.commit()
    service.shutil.rmtree(versions / "1.9.4")
    assert client.delete(URL, headers=AUTH).status_code == 200


def test_delete_last_beta_keeps_release_pointer(client, app):
    with app.app_context():
        add_version(app, "1.9.4")
        add_version(app, "1.9.5-beta.1", channel="beta")
        db.session.commit()
    response = client.delete("/api/admin/versions/1.9.5-beta.1", headers=AUTH)
    assert response.json["channel_pointers"] == {"release": "1.9.4", "beta": "1.9.4"}
    response = client.delete(URL, headers=AUTH)
    assert response.json["channel_pointers"] == {"release": None, "beta": None}


@pytest.mark.parametrize("failure", ["rename", "pointer", "commit"])
def test_delete_rolls_back_files_and_database(client, app, versions, monkeypatch, failure):
    def fail(*args, **kwargs):
        raise OSError("private server path")

    if failure == "rename":
        monkeypatch.setattr(Path, "rename", fail)
    elif failure == "pointer":
        monkeypatch.setattr("app.routes.api.pointer_candidate", fail)
    else:
        original_commit = db.session.commit
        failures = []

        def fail_once():
            if not failures:
                failures.append(True)
                fail()
            return original_commit()

        monkeypatch.setattr(db.session, "commit", fail_once)
    response = client.delete(URL, headers=AUTH)
    assert response.status_code == 500
    assert "private server path" not in response.get_data(as_text=True)
    assert (versions / "1.9.4/update.zip").read_bytes() == b"package"
    assert (versions / ".staging/custom-target/download.tmp").exists()
    with app.app_context():
        assert Version.query.filter_by(version="1.9.4").one()
        assert Artifact.query.count() == 2
        assert WebhookEvent.query.count() == 4
        assert db.session.get(ChannelPointer, "release").current_version == "1.9.4"
        assert AuditLog.query.filter_by(action="delete_version").count() == 0


def test_cleanup_failure_is_visible_and_retryable(client, app, versions, monkeypatch):
    remove = service.shutil.rmtree

    def fail(*args, **kwargs):
        raise OSError("fixture cleanup failure")

    monkeypatch.setattr(service.shutil, "rmtree", fail)
    response = client.delete(URL, headers=AUTH)
    assert response.status_code == 200 and response.json["files_cleanup_pending"] is True
    assert not (versions / "1.9.4").exists()
    assert signed_post(client, payload_for()).json["error"]["code"] == "version_cleanup_pending"
    monkeypatch.setattr(service.shutil, "rmtree", remove)
    response = client.delete(URL, headers=AUTH)
    assert response.status_code == 200 and response.json["files_cleanup_pending"] is False
    assert response.json["idempotent"] is True


def test_busy_version_cannot_be_deleted_or_republished(client, app, versions):
    with app.app_context(), version_lock("1.9.4"):
        assert client.delete(URL, headers=AUTH).json["error"]["code"] == "version_busy"
        assert signed_post(client, payload_for()).json["error"]["code"] == "version_busy"


def test_delete_rejects_escaping_storage_symlink(client, app, versions, tmp_path):
    service.shutil.rmtree(versions / "1.9.4")
    outside = tmp_path / "outside"
    outside.mkdir()
    protected = outside / "keep.txt"
    protected.write_text("keep", encoding="utf-8")
    try:
        (versions / "1.9.4").symlink_to(outside, target_is_directory=True)
    except OSError:
        pytest.skip("Creating symlinks is unavailable")
    response = client.delete(URL, headers=AUTH)
    assert response.status_code == 409
    assert protected.read_text() == "keep"
    with app.app_context():
        assert Version.query.filter_by(version="1.9.4").first()
