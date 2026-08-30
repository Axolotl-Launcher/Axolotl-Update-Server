import hashlib
import hmac
import json
import time
from pathlib import Path


def upload(client, body=b"payload", filename="app.tar.gz", platform="linux-x86_64", kind="updater", variant="", display_name=""):
    return client.put(
        f"/api/artifacts/1.0.0/{filename}",
        data=body,
        headers={"X-Upload-Token": "upload", "X-Axolotl-Platform": platform, "X-Axolotl-Signature": "sig", "X-Axolotl-Kind": kind, "X-Axolotl-Variant": variant, "X-Axolotl-Display-Name": display_name},
    )


def test_health_and_upload(client):
    assert client.get("/api/health").status_code == 200
    response = upload(client)
    assert response.status_code == 201
    assert response.json["artifact"]["sha256"] == hashlib.sha256(b"payload").hexdigest()
    assert client.put("/api/artifacts/1.0.0/../bad", data=b"x", headers={"X-Upload-Token": "upload"}).status_code in (404, 308)


def test_latest_and_idempotent_upload(client):
    upload(client)
    again = upload(client)
    assert again.status_code == 200 and again.json["idempotent"]
    assert client.get("/latest?platform=linux-x86_64").status_code == 204
    payload = {"event_id": "e1", "tag": "v1.0.0", "version": "1.0.0", "channel": "release", "notes": "n", "published_at": "2026-08-30T00:00:00Z", "artifacts": [{"platform": "linux-x86_64", "architecture": "x86_64", "filename": "app.tar.gz", "size": 7, "sha256": hashlib.sha256(b"payload").hexdigest(), "signature": "sig"}]}
    raw = json.dumps(payload).encode(); ts = str(int(time.time()))
    sig = hmac.new(b"webhook", (ts + ".").encode() + raw, hashlib.sha256).hexdigest()
    assert client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": sig}).status_code == 200
    latest = client.get("/latest?platform=linux-x86_64")
    assert latest.status_code == 200 and latest.json["version"] == "1.0.0"


def test_force_update_propagates_and_changes_etag(client, app):
    upload(client)
    payload = {"event_id": "force-1", "tag": "v1.0.0", "version": "1.0.0", "channel": "release", "force_update": True, "published_at": "2026-08-30T00:00:00Z", "artifacts": [{"platform": "linux-x86_64", "filename": "app.tar.gz", "size": 7, "sha256": hashlib.sha256(b"payload").hexdigest(), "signature": "sig"}]}
    raw = json.dumps(payload).encode(); ts = str(int(time.time())); sig = hmac.new(b"webhook", (ts + ".").encode() + raw, hashlib.sha256).hexdigest()
    assert client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": sig}).status_code == 200
    first = client.get("/latest?platform=linux-x86_64")
    assert first.json["force_update"] is True
    assert client.get("/api/versions/1.0.0").json["force_update"] is True
    with app.app_context():
        from app.extensions import db
        from app.models import Version
        Version.query.filter_by(version="1.0.0").update({"force_update": False}); db.session.commit()
    second = client.get("/latest?platform=linux-x86_64")
    assert first.headers["ETag"] != second.headers["ETag"]
    assert json.loads(Path(app.config["DIST_ROOT"], "1.0.0", "manifest.json").read_text())["force_update"] is True


def test_beta_rejects_stable_version(client):
    payload = {"event_id": "bad-beta", "tag": "v1.0.0", "version": "1.0.0", "channel": "beta", "artifacts": []}
    raw = json.dumps(payload).encode(); ts = str(int(time.time())); sig = hmac.new(b"webhook", (ts + ".").encode() + raw, hashlib.sha256).hexdigest()
    response = client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": sig})
    assert response.status_code == 400 and response.json["error"]["code"] == "stable_beta"


