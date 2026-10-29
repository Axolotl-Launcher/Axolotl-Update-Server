import json
from datetime import datetime, timezone

import sqlalchemy as sa
from flask_migrate import downgrade, upgrade

from app import create_app
from app.extensions import db
from app.models import Version, artifact_platforms


def test_target_platforms_migration_backfills_universal_macos_updater(tmp_path):
    class Config:
        TESTING = True
        FLASK_ENV = "production"
        SECRET_KEY = "migration-test"
        ADMIN_TOKEN = "admin"
        WEBHOOK_SECRET = "webhook"
        SQLALCHEMY_DATABASE_URI = f"sqlite:///{(tmp_path / 'migration.db').as_posix()}"
        SQLALCHEMY_TRACK_MODIFICATIONS = False
        DIST_ROOT = str(tmp_path / "dist")

    app = create_app(Config)
    with app.app_context():
        upgrade(revision="0007_webhook_event_version")
        now = datetime.now(timezone.utc)
        with db.engine.begin() as connection:
            tables = sa.MetaData()
            tables.reflect(bind=connection)
            first_version = connection.execute(tables.tables["version"].insert().values(
                version="1.10.3", channel="release", status="published",
                release_tag="v1.10.3", release_id="123", created_at=now, updated_at=now,
            )).inserted_primary_key[0]
            second_version = connection.execute(tables.tables["version"].insert().values(
                version="1.10.4", channel="release", status="published",
                release_tag="v1.10.4", release_id="124", created_at=now, updated_at=now,
            )).inserted_primary_key[0]
            for version_id, filename, platform, architecture in (
                (first_version, "Axolotl.Launcher_universal.app.tar.gz", "darwin-aarch64", "universal"),
                (first_version, "Axolotl.Launcher_1.10.3_amd64.AppImage.tar.gz", "linux-x86_64", "x86_64"),
                (second_version, "Axolotl.Launcher_universal.app.tar.gz", "darwin-aarch64", "universal"),
                (second_version, "Axolotl.Launcher_1.10.4_x64-setup.nsis.zip", "darwin-x86_64", "x86_64"),
            ):
                connection.execute(tables.tables["artifact"].insert().values(
                    version_id=version_id,
                    platform=platform,
                    architecture=architecture,
                    filename=filename,
                    relative_path=f"dist/{'1.10.3' if version_id == first_version else '1.10.4'}/{filename}",
                    size=1,
                    sha256="0" * 64,
                    content_type="application/octet-stream",
                    kind="updater",
                    uploaded_at=now,
                    created_at=now,
                    updated_at=now,
                ))

        upgrade()
        with db.engine.connect() as connection:
            rows = dict(connection.execute(sa.text(
                "SELECT version_id || ':' || filename, target_platforms FROM artifact"
            )).all())

        universal_key = f"{first_version}:Axolotl.Launcher_universal.app.tar.gz"
        assert json.loads(rows[universal_key]) == [
            "darwin-aarch64",
            "darwin-x86_64",
        ]
        assert rows[f"{first_version}:Axolotl.Launcher_1.10.3_amd64.AppImage.tar.gz"] is None
        assert rows[f"{second_version}:Axolotl.Launcher_universal.app.tar.gz"] is None
        second_record = Version.query.filter_by(version="1.10.4").one()
        second_universal = next(
            artifact
            for artifact in second_record.artifacts
            if artifact.architecture == "universal"
        )
        assert artifact_platforms(second_universal) == ["darwin-aarch64"]

        downgrade(revision="0007_webhook_event_version")
        assert "target_platforms" not in {
            column["name"] for column in sa.inspect(db.engine).get_columns("artifact")
        }
