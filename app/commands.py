import click
from sqlalchemy import delete

from .errors import ApiError
from .extensions import db
from .models import UsageEvent, Version
from .services.purge_dist import convert_and_purge


def register_commands(app):
    @app.cli.command("purge-dist-assets")
    @click.option("--version", "selected_version", help="Only convert this version; default: all Release and Beta versions.")
    @click.option("--dry-run", is_flag=True, help="Validate and list versions without deleting files.")
    def purge_dist_assets(selected_version, dry_run):
        """Convert existing Release/Beta versions to metadata-only storage. Stop workers first."""
        query = Version.query.filter(Version.channel.in_(("release", "beta")))
        if selected_version:
            query = query.filter_by(version=selected_version)
        versions = [item.version for item in query.order_by(Version.id).all()]
        if selected_version and not versions:
            raise click.ClickException("Version not found.")
        db.session.remove()
        for version in versions:
            try:
                result = convert_and_purge(version, dry_run=dry_run)
            except Exception as exc:
                db.session.rollback()
                message = exc.message if isinstance(exc, ApiError) else "Conversion or file cleanup failed; retry after checking permissions."
                raise click.ClickException(f"{version}: {message} Earlier versions may already be converted; rerunning is safe.") from exc
            label = "Would remove" if dry_run else "Removed"
            click.echo(f"{version} ({result['channel']}): {label} {result['entries']} entries; manifest and metadata retained.")
            db.session.remove()
        click.echo(f"{'Validated' if dry_run else 'Completed'} {len(versions)} versions.")

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
