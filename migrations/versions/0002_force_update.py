"""add force_update to versions

Revision ID: 0002_force_update
Revises: 0001_initial
"""
import sqlalchemy as sa
from alembic import op

revision = "0002_force_update"
down_revision = "0001_initial"
branch_labels = None
depends_on = None


def upgrade():
    inspector = sa.inspect(op.get_bind())
    columns = {column["name"] for column in inspector.get_columns("version")}
    if "force_update" not in columns:
        with op.batch_alter_table("version") as batch_op:
            batch_op.add_column(sa.Column("force_update", sa.Boolean(), nullable=False, server_default="0"))


def downgrade():
    inspector = sa.inspect(op.get_bind())
    columns = {column["name"] for column in inspector.get_columns("version")}
    if "force_update" in columns:
        with op.batch_alter_table("version") as batch_op:
            batch_op.drop_column("force_update")
