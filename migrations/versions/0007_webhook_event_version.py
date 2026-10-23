"""Associate release webhook events with their version.

Revision ID: 0007_webhook_event_version
Revises: 0006_usage_dimensions
"""
import sqlalchemy as sa
from alembic import op

revision = "0007_webhook_event_version"
down_revision = "0006_usage_dimensions"
branch_labels = None
depends_on = None


def upgrade():
    connection = op.get_bind()
    inspector = sa.inspect(connection)
    if "version" not in {column["name"] for column in inspector.get_columns("webhook_event")}:
        with op.batch_alter_table("webhook_event") as batch:
            batch.add_column(sa.Column("version", sa.String(64), nullable=True))
    if "ix_webhook_event_version" not in {index["name"] for index in sa.inspect(connection).get_indexes("webhook_event")}:
        op.create_index("ix_webhook_event_version", "webhook_event", ["version"])
    # Legacy payloads were not stored; associate only exact workflow event IDs.
    versions = connection.execute(sa.text("SELECT version, release_tag, release_id FROM version"))
    for version, tag, release_id in versions:
        if tag and release_id:
            connection.execute(sa.text(
                "UPDATE webhook_event SET version = :version "
                "WHERE version IS NULL AND event_id = :event_id"
            ), {"version": version, "event_id": f"github-{tag}-{release_id}"})


def downgrade():
    with op.batch_alter_table("webhook_event") as batch:
        batch.drop_index("ix_webhook_event_version")
        batch.drop_column("version")
