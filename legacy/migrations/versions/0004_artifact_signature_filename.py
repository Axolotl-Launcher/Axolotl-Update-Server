"""add artifact signature filename

Revision ID: 0004_artifact_signature_filename
Revises: 0003_artifact_package_metadata
"""
import sqlalchemy as sa
from alembic import op

revision = "0004_artifact_signature_filename"
down_revision = "0003_artifact_package_metadata"
branch_labels = None
depends_on = None


def upgrade():
    columns = {column["name"] for column in sa.inspect(op.get_bind()).get_columns("artifact")}
    if "signature_filename" not in columns:
        with op.batch_alter_table("artifact") as batch_op:
            batch_op.add_column(sa.Column("signature_filename", sa.String(255)))


def downgrade():
    columns = {column["name"] for column in sa.inspect(op.get_bind()).get_columns("artifact")}
    if "signature_filename" in columns:
        with op.batch_alter_table("artifact") as batch_op:
            batch_op.drop_column("signature_filename")
