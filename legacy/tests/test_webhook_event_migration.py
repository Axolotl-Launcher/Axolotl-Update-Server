from datetime import datetime, timezone

import sqlalchemy as sa
from flask_migrate import downgrade, upgrade

from app import create_app
from app.extensions import db


def test_event_version_migration_backfills_only_exact_legacy_event(tmp_path):
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
        upgrade(revision="0006_usage_dimensions")
        now = datetime.now(timezone.utc)
        with db.engine.begin() as connection:
            tables = sa.MetaData()
            tables.reflect(bind=connection)
            connection.execute(tables.tables["version"].insert().values(
                version="1.9.4", channel="release", status="published",
                release_tag="v1.9.4", release_id="123", created_at=now, updated_at=now,
            ))
            for event_id in ("github-v1.9.4-123", "github-v1.9.40-123", "custom-unknown"):
                connection.execute(tables.tables["webhook_event"].insert().values(
                    event_id=event_id, event_type="release", payload_hash="a" * 64,
                    received_at=now, status="processed",
                ))
        upgrade()
        with db.engine.connect() as connection:
            rows = dict(connection.execute(sa.text("SELECT event_id, version FROM webhook_event")).all())
        assert rows == {"github-v1.9.4-123": "1.9.4", "github-v1.9.40-123": None, "custom-unknown": None}
        downgrade(revision="0006_usage_dimensions")
        assert "version" not in {column["name"] for column in sa.inspect(db.engine).get_columns("webhook_event")}
        upgrade()
        assert "version" in {column["name"] for column in sa.inspect(db.engine).get_columns("webhook_event")}
