"""add API and download usage events

Revision ID: 0005_usage_events
Revises: 0004_artifact_signature_filename
"""
import sqlalchemy as sa
from alembic import op

revision = "0005_usage_events"
down_revision = "0004_artifact_signature_filename"
branch_labels = None
depends_on = None


def upgrade():
    op.create_table(
        "usage_event",
        sa.Column("id", sa.Integer(), primary_key=True),
        sa.Column("occurred_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("path", sa.String(length=512), nullable=False),
        sa.Column("channel", sa.String(length=16), nullable=False, server_default="unknown"),
        sa.Column("event_type", sa.String(length=16), nullable=False, server_default="api"),
        sa.Column("status_code", sa.Integer(), nullable=False, server_default="200"),
        sa.Column("bytes_sent", sa.BigInteger(), nullable=False, server_default="0"),
    )
    op.create_index("ix_usage_event_occurred_at", "usage_event", ["occurred_at"])
    op.create_index("ix_usage_event_channel", "usage_event", ["channel"])
    op.create_index("ix_usage_event_event_type", "usage_event", ["event_type"])


def downgrade():
    op.drop_index("ix_usage_event_event_type", table_name="usage_event")
    op.drop_index("ix_usage_event_channel", table_name="usage_event")
    op.drop_index("ix_usage_event_occurred_at", table_name="usage_event")
    op.drop_table("usage_event")
