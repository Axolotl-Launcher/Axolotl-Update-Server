from datetime import datetime, timezone

import sqlalchemy as sa
from flask_migrate import downgrade, upgrade

from app import create_app
from app.extensions import db


def test_storage_migration_preserves_legacy_local_releases_and_schema_round_trip(tmp_path):
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
        upgrade(revision="0008_artifact_target_platforms")
        with db.engine.begin() as connection:
            tables = sa.MetaData()
            tables.reflect(bind=connection)
            for version, channel in (("1.9.4", "release"), ("1.9.5-beta.1", "beta")):
                connection.execute(tables.tables["version"].insert().values(
                    version=version, channel=channel, status="published",
                    created_at=datetime.now(timezone.utc), updated_at=datetime.now(timezone.utc),
                ))
        payload = tmp_path / "dist" / "1.9.4" / "package.zip"
        payload.parent.mkdir()
        payload.write_bytes(b"keep legacy package")
        upgrade()
        with db.engine.connect() as connection:
            modes = dict(connection.execute(sa.text("SELECT version, artifact_storage FROM version")).all())
        assert modes == {"1.9.4": "local", "1.9.5-beta.1": "local"}
        assert payload.read_bytes() == b"keep legacy package"
        downgrade(revision="0008_artifact_target_platforms")
        assert "artifact_storage" not in {column["name"] for column in sa.inspect(db.engine).get_columns("version")}
        upgrade()
        assert "artifact_storage" in {column["name"] for column in sa.inspect(db.engine).get_columns("version")}
