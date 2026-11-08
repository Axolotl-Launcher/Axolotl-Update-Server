-- +goose Up
CREATE TABLE version (
 id INTEGER PRIMARY KEY AUTOINCREMENT, version TEXT NOT NULL UNIQUE, channel TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'draft', notes TEXT NOT NULL DEFAULT '', release_tag TEXT NOT NULL DEFAULT '',
 release_id TEXT NOT NULL DEFAULT '', published_at DATETIME, minimum_version TEXT,
 force_update BOOLEAN NOT NULL DEFAULT 0, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
 revoked_at DATETIME, revoke_reason TEXT
);
CREATE INDEX version_channel_status ON version(channel,status);
CREATE TABLE artifact (
 id INTEGER PRIMARY KEY AUTOINCREMENT, version_id INTEGER NOT NULL REFERENCES version(id) ON DELETE CASCADE,
 platform TEXT NOT NULL, target_platforms TEXT, architecture TEXT NOT NULL DEFAULT '',
 filename TEXT NOT NULL, relative_path TEXT NOT NULL UNIQUE, size BIGINT NOT NULL CHECK(size>=0), sha256 TEXT NOT NULL,
 signature TEXT, signature_filename TEXT, content_type TEXT NOT NULL,
 kind TEXT NOT NULL DEFAULT 'updater', variant TEXT NOT NULL DEFAULT '', display_name TEXT NOT NULL DEFAULT '',
 sort_order INTEGER NOT NULL DEFAULT 0, is_public BOOLEAN NOT NULL DEFAULT 1,
 uploaded_at DATETIME NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
 UNIQUE(version_id,filename)
);
CREATE INDEX artifact_version ON artifact(version_id);
CREATE TABLE channel_pointer(channel TEXT PRIMARY KEY, current_version TEXT, updated_at DATETIME NOT NULL);
CREATE TABLE webhook_event(event_id TEXT PRIMARY KEY, version TEXT, event_type TEXT NOT NULL DEFAULT 'release',
 payload_hash TEXT NOT NULL, received_at DATETIME NOT NULL, processed_at DATETIME,
 status TEXT NOT NULL, error_message TEXT);
CREATE INDEX webhook_event_version ON webhook_event(version);
CREATE TABLE audit_log(id INTEGER PRIMARY KEY AUTOINCREMENT,operator TEXT NOT NULL,action TEXT NOT NULL,
 channel TEXT,version TEXT,reason TEXT,request_id TEXT,ip_address TEXT,created_at DATETIME NOT NULL);
CREATE INDEX audit_log_created ON audit_log(created_at);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE webhook_event;
DROP TABLE channel_pointer;
DROP TABLE artifact;
DROP TABLE version;

