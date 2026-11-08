from pathlib import Path

from sqlalchemy import event

from app import create_app
from app.extensions import db
from app.models import Artifact, Version, utcnow
from app.services.latest_cache import LatestCache


def seed(app, version="1.9.4", channel="release"):
    with app.app_context():
        item = Version(version=version, channel=channel, status="published", published_at=utcnow())
        directory = Path(app.config["DIST_ROOT"], version)
        directory.mkdir()
        (directory / "update.zip").write_bytes(b"package")
        item.artifacts.append(Artifact(
            platform="linux-x86_64", filename="update.zip", kind="updater", signature="sig",
            relative_path=f"dist/{version}/update.zip", size=7, sha256="0" * 64, content_type="application/zip",
        ))
        db.session.add(item)
        db.session.commit()


def test_latest_caches_queries_and_has_consistent_200_204_304_headers(client, app):
    seed(app)
    with app.app_context():
        queries = []

        def count(conn, cursor, statement, parameters, context, executemany):
            queries.append(statement)

        event.listen(db.engine, "before_cursor_execute", count)
        try:
            first = client.get("/latest?platform=linux-x86_64&current_version=1.9.3")
            assert queries
            queries.clear()
            second = client.get("/latest?platform=linux-x86_64&current_version=1.9.3")
            assert queries == [] and second.json == first.json
            conditional = client.get("/latest?platform=linux-x86_64&current_version=1.9.3", headers={"If-None-Match": first.headers["ETag"]})
            assert conditional.status_code == 304 and queries == []
        finally:
            event.remove(db.engine, "before_cursor_execute", count)
    empty = client.get("/latest?platform=linux-x86_64&current_version=1.9.4")
    assert empty.status_code == 204
    for response in (first, conditional, empty):
        assert "s-maxage=30" in response.headers["Cache-Control"]
        assert response.headers["Cloudflare-CDN-Cache-Control"] == "public, max-age=30"
        assert "X-Axolotl-Channel" in response.headers["Vary"]


def test_header_variants_and_failures_cannot_poison_cloudflare_default_key(client, app):
    seed(app)
    response = client.get("/latest", headers={"X-Axolotl-Channel": "release", "X-Axolotl-Version": "1.9.3"})
    assert response.status_code == 200
    assert response.headers["Cloudflare-CDN-Cache-Control"] == "no-store"
    assert "private" in response.headers["Cache-Control"]
    assert client.get("/latest?channel=invalid").headers["Cache-Control"] == "no-store"
    assert client.get("/latest?current_version=invalid").headers["Cache-Control"] == "no-store"
    assert client.get("/latest", headers={"Authorization": "Bearer secret"}).headers["Cache-Control"] == "no-store"


def test_verified_native_cdn_header_key_can_enable_shared_cache(client, app):
    seed(app)
    app.config["LATEST_CDN_HEADER_CACHE_KEY"] = True
    response = client.get("/latest", headers={"X-Axolotl-Channel": "beta"})
    assert response.headers["Cloudflare-CDN-Cache-Control"] == "public, max-age=30"
    assert "s-maxage=30" in response.headers["Cache-Control"]


def test_cache_separates_channels_platforms_versions_and_invalidates_after_commit(client, app):
    seed(app)
    assert client.get("/latest?platform=linux-x86_64").json["version"] == "1.9.4"
    assert client.get("/latest?platform=windows-x86_64").status_code == 204
    assert client.get("/latest?current_version=1.9.4").status_code == 204
    seed(app, "1.9.5-beta.1", "beta")
    assert client.get("/latest?channel=beta&platform=linux-x86_64").json["version"] == "1.9.5-beta.1"
    assert client.get("/latest?channel=release&platform=linux-x86_64").json["version"] == "1.9.4"
    response = client.post("/api/admin/versions/1.9.4/revoke", headers={"Authorization": "Bearer admin"})
    assert response.status_code == 200
    assert client.get("/latest?platform=linux-x86_64").status_code == 204


def test_revision_invalidates_other_worker_cache_and_ttl_expires(tmp_path, monkeypatch):
    clock = [0.0]
    monkeypatch.setattr("app.services.latest_cache.monotonic", lambda: clock[0])
    a = LatestCache(tmp_path / "revision")
    b = LatestCache(tmp_path / "revision")
    assert b.get_or_build(("release",), 30, lambda: "old") == "old"
    a.invalidate()
    assert b.get_or_build(("release",), 30, lambda: "new") == "new"
    clock[0] = 31
    assert b.get_or_build(("release",), 30, lambda: "expired") == "expired"


def test_latest_reads_succeed_while_another_connection_holds_write_lock(app, tmp_path):
    config = type("LockedConfig", (), {
        **app.config, "SQLALCHEMY_DATABASE_URI": f"sqlite:///{(tmp_path / 'locked.db').as_posix()}",
        "DIST_ROOT": str(tmp_path / "dist"),
    })
    locked_app = create_app(config)
    seed(locked_app)
    with locked_app.app_context(), db.engine.connect() as writer:
        writer.exec_driver_sql("BEGIN IMMEDIATE")
        writer.exec_driver_sql("UPDATE version SET notes = 'import still running'")
        try:
            client = locked_app.test_client()
            for _ in range(20):
                assert client.get("/latest?platform=linux-x86_64").status_code == 200
            assert client.get("/api/health").status_code == 200
        finally:
            writer.rollback()