def test_webhook_failed_event_can_retry_but_payload_conflict_is_rejected(client):
    payload = {"event_id": "retry-1", "tag": "v1.0.0", "version": "1.0.0", "channel": "release", "artifacts": []}
    raw = json.dumps(payload).encode(); ts = str(int(time.time())); sig = hmac.new(b"webhook", (ts + ".").encode() + raw, hashlib.sha256).hexdigest()
    failed = client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": sig})
    assert failed.status_code == 400
    conflict = dict(payload, notes="changed")
    conflict_raw = json.dumps(conflict).encode(); conflict_sig = hmac.new(b"webhook", (ts + ".").encode() + conflict_raw, hashlib.sha256).hexdigest()
    response = client.post("/api/webhook/release", data=conflict_raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": conflict_sig})
    assert response.status_code == 409 and response.json["error"]["code"] == "webhook_event_conflict"


def test_complete_downloads_are_separate_from_latest(client):
    upload(client, filename="update.zip")
    upload(client, body=b"installer", filename="Axolotl-modern.exe", platform="windows", kind="installer", variant="modern", display_name="Windows x64 Modern Installer")
    payload = {"event_id": "downloads-1", "tag": "v1.0.0", "version": "1.0.0", "channel": "release", "published_at": "2026-08-30T00:00:00Z", "artifacts": [{"platform": "linux-x86_64", "architecture": "x86_64", "kind": "updater", "filename": "update.zip", "size": 7, "sha256": hashlib.sha256(b"payload").hexdigest(), "signature": "sig"}, {"platform": "windows", "architecture": "x86_64", "kind": "installer", "variant": "modern", "filename": "Axolotl-modern.exe", "size": 9, "sha256": hashlib.sha256(b"installer").hexdigest(), "signature": None, "display_name": "Windows x64 Modern Installer"}]}
    raw = json.dumps(payload).encode(); ts = str(int(time.time())); sig = hmac.new(b"webhook", (ts + ".").encode() + raw, hashlib.sha256).hexdigest()
    assert client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": sig}).status_code == 200
    latest = client.get("/latest?platform=linux-x86_64")
    assert latest.status_code == 200 and latest.json["platforms"].keys() == {"linux-x86_64"}
    downloads = client.get("/api/downloads/latest?channel=release")
    assert downloads.status_code == 200 and [x["kind"] for x in downloads.json["downloads"]] == ["installer"]
    assert downloads.json["downloads"][0]["label"] == "Windows x64 Modern Installer"
    with_updater = client.get("/api/downloads/latest?channel=release&include_updater=true")
    assert {x["kind"] for x in with_updater.json["downloads"]} == {"installer", "updater"}


def test_beta_channel_includes_release_and_beta(client, app):
    upload(client)
    payload = {
        "event_id": "beta-1",
        "tag": "v1.0.0",
        "version": "1.0.0",
        "channel": "release",
        "published_at": "2026-08-30T00:00:00Z",
        "artifacts": [{"platform": "linux-x86_64", "filename": "app.tar.gz", "size": 7, "sha256": hashlib.sha256(b"payload").hexdigest(), "signature": "sig"}],
    }
    raw = json.dumps(payload).encode(); ts = str(int(time.time())); sig = hmac.new(b"webhook", (ts + ".").encode() + raw, hashlib.sha256).hexdigest()
    client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": sig})
    with app.app_context():
        from pathlib import Path

        from app.extensions import db
        from app.models import Artifact, Version
        beta = Version(version="1.1.0-beta.1", channel="beta", status="published", published_at=Version.query.filter_by(version="1.0.0").first().published_at)
        db.session.add(beta); db.session.commit()
        Path(app.config["DIST_ROOT"], "1.1.0-beta.1").mkdir(parents=True, exist_ok=True)
        Path(app.config["DIST_ROOT"], "1.1.0-beta.1", "beta.tar.gz").write_bytes(b"x")
        db.session.add(Artifact(version_id=beta.id, platform="linux-x86_64", filename="beta.tar.gz", relative_path="dist/1.1.0-beta.1/beta.tar.gz", size=1, sha256="0" * 64, signature="sig", content_type="application/gzip")); db.session.commit()
    response = client.get("/latest?channel=beta&platform=linux-x86_64")
    assert response.status_code == 200
    assert response.json["version"] == "1.1.0-beta.1"


def test_revoke_restore(client):
    upload(client)
    payload = {"event_id": "e2", "tag": "v1.0.0", "version": "1.0.0", "channel": "release", "published_at": "2026-08-30T00:00:00Z", "artifacts": [{"platform": "linux-x86_64", "filename": "app.tar.gz", "size": 7, "sha256": hashlib.sha256(b"payload").hexdigest(), "signature": "sig"}]}
    raw = json.dumps(payload).encode(); ts = str(int(time.time())); sig = hmac.new(b"webhook", (ts + ".").encode() + raw, hashlib.sha256).hexdigest()
    client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": ts, "X-Webhook-Signature": sig})
    assert client.post("/api/admin/versions/1.0.0/revoke", headers={"Authorization": "Bearer admin"}, json={"reason": "bad"}).status_code == 200
    assert client.get("/latest?platform=linux-x86_64").status_code == 204
    assert client.post("/api/admin/versions/1.0.0/restore", headers={"Authorization": "Bearer admin"}).status_code == 200
    assert client.get("/latest?platform=linux-x86_64").status_code == 200
