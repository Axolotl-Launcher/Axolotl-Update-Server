"""create update server schema

Revision ID: 0001_initial
Revises:
"""
import sqlalchemy as sa
from alembic import op

revision = "0001_initial"
down_revision = None
branch_labels = None
depends_on = None


def upgrade():
    inspector = sa.inspect(op.get_bind())
    tables = set(inspector.get_table_names())
    if "version" not in tables:
        op.create_table(
            "version",
            sa.Column("id", sa.Integer(), primary_key=True),
            sa.Column("version", sa.String(64), nullable=False),
            sa.Column("channel", sa.String(16), nullable=False),
            sa.Column("status", sa.String(16), nullable=False, server_default="draft"),
            sa.Column("notes", sa.Text(), server_default=""),
            sa.Column("release_tag", sa.String(128), server_default=""),
            sa.Column("release_id", sa.String(128), server_default=""),
            sa.Column("published_at", sa.DateTime(timezone=True)),
            sa.Column("minimum_version", sa.String(64)),
            sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
            sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
            sa.Column("revoked_at", sa.DateTime(timezone=True)),
            sa.Column("revoke_reason", sa.Text()),
            sa.Column("force_update", sa.Boolean(), nullable=False, server_default="0"),
            sa.UniqueConstraint("version"),
        )
        op.create_index("ix_version_version", "version", ["version"])
        op.create_index("ix_version_channel", "version", ["channel"])
        op.create_index("ix_version_status", "version", ["status"])
    if "artifact" not in tables:
        op.create_table(
            "artifact", sa.Column("id", sa.Integer(), primary_key=True),
            sa.Column("version_id", sa.Integer(), sa.ForeignKey("version.id"), nullable=False),
            sa.Column("platform", sa.String(64), nullable=False), sa.Column("architecture", sa.String(32)),
            sa.Column("filename", sa.String(255), nullable=False), sa.Column("relative_path", sa.String(512), nullable=False, unique=True),
            sa.Column("size", sa.BigInteger(), nullable=False), sa.Column("sha256", sa.String(64), nullable=False),
            sa.Column("signature", sa.Text()), sa.Column("content_type", sa.String(128), nullable=False),
            sa.Column("uploaded_at", sa.DateTime(timezone=True), nullable=False), sa.Column("created_at", sa.DateTime(timezone=True), nullable=False), sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        )
    if "channel_pointer" not in tables:
        op.create_table("channel_pointer", sa.Column("channel", sa.String(16), primary_key=True), sa.Column("current_version", sa.String(64)), sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False))
    if "webhook_event" not in tables:
        op.create_table("webhook_event", sa.Column("event_id", sa.String(255), primary_key=True), sa.Column("event_type", sa.String(64), nullable=False), sa.Column("payload_hash", sa.String(64), nullable=False), sa.Column("received_at", sa.DateTime(timezone=True), nullable=False), sa.Column("processed_at", sa.DateTime(timezone=True)), sa.Column("status", sa.String(16), nullable=False), sa.Column("error_message", sa.Text()))
    if "audit_log" not in tables:
        op.create_table("audit_log", sa.Column("id", sa.Integer(), primary_key=True), sa.Column("operator", sa.String(128), nullable=False), sa.Column("action", sa.String(64), nullable=False), sa.Column("channel", sa.String(16)), sa.Column("version", sa.String(64)), sa.Column("reason", sa.Text()), sa.Column("request_id", sa.String(64)), sa.Column("ip_address", sa.String(64)), sa.Column("created_at", sa.DateTime(timezone=True), nullable=False))


def downgrade():
    for table in ("audit_log", "webhook_event", "channel_pointer", "artifact", "version"):
        op.drop_table(table)
