from pathlib import Path

import pytest

from app import create_app
from app.extensions import db
from app.services.dist_server import normalize_dist_server
from tests.test_api import configure_downloads, payload_for, signed_post


@pytest.mark.parametrize("method", ["GET", "HEAD"])
@pytest.mark.parametrize("asset", ["setup.exe", "update.tar.gz.sig", "manifest.json", "subdir/portable.zip"])
def test_all_dist_paths_redirect_without_database_access(client, app, monkeypatch, method, asset):
    app.config["DIST_SERVER"] = "https://downloads.example.com"

    def forbidden(*args, **kwargs):
        pytest.fail("DIST_SERVER redirects must not query the database")

    monkeypatch.setattr(db.session, "execute", forbidden)
    response = client.open(f"/dist/1.10.5-beta.1/{asset}?download=1&token=a%2Bb", method=method)
    assert response.status_code == 302
    assert response.headers["Location"] == f"https://downloads.example.com/dist/1.10.5-beta.1/{asset}?download=1&token=a%2Bb"
    assert response.headers["Cache-Control"] == "no-store"
    assert response.headers["Cloudflare-CDN-Cache-Control"] == "no-store"
    if method == "HEAD":
        assert not response.data


def test_local_file_and_range_also_redirect(client, app):
    directory = Path(app.config["DIST_ROOT"], "1.9.4")
    directory.mkdir()
    (directory / "setup.exe").write_bytes(b"local package")
    app.config["DIST_SERVER"] = "https://downloads.example.com"
    response = client.get("/dist/1.9.4/setup.exe", headers={"Range": "bytes=0-2"})
    assert response.status_code == 302
    assert response.headers["Location"] == "https://downloads.example.com/dist/1.9.4/setup.exe"


def test_dist_server_base_path_and_encoded_asset(client, app):
    app.config["DIST_SERVER"] = "https://downloads.example.com/storage"
    response = client.get("/dist/1.9.4/Axolotl%20%E5%AE%89%E8%A3%85%23%3F.exe")
    assert response.headers["Location"] == "https://downloads.example.com/storage/dist/1.9.4/Axolotl%20%E5%AE%89%E8%A3%85%23%3F.exe"


@pytest.mark.parametrize("path", ["/dist/1.9.4/../outside", "/dist/../secret", "/dist/1.9.4/a%5Cb.exe", "/dist/1.9.4/a%00.exe"])
def test_unsafe_dist_paths_are_rejected(client, app, path):
    app.config["DIST_SERVER"] = "https://downloads.example.com"
    response = client.get(path)
    assert response.status_code == 400
    assert "Location" not in response.headers


def test_unset_dist_server_retains_local_downloads(client, app):
    directory = Path(app.config["DIST_ROOT"], "1.9.4")
    directory.mkdir()
    (directory / "setup.exe").write_bytes(b"package")
    response = client.get("/dist/1.9.4/setup.exe", headers={"Range": "bytes=0-2"})
    assert response.status_code == 206 and response.data == b"pac"
    assert client.get("/dist/unknown/no-file.zip").status_code == 404


@pytest.mark.parametrize("server", ["ftp://downloads.example.com", "//downloads.example.com", "https://user:secret@downloads.example.com", "https://downloads.example.com/?a=1", "https://downloads.example.com/#fragment", "https://downloads.example.com:bad", "https://downloads.example.com/../other", "https://downloads.example.com\nInjected", "https://update.axlmc.org/", "http://update.axlmc.org", "https://update.axlmc.org:443", "https://update.axlmc.org.", "/", "https://downloads.example.com\\evil"])
def test_invalid_dist_server_fails_at_startup(app, server):
    config = type("InvalidDistConfig", (), {**app.config, "DIST_SERVER": server})
    with pytest.raises(ValueError, match="DIST_SERVER"):
        create_app(config)


def test_server_normalization_and_runtime_alias_loop(client, app):
    assert normalize_dist_server(" https://downloads.example.com/// ", "https://update.axlmc.org") == "https://downloads.example.com"
    assert normalize_dist_server("", "https://update.axlmc.org") == ""
    app.config["DIST_SERVER"] = "http://localhost"
    assert client.get("/dist/1.9.4/a.zip").status_code == 503


def test_startup_normalizes_dist_server(app):
    config = type("DistConfig", (), {**app.config, "DIST_SERVER": " https://downloads.example.com/// "})
    configured = create_app(config)
    assert configured.config["DIST_SERVER"] == "https://downloads.example.com"
    response = configured.test_client().get("/dist/1.9.4/setup.exe")
    assert response.headers["Location"] == "https://downloads.example.com/dist/1.9.4/setup.exe"


def test_latest_and_download_catalog_keep_public_update_urls(client, app):
    app.config["DIST_SERVER"] = "https://downloads.example.com"
    configure_downloads(app, {"update.tar.gz.sig": b"tauri-signature\n"})
    assert signed_post(client, payload_for()).status_code == 200
    latest_url = client.get("/latest?platform=linux-x86_64").json["platforms"]["linux-x86_64"]["url"]
    download_url = client.get("/api/downloads/1.9.4").json["downloads"][0]["url"]
    assert latest_url == "https://update.axlmc.org/dist/1.9.4/update.tar.gz"
    assert download_url == "https://update.axlmc.org/dist/1.9.4/installer.exe"
    assert client.get("/dist/1.9.4/update.tar.gz").headers["Location"] == "https://downloads.example.com/dist/1.9.4/update.tar.gz"
