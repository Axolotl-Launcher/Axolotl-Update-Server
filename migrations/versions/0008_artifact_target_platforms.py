"""add artifact target platforms

Revision ID: 0008_artifact_target_platforms
Revises: 0007_webhook_event_version
"""
import json

import sqlalchemy as sa
from alembic import op

revision = "0008_artifact_target_platforms"
down_revision = "0007_webhook_event_version"
branch_labels = None
depends_on = None


def upgrade():
    connection = op.get_bind()
    columns = {column["name"] for column in sa.inspect(connection).get_columns("artifact")}
    if "target_platforms" not in columns:
        with op.batch_alter_table("artifact") as batch:
            batch.add_column(sa.Column("target_platforms", sa.JSON(), nullable=True))
    connection.execute(
        sa.text(
            "UPDATE artifact SET target_platforms = :platforms "
            "WHERE kind = 'updater' AND platform = 'darwin-aarch64' "
            "AND architecture = 'universal' "
            "AND NOT EXISTS ("
            "SELECT 1 FROM artifact AS conflict "
            "WHERE conflict.version_id = artifact.version_id "
            "AND conflict.id <> artifact.id "
            "AND conflict.kind = 'updater' "
            "AND ("
            "conflict.platform = 'darwin-x86_64' "
            "OR ("
            "conflict.platform = 'darwin-aarch64' "
            "AND conflict.architecture = 'universal' "
            "AND conflict.id < artifact.id"
            ")"
            ")"
            ")"
        ),
        {"platforms": json.dumps(["darwin-aarch64", "darwin-x86_64"])},
    )


def downgrade():
    columns = {column["name"] for column in sa.inspect(op.get_bind()).get_columns("artifact")}
    if "target_platforms" in columns:
        with op.batch_alter_table("artifact") as batch:
            batch.drop_column("target_platforms")
