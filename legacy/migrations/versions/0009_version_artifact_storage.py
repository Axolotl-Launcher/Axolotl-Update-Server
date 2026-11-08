"""Track metadata-only release imports without changing legacy local versions.

Revision ID: 0009_version_artifact_storage
Revises: 0008_artifact_target_platforms
"""
import sqlalchemy as sa
from alembic import op

revision = "0009_version_artifact_storage"
down_revision = "0008_artifact_target_platforms"
branch_labels = None
depends_on = None


def upgrade():
    columns = {column["name"] for column in sa.inspect(op.get_bind()).get_columns("version")}
    if "artifact_storage" not in columns:
        with op.batch_alter_table("version") as batch:
            batch.add_column(sa.Column("artifact_storage", sa.String(16), nullable=False, server_default="local"))


def downgrade():
    with op.batch_alter_table("version") as batch:
        batch.drop_column("artifact_storage")
