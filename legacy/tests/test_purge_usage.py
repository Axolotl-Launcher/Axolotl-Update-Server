from app import create_app
from app.extensions import db
from app.models import AuditLog, UsageEvent, Version


def test_purge_usage_deletes_only_statistics_and_compacts_sqlite(app):
    with app.app_context():
        db.session.add_all([
            UsageEvent(path="/latest", version="1.9.4", event_type="api"),
            UsageEvent(path="/dist/1.9.4/a.zip", version="1.9.4", event_type="download"),
            Version(version="1.9.4", channel="release", status="draft"),
            AuditLog(operator="admin", action="delete_version", version="1.9.3"),
        ])
        db.session.commit()
    response = app.test_cli_runner().invoke(args=["purge-usage"])
    assert response.exit_code == 0, response.output
    assert "Deleted 2 usage records" in response.output
    assert "VACUUM" in response.output
    with app.app_context():
        assert UsageEvent.query.count() == 0
        assert Version.query.count() == 1
        assert AuditLog.query.count() == 1
    assert app.test_cli_runner().invoke(args=["purge-usage"]).exit_code == 0


def test_sqlite_file_really_shrinks_after_purge(app, tmp_path):
    database = tmp_path / "usage.db"
    config = type("FileConfig", (), {
        **app.config, "SQLALCHEMY_DATABASE_URI": f"sqlite:///{database.as_posix()}",
        "DIST_ROOT": str(tmp_path / "dist"),
    })
    file_app = create_app(config)
    with file_app.app_context():
        db.session.add(Version(version="1.9.4", channel="release", status="published"))
        db.session.add_all(UsageEvent(path="x" * 500, event_type="api") for _ in range(2000))
        db.session.commit()
    before = database.stat().st_size
    result = file_app.test_cli_runner().invoke(args=["purge-usage"])
    assert result.exit_code == 0, result.output
    assert database.stat().st_size < before / 2
    with file_app.app_context():
        assert UsageEvent.query.count() == 0
        assert Version.query.count() == 1
