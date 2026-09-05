from datetime import datetime, timezone

from .extensions import db


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


class Version(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    version = db.Column(db.String(64), unique=True, nullable=False, index=True)
    channel = db.Column(db.String(16), nullable=False, index=True)
    status = db.Column(db.String(16), nullable=False, default="draft", index=True)
    notes = db.Column(db.Text, default="")
    release_tag = db.Column(db.String(128), default="")
    release_id = db.Column(db.String(128), default="")
    published_at = db.Column(db.DateTime(timezone=True))
    minimum_version = db.Column(db.String(64))
    created_at = db.Column(db.DateTime(timezone=True), default=utcnow, nullable=False)
    updated_at = db.Column(db.DateTime(timezone=True), default=utcnow, onupdate=utcnow, nullable=False)
    revoked_at = db.Column(db.DateTime(timezone=True))
    revoke_reason = db.Column(db.Text)
    force_update = db.Column(db.Boolean, nullable=False, default=False, server_default="0")
    artifacts = db.relationship("Artifact", backref="version_ref", cascade="all, delete-orphan")


class Artifact(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    version_id = db.Column(db.Integer, db.ForeignKey("version.id"), nullable=False)
    platform = db.Column(db.String(64), nullable=False)
    architecture = db.Column(db.String(32), default="")
    filename = db.Column(db.String(255), nullable=False)
    relative_path = db.Column(db.String(512), nullable=False, unique=True)
    size = db.Column(db.BigInteger, nullable=False)
    sha256 = db.Column(db.String(64), nullable=False)
    signature = db.Column(db.Text)
    signature_filename = db.Column(db.String(255))
    content_type = db.Column(db.String(128), nullable=False)
    kind = db.Column(db.String(16), nullable=False, default="updater", server_default="updater")
    variant = db.Column(db.String(32), nullable=False, default="", server_default="")
    display_name = db.Column(db.String(160), nullable=False, default="", server_default="")
    sort_order = db.Column(db.Integer, nullable=False, default=0, server_default="0")
    is_public = db.Column(db.Boolean, nullable=False, default=True, server_default="1")
    uploaded_at = db.Column(db.DateTime(timezone=True), default=utcnow, nullable=False)
    created_at = db.Column(db.DateTime(timezone=True), default=utcnow, nullable=False)
    updated_at = db.Column(db.DateTime(timezone=True), default=utcnow, onupdate=utcnow, nullable=False)


class ChannelPointer(db.Model):
    channel = db.Column(db.String(16), primary_key=True)
    current_version = db.Column(db.String(64), nullable=True)
    updated_at = db.Column(db.DateTime(timezone=True), default=utcnow, onupdate=utcnow, nullable=False)


class WebhookEvent(db.Model):
    event_id = db.Column(db.String(255), primary_key=True)
    event_type = db.Column(db.String(64), nullable=False, default="release")
    payload_hash = db.Column(db.String(64), nullable=False)
    received_at = db.Column(db.DateTime(timezone=True), default=utcnow, nullable=False)
    processed_at = db.Column(db.DateTime(timezone=True))
    status = db.Column(db.String(16), nullable=False, default="received")
    error_message = db.Column(db.Text)


class AuditLog(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    operator = db.Column(db.String(128), nullable=False)
    action = db.Column(db.String(64), nullable=False)
    channel = db.Column(db.String(16))
    version = db.Column(db.String(64))
    reason = db.Column(db.Text)
    request_id = db.Column(db.String(64))
    ip_address = db.Column(db.String(64))
    created_at = db.Column(db.DateTime(timezone=True), default=utcnow, nullable=False)


class UsageEvent(db.Model):
    id = db.Column(db.Integer, primary_key=True)
    occurred_at = db.Column(db.DateTime(timezone=True), default=utcnow, nullable=False, index=True)
    path = db.Column(db.String(512), nullable=False)
    channel = db.Column(db.String(16), nullable=False, default="unknown", index=True)
    event_type = db.Column(db.String(16), nullable=False, default="api", index=True)
    status_code = db.Column(db.Integer, nullable=False, default=200)
    bytes_sent = db.Column(db.BigInteger, nullable=False, default=0)
