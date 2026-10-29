import hashlib
import hmac
import io
import json
import time
from pathlib import Path

import pytest


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
        url = f"https://github.com/Axolotl-Launcher/Axolotl/releases/download/{tag}/{filename}"
        assets.append({"name": filename, "size": len(body), "digest": f"sha256:{digest}", "browser_download_url": url})
        catalog_files.append({"filename": filename, "size": len(body), "sha256": digest, "downloadUrl": url})
    return {"event_id": f"github-{version}-1", "tag": tag, "version": version, "channel": channel, "force_update": force_update, "release": {"id": 123, "tag_name": tag, "body": "Release notes", "published_at": "2026-08-29T15:19:00Z", "draft": False, "assets": assets}, "catalog": {"version": version, "files": catalog_files, "artifacts": [{"filename": "update.tar.gz", "platform": "linux-x86_64", "architecture": "x86_64", "kind": "updater", "signatureFilename": "update.tar.gz.sig"}, {"filename": "installer.exe", "platform": "windows", "architecture": "x86_64", "kind": "installer", "variant": "modern"}]}}


def universal_payload():
    bodies = {
        "Axolotl.Launcher_universal.app.tar.gz": b"universal-update",
        "Axolotl.Launcher_universal.app.tar.gz.sig": b"universal-signature\n",
        "installer.exe": b"installer",
    }
    payload = payload_for(bodies)
    payload["catalog"]["artifacts"][0].update({
        "filename": "Axolotl.Launcher_universal.app.tar.gz",
        "platform": "darwin-aarch64",
        "targetPlatforms": ["darwin-aarch64", "darwin-x86_64"],
        "architecture": "universal",
        "signatureFilename": "Axolotl.Launcher_universal.app.tar.gz.sig",
    })
    return bodies, payload


def configure_downloads(app, bodies, version="1.9.4"):
    urls = {f"https://github.com/Axolotl-Launcher/Axolotl/releases/download/v{version}/{filename}": body for filename, body in bodies.items()}
    app.config["GITHUB_DOWNLOAD_OPEN"] = lambda url, _timeout: io.BytesIO(urls[url])


def test_health_and_stream_import(client, app):
    assert client.get("/api/health").status_code == 200
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"tauri-signature\n", "installer.exe": b"installer"}
    configure_downloads(app, bodies)
    assert signed_post(client, payload_for(bodies)).status_code == 200
    assert client.get("/latest?platform=linux-x86_64").json["version"] == "1.9.4"
    assert Path(app.config["DIST_ROOT"], "1.9.4", "update.tar.gz").read_bytes() == b"update"
    assert client.get("/api/downloads/1.9.4").json["downloads"][0]["kind"] == "installer"


def test_universal_updater_is_available_to_both_darwin_targets(client, app):
    bodies, payload = universal_payload()
    configure_downloads(app, bodies)

    assert signed_post(client, payload).status_code == 200

    arm = client.get("/latest?platform=darwin-aarch64").json
    intel = client.get("/latest?platform=darwin-x86_64").json
    assert arm["version"] == intel["version"] == "1.9.4"
    assert arm["platforms"]["darwin-aarch64"] == intel["platforms"]["darwin-x86_64"]
    assert arm["platforms"]["darwin-aarch64"]["url"].endswith(
        "/dist/1.9.4/Axolotl.Launcher_universal.app.tar.gz"
    )

    generic = client.get("/latest").json
    assert set(generic["platforms"]) == {"darwin-aarch64", "darwin-x86_64"}
    detail = client.get("/api/versions/1.9.4").json
    universal = next(
        artifact
        for artifact in detail["artifacts"]
        if artifact["filename"] == "Axolotl.Launcher_universal.app.tar.gz"
    )
    assert universal["target_platforms"] == ["darwin-aarch64", "darwin-x86_64"]


@pytest.mark.parametrize(
    "targets",
    [
        [],
        ["darwin-x86_64"],
        ["darwin-aarch64", "unsupported-platform"],
        ["darwin-aarch64", "darwin-aarch64"],
    ],
)
def test_updater_rejects_invalid_target_platforms(client, app, targets):
    bodies, payload = universal_payload()
    payload["catalog"]["artifacts"][0]["targetPlatforms"] = targets
    configure_downloads(app, bodies)

    response = signed_post(client, payload)

    assert response.status_code == 400
    assert response.json["error"]["code"] == "invalid_artifact_platform"


def test_updater_rejects_overlapping_target_platforms(client, app):
    bodies = {
        "arm.tar.gz": b"arm-update",
        "arm.tar.gz.sig": b"arm-signature\n",
        "intel.tar.gz": b"intel-update",
        "intel.tar.gz.sig": b"intel-signature\n",
    }
    payload = payload_for(bodies)
    payload["catalog"]["artifacts"] = [
        {
            "filename": "arm.tar.gz",
            "platform": "darwin-aarch64",
            "targetPlatforms": ["darwin-aarch64", "darwin-x86_64"],
            "architecture": "universal",
            "kind": "updater",
            "signatureFilename": "arm.tar.gz.sig",
        },
        {
            "filename": "intel.tar.gz",
            "platform": "darwin-x86_64",
            "targetPlatforms": ["darwin-x86_64"],
            "architecture": "x86_64",
            "kind": "updater",
            "signatureFilename": "intel.tar.gz.sig",
        },
    ]
    configure_downloads(app, bodies)

    response = signed_post(client, payload)

    assert response.status_code == 400
    assert response.json["error"]["code"] == "duplicate_artifact"


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


def test_beta_import_from_current_github_repository(client, app):
    version = "1.10.0-beta.5"
    bodies = {"update.tar.gz": b"update", "update.tar.gz.sig": b"tauri-signature\n", "installer.exe": b"installer"}
    configure_downloads(app, bodies, version=version)
    payload = payload_for(bodies, version=version, channel="beta")

    response = signed_post(client, payload)

    assert response.status_code == 200
    assert response.json["status"] == "published"
    latest = client.get("/latest?channel=beta&platform=linux-x86_64").json
    assert latest["version"] == version
    assert latest["platforms"]["linux-x86_64"]["signature"] == "tauri-signature"
    assert Path(app.config["DIST_ROOT"], version, "update.tar.gz").read_bytes() == b"update"
    assert signed_post(client, payload).json["idempotent"] is True


@pytest.mark.parametrize("url", [
    "https://github.com/Mystic-Stars/Axolotl/releases/download/v1.9.4/update.tar.gz",
    "https://github.com/Axolotl-Launcher/Other/releases/download/v1.9.4/update.tar.gz",
    "http://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz",
    "https://example.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz",
    "https://github.com/Axolotl-Launcher/Axolotl/releases/download/untagged-example/update.tar.gz",
    "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.5/update.tar.gz",
    "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/other.tar.gz",
    "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz?download=1",
    "https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz#fragment",
    "https://user:password@github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz",
    "https://github.com:443/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz",
])
def test_github_repository_change_preserves_url_restrictions(client, url):
    payload = payload_for()
    payload["release"]["assets"][0]["browser_download_url"] = url
    payload["catalog"]["files"][0]["downloadUrl"] = url

    response = signed_post(client, payload)

    assert response.status_code == 400
    assert response.json["error"]["code"] == "invalid_github_asset_url"


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
