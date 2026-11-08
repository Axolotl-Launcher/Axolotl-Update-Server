-- +goose Up
CREATE TABLE version (
 id BIGSERIAL PRIMARY KEY, version TEXT NOT NULL UNIQUE, channel TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'draft', notes TEXT NOT NULL DEFAULT '', release_tag TEXT NOT NULL DEFAULT '',
 release_id TEXT NOT NULL DEFAULT '', published_at TIMESTAMPTZ, minimum_version TEXT,
 force_update BOOLEAN NOT NULL DEFAULT FALSE, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 revoked_at TIMESTAMPTZ, revoke_reason TEXT
);
CREATE INDEX version_channel_status ON version(channel,status);
CREATE TABLE artifact (
 id BIGSERIAL PRIMARY KEY, version_id BIGINT NOT NULL REFERENCES version(id) ON DELETE CASCADE,
 platform TEXT NOT NULL, target_platforms JSONB, architecture TEXT NOT NULL DEFAULT '',
 filename TEXT NOT NULL, relative_path TEXT NOT NULL UNIQUE, size BIGINT NOT NULL CHECK(size>=0), sha256 TEXT NOT NULL,
 signature TEXT, signature_filename TEXT, content_type TEXT NOT NULL,
 kind TEXT NOT NULL DEFAULT 'updater', variant TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
 sort_order INTEGER NOT NULL DEFAULT 0, is_public BOOLEAN NOT NULL DEFAULT TRUE,
 uploaded_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
 UNIQUE(version_id,filename)
);
CREATE INDEX artifact_version ON artifact(version_id);
CREATE TABLE channel_pointer(channel TEXT PRIMARY KEY, current_version TEXT, updated_at TIMESTAMPTZ NOT NULL);
CREATE TABLE webhook_event(event_id TEXT PRIMARY KEY, version TEXT, event_type TEXT NOT NULL DEFAULT 'release',
 payload_hash TEXT NOT NULL, received_at TIMESTAMPTZ NOT NULL, processed_at TIMESTAMPTZ,
 status TEXT NOT NULL, error_message TEXT);
CREATE INDEX webhook_event_version ON webhook_event(version);
CREATE TABLE audit_log(id BIGSERIAL PRIMARY KEY,operator TEXT NOT NULL,action TEXT NOT NULL,
 channel TEXT,version TEXT,reason TEXT,request_id TEXT,ip_address TEXT,created_at TIMESTAMPTZ NOT NULL);
CREATE INDEX audit_log_created ON audit_log(created_at);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE webhook_event;
DROP TABLE channel_pointer;
DROP TABLE artifact;
DROP TABLE version;
