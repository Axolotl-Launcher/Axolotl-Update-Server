"""add artifact package metadata

Revision ID: 0003_artifact_package_metadata
Revises: 0002_force_update
"""
import sqlalchemy as sa
from alembic import op

revision = "0003_artifact_package_metadata"
down_revision = "0002_force_update"
branch_labels = None
depends_on = None


def upgrade():
    inspector = sa.inspect(op.get_bind())
    columns = {column["name"] for column in inspector.get_columns("artifact")}
    additions = [
        ("kind", sa.String(16), "updater"),
        ("variant", sa.String(32), ""),
        ("display_name", sa.String(160), ""),
        ("sort_order", sa.Integer(), "0"),
        ("is_public", sa.Boolean(), "1"),
    ]
    with op.batch_alter_table("artifact") as batch_op:
        for name, column_type, default in additions:
            if name not in columns:
                batch_op.add_column(sa.Column(name, column_type, nullable=False, server_default=default))


def downgrade():
    inspector = sa.inspect(op.get_bind())
    columns = {column["name"] for column in inspector.get_columns("artifact")}
    with op.batch_alter_table("artifact") as batch_op:
        for name in ("is_public", "sort_order", "display_name", "variant", "kind"):
            if name in columns:
                batch_op.drop_column(name)
