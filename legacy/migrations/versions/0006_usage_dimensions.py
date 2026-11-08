"""add version and platform dimensions to usage events

Revision ID: 0006_usage_dimensions
Revises: 0005_usage_events
"""
import sqlalchemy as sa
from alembic import op

revision = "0006_usage_dimensions"
down_revision = "0005_usage_events"
branch_labels = None
depends_on = None


def upgrade():
    inspector = sa.inspect(op.get_bind())
    columns = {column["name"] for column in inspector.get_columns("usage_event")}
    with op.batch_alter_table("usage_event") as batch_op:
        if "version" not in columns:
            batch_op.add_column(sa.Column("version", sa.String(length=64), nullable=True))
        if "platform" not in columns:
            batch_op.add_column(sa.Column("platform", sa.String(length=64), nullable=True))
    indexes = {index["name"] for index in inspector.get_indexes("usage_event")}
    if "ix_usage_event_version" not in indexes:
        op.create_index("ix_usage_event_version", "usage_event", ["version"])
    if "ix_usage_event_platform" not in indexes:
        op.create_index("ix_usage_event_platform", "usage_event", ["platform"])


def downgrade():
    op.drop_index("ix_usage_event_platform", table_name="usage_event")
    op.drop_index("ix_usage_event_version", table_name="usage_event")
    with op.batch_alter_table("usage_event") as batch_op:
        batch_op.drop_column("platform")
        batch_op.drop_column("version")
