from pathlib import Path

from app.extensions import db
from app.models import Artifact, Version
from app.services.retention import prune_dist


def add_imported_version(app, version):
    item = Version(
        version=version,
        channel="release",
        status="published",
        release_tag=f"v{version}",
        release_id=f"github-{version}",
    )
    db.session.add(item)
    db.session.flush()
    artifact = Artifact(
        version_id=item.id,
        platform="windows",
        architecture="x86_64",
        filename="setup.exe",
        relative_path=f"dist/{version}/setup.exe",
        size=1,
        sha256="0" * 64,
        content_type="application/octet-stream",
        kind="installer",
    )
    db.session.add(artifact)
    path = Path(app.config["DIST_ROOT"], version)
    path.mkdir(parents=True)
    (path / artifact.filename).write_bytes(b"x")


def test_pruned_github_artifact_redirects_to_github(client, app):
    with app.app_context():
        for patch in range(1, 5):
            add_imported_version(app, f"1.0.{patch}")
        db.session.commit()
        prune_dist()

    response = client.get("/dist/1.0.1/setup.exe", follow_redirects=False)

    assert response.status_code == 302
    assert response.headers["Location"] == (
        "https://github.com/Mystic-Stars/Axolotl/releases/download/v1.0.1/setup.exe"
    )


def test_missing_retained_or_unknown_artifact_returns_not_found(client, app):
    with app.app_context():
        add_imported_version(app, "1.0.4")
        db.session.commit()
        Path(app.config["DIST_ROOT"], "1.0.4", "setup.exe").unlink()

    assert client.get("/dist/1.0.4/setup.exe").status_code == 404
    assert client.get("/dist/does-not-exist/setup.exe").status_code == 404
