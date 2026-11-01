import click
from sqlalchemy import delete

from .extensions import db
from .models import UsageEvent


def register_commands(app):
    @app.cli.command("purge-usage")
    def purge_usage():
        """Delete all historical usage statistics and compact SQLite. Stop workers first."""
        try:
            result = db.session.execute(delete(UsageEvent))
            count = result.rowcount
            db.session.commit()
        except Exception as exc:
            db.session.rollback()
            raise click.ClickException("Could not delete usage history; stop the service and retry.") from exc
        click.echo(f"Deleted {count} usage records. Version, artifact, webhook and audit data retained.")
        db.session.remove()
        if db.engine.dialect.name == "sqlite":
            try:
                with db.engine.connect().execution_options(isolation_level="AUTOCOMMIT") as connection:
                    connection.exec_driver_sql("VACUUM")
                    connection.exec_driver_sql("PRAGMA wal_checkpoint(TRUNCATE)")
            except Exception as exc:
                raise click.ClickException(
                    "Usage history was deleted but SQLite compaction failed. "
                    "Stop all database users, ensure enough free disk space, then rerun purge-usage."
                ) from exc
            click.echo("SQLite VACUUM and WAL checkpoint completed.")
