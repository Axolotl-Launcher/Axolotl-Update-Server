import hashlib
import hmac
import json
import time


def upload(client, body=b"payload", filename="app.tar.gz", platform="linux-x86_64"):
    return client.put(
        f"/api/artifacts/1.0.0/{filename}",
        data=body,
        headers={"X-Upload-Token": "upload", "X-Axolotl-Platform": platform, "X-Axolotl-Signature": "sig"},
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
        from app.extensions import db
        from app.models import Artifact, Version
        beta = Version(version="1.1.0-beta.1", channel="beta", status="published", published_at=Version.query.filter_by(version="1.0.0").first().published_at)
        db.session.add(beta); db.session.commit()
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
