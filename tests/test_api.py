import hashlib
import hmac
import io
import json
import time
from pathlib import Path


def signed_post(client, payload):
    raw = json.dumps(payload, separators=(",", ":")).encode()
    timestamp = str(int(time.time()))
    signature = hmac.new(b"webhook", f"{timestamp}.".encode() + raw, hashlib.sha256).hexdigest()
    return client.post("/api/webhook/release", data=raw, content_type="application/json", headers={"X-Webhook-Timestamp": timestamp, "X-Webhook-Signature": f"sha256={signature}"})


def payload_for(files=None, *, version="1.9.4", channel="release", force_update=False):
    files = files or {"update.tar.gz": b"update", "update.tar.gz.sig": b"tauri-signature\n", "installer.exe": b"installer"}
    tag = f"v{version}"
    assets, catalog_files = [], []
    for filename, body in files.items():
        digest = hashlib.sha256(body).hexdigest()
        url = f"https://github.com/Mystic-Stars/Axolotl/releases/download/{tag}/{filename}"
        assets.append({"name": filename, "size": len(body), "digest": f"sha256:{digest}", "browser_download_url": url})
        catalog_files.append({"filename": filename, "size": len(body), "sha256": digest, "downloadUrl": url})
    return {"event_id": f"github-{version}-1", "tag": tag, "version": version, "channel": channel, "force_update": force_update, "release": {"id": 123, "tag_name": tag, "body": "Release notes", "published_at": "2026-08-29T15:19:00Z", "draft": False, "assets": assets}, "catalog": {"version": version, "files": catalog_files, "artifacts": [{"filename": "update.tar.gz", "platform": "linux-x86_64", "architecture": "x86_64", "kind": "updater", "signatureFilename": "update.tar.gz.sig"}, {"filename": "installer.exe", "platform": "windows", "architecture": "x86_64", "kind": "installer", "variant": "modern"}]}}


def configure_downloads(app, bodies, version="1.9.4"):
    urls = {f"https://github.com/Mystic-Stars/Axolotl/releases/download/v{version}/{filename}": body for filename, body in bodies.items()}
    app.config["GITHUB_DOWNLOAD_OPEN"] = lambda url, _timeout: io.BytesIO(urls[url])


def test_health_and_stream_import(client, app):
    assert client.get("/api/health").status_code == 200
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"tauri-signature\n", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    assert signed_post(client, payload_for(bodies)).status_code == 200
    assert client.get("/latest?platform=linux-x86_64").json["version"] == "1.9.4"
    assert Path(app.config["DIST_ROOT"], "1.9.4", "update.tar.gz").read_bytes() == b"update"
    assert client.get("/api/downloads/1.9.4").json["downloads"][0]["kind"] == "installer"


def test_catalog_and_release_validation(client, app):
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"sig", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    bad = payload_for(bodies); bad["event_id"] = "draft"; bad["release"]["draft"] = True
    assert signed_post(client, bad).json["error"]["code"] == "invalid_github_release"
    bad = payload_for(bodies); bad["event_id"] = "size"; bad["catalog"]["files"][0]["size"] += 1
    assert signed_post(client, bad).json["error"]["code"] == "github_asset_metadata_mismatch"
    bad = payload_for(bodies); bad["event_id"] = "url"; bad["catalog"]["files"][0]["downloadUrl"] = "https://example.com/x"
    bad["release"]["assets"][0]["browser_download_url"] = "https://example.com/x"
    assert signed_post(client, bad).json["error"]["code"] == "invalid_github_asset_url"
    bad = payload_for(bodies); bad["event_id"] = "hash"; bad["catalog"]["files"][0]["sha256"] = "0" * 64
    assert signed_post(client, bad).json["error"]["code"] == "github_asset_metadata_mismatch"
    bad = payload_for(bodies); bad["event_id"] = "digest"; bad["release"]["assets"][0].pop("digest")
    assert signed_post(client, bad).json["error"]["code"] == "invalid_github_digest"


def test_updater_requires_signature(client, app):
    bodies = {"update.tar.gz": b"update", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    bad = payload_for(bodies)
    bad["catalog"]["files"] = [x for x in bad["catalog"]["files"] if x["filename"] != "update.tar.gz.sig"]
    bad["release"]["assets"] = [x for x in bad["release"]["assets"] if x["name"] != "update.tar.gz.sig"]
    assert signed_post(client, bad).json["error"]["code"] in {"missing_signature", "invalid_catalog"}


def test_idempotency_and_failed_retry(client, app):
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"sig", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    payload = payload_for(bodies)
    assert signed_post(client, payload).status_code == 200
    assert signed_post(client, payload).json["idempotent"] is True
    retry = payload_for(bodies, version="1.9.5"); retry["event_id"] = "retry-event"
    app.config["GITHUB_DOWNLOAD_OPEN"] = lambda _url, _timeout: (_ for _ in ()).throw(OSError("fixture failure"))
    assert signed_post(client, retry).status_code == 502
    configure_downloads(app, bodies, version="1.9.5")
    assert signed_post(client, retry).status_code == 200


def test_force_update_and_published_at(client, app):
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"sig", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    assert signed_post(client, payload_for(bodies, force_update=True)).status_code == 200
    latest = client.get("/latest?platform=linux-x86_64").json
    assert latest["force_update"] is True and latest["published_at"] == "2026-08-29T15:19:00Z"


def test_failed_import_does_not_change_pointer(client, app):
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"sig", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    assert signed_post(client, payload_for(bodies)).status_code == 200
    broken = payload_for(bodies, version="1.9.5")
    app.config["GITHUB_DOWNLOAD_OPEN"] = lambda _url, _timeout: (_ for _ in ()).throw(OSError("fixture failure"))
    assert signed_post(client, broken).status_code == 502
    assert client.get("/latest?platform=linux-x86_64").json["version"] == "1.9.4"
