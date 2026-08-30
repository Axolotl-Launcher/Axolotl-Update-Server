from pathlib import Path

from app.extensions import db
from app.models import Version
from app.services.retention import prune_dist


def test_prune_dist_keeps_three_versions_per_channel_and_db_rows(app):
    with app.app_context():
        for index in range(1, 5):
            release = Version(version=f"1.0.{index}", channel="release", status="published")
            beta = Version(version=f"2.0.0-beta.{index}", channel="beta", status="published")
            db.session.add_all([release, beta])
            Path(app.config["DIST_ROOT"], release.version).mkdir(parents=True)
            Path(app.config["DIST_ROOT"], beta.version).mkdir(parents=True)
        db.session.commit()

        removed = prune_dist()

        assert set(removed) == {"1.0.1", "2.0.0-beta.1"}
        for version in ("1.0.2", "1.0.3", "1.0.4", "2.0.0-beta.2", "2.0.0-beta.3", "2.0.0-beta.4"):
            assert Path(app.config["DIST_ROOT"], version).is_dir()
        assert Version.query.count() == 8
