import hashlib
import json
from pathlib import Path

import pytest

from app.extensions import db
from app.models import Artifact, AuditLog, ChannelPointer, Version, WebhookEvent, utcnow
from app.services import purge_dist
from app.services.version_lock import version_lock


def seed(app, version="1.9.4", channel="release", status="published", signature="sig"):
    root = Path(app.config["DIST_ROOT"], version)
    root.mkdir(parents=True)
    (root / "update.zip").write_bytes(b"package")
    (root / "update.zip.sig").write_bytes(b"sig\n")
    (root / "manifest.json").write_text('{"old":"manifest"}', encoding="utf-8")
    item = Version(version=version, channel=channel, status=status, notes="Keep notes",
                   published_at=utcnow(), force_update=True, release_tag=f"v{version}", release_id="123")
    item.artifacts.append(Artifact(filename="update.zip", relative_path=f"dist/{version}/update.zip",
                                   platform="linux-x86_64", kind="updater", size=7, signature=signature,
                                   sha256=hashlib.sha256(b"package").hexdigest(), content_type="application/zip"))
    db.session.add(item)
    return root


def test_cleanup_converts_old_release_and_beta_preserving_metadata(client, app):
    with app.app_context():
        directories = [seed(app), seed(app, "1.9.5-beta.1", "beta")]
        db.session.add(ChannelPointer(channel="release", current_version="1.9.4"))
        db.session.add(WebhookEvent(event_id="keep-event", version="1.9.4", payload_hash="a" * 64))
        db.session.commit()
    response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code == 0, response.output
    for directory in directories:
        assert {path.name for path in directory.iterdir()} == {"manifest.json"}
        manifest = json.loads((directory / "manifest.json").read_text())
        assert manifest["notes"] == "Keep notes" and manifest["force_update"] is True
        assert manifest["platforms"]["linux-x86_64"]["signature"] == "sig"
        missing = client.get(f"/dist/{directory.name}/update.zip")
        assert missing.status_code == 404 and "Location" not in missing.headers
    with app.app_context():
        assert all(item.artifact_storage == "metadata" for item in Version.query.all())
        assert Artifact.query.count() == 2
        assert WebhookEvent.query.count() == 1
        assert db.session.get(ChannelPointer, "release").current_version == "1.9.4"
        assert AuditLog.query.filter_by(action="purge_dist_assets").count() == 2
    assert client.get("/latest?platform=linux-x86_64").json["version"] == "1.9.4"
    assert client.get("/latest?channel=beta&platform=linux-x86_64").json["version"] == "1.9.5-beta.1"
    assert app.test_cli_runner().invoke(args=["purge-dist-assets"]).exit_code == 0
    with app.app_context():
        assert AuditLog.query.filter_by(action="purge_dist_assets").count() == 2


def test_dry_run_and_version_filter(client, app):
    with app.app_context():
        release = seed(app, signature=None)
        beta = seed(app, "1.9.5-beta.1", "beta")
        db.session.commit()
    response = app.test_cli_runner().invoke(args=["purge-dist-assets", "--dry-run"])
    assert response.exit_code == 0 and "Would remove" in response.output
    assert (release / "update.zip").exists() and (beta / "update.zip").exists()
    with app.app_context():
        assert Version.query.filter_by(version="1.9.4").one().artifact_storage == "local"
        assert Artifact.query.filter_by(relative_path="dist/1.9.4/update.zip").one().signature is None
    response = app.test_cli_runner().invoke(args=["purge-dist-assets", "--version", "1.9.4"])
    assert response.exit_code == 0, response.output
    assert not (release / "update.zip").exists() and (beta / "update.zip").exists()
    with app.app_context():
        assert Artifact.query.filter_by(relative_path="dist/1.9.4/update.zip").one().signature == "sig"


def test_cleanup_can_convert_already_pruned_and_revoked_versions(app):
    with app.app_context():
        directory = seed(app, status="revoked")
        db.session.commit()
    purge_dist.shutil.rmtree(directory)
    response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code == 0, response.output
    assert {path.name for path in directory.iterdir()} == {"manifest.json"}
    with app.app_context():
        item = Version.query.one()
        assert item.status == "revoked" and item.artifact_storage == "metadata"


def test_missing_signature_stops_before_deleting_files(app):
    with app.app_context():
        directory = seed(app, signature=None)
        db.session.commit()
    (directory / "update.zip.sig").unlink()
    response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code != 0 and "signature" in response.output
    assert (directory / "update.zip").read_bytes() == b"package"
    with app.app_context():
        assert Version.query.one().artifact_storage == "local"


def test_commit_failure_keeps_original_manifest_and_payloads(app, monkeypatch):
    with app.app_context():
        directory = seed(app)
        db.session.commit()
    original = (directory / "manifest.json").read_bytes()

    def fail():
        raise OSError("fixture database failure")

    monkeypatch.setattr(db.session, "commit", fail)
    response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code != 0
    assert (directory / "manifest.json").read_bytes() == original
    assert (directory / "update.zip").exists()
    with app.app_context():
        assert Version.query.one().artifact_storage == "local"


def test_file_cleanup_failure_is_retryable_after_metadata_commit(app, monkeypatch):
    with app.app_context():
        directory = seed(app)
        db.session.commit()
    original = Path.unlink

    def fail_payload(path, *args, **kwargs):
        if path == directory / "update.zip":
            raise OSError("fixture file cleanup failure")
        return original(path, *args, **kwargs)

    monkeypatch.setattr(Path, "unlink", fail_payload)
    response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code != 0
    with app.app_context():
        assert Version.query.one().artifact_storage == "metadata"
    monkeypatch.setattr(Path, "unlink", original)
    response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code == 0
    assert {path.name for path in directory.iterdir()} == {"manifest.json"}


def test_cleanup_rejects_external_version_link(app, tmp_path):
    with app.app_context():
        directory = seed(app)
        db.session.commit()
    purge_dist.shutil.rmtree(directory)
    outside = tmp_path / "external"
    outside.mkdir()
    (outside / "keep.zip").write_bytes(b"keep")
    try:
        directory.symlink_to(outside, target_is_directory=True)
    except OSError:
        pytest.skip("Symlinks unavailable")
    response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code != 0
    assert (outside / "keep.zip").read_bytes() == b"keep"


def test_cleanup_does_not_touch_unregistered_directories_and_does_not_follow_child_links(app, tmp_path):
    with app.app_context():
        directory = seed(app)
        db.session.commit()
    unknown = Path(app.config["DIST_ROOT"], "unregistered")
    unknown.mkdir()
    (unknown / "keep.zip").write_bytes(b"keep unknown")
    outside = tmp_path / "outside"
    outside.mkdir()
    (outside / "keep.zip").write_bytes(b"keep outside")
    try:
        (directory / "linked-payload").symlink_to(outside, target_is_directory=True)
    except OSError:
        pytest.skip("Symlinks unavailable")
    result = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert result.exit_code == 0, result.output
    assert {path.name for path in directory.iterdir()} == {"manifest.json"}
    assert (outside / "keep.zip").read_bytes() == b"keep outside"
    assert (unknown / "keep.zip").read_bytes() == b"keep unknown"


def test_cleanup_refuses_busy_version(app):
    with app.app_context():
        directory = seed(app)
        db.session.commit()
        with version_lock("1.9.4"):
            response = app.test_cli_runner().invoke(args=["purge-dist-assets"])
    assert response.exit_code != 0
    assert (directory / "update.zip").exists()
