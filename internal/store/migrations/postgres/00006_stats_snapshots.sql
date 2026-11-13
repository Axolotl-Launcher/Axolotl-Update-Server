-- +goose Up
CREATE TABLE analytics.stats_snapshots (
 key TEXT PRIMARY KEY,
 spec TEXT NOT NULL,
 payload TEXT,
 generated_at TIMESTAMPTZ,
 requested_at TIMESTAMPTZ NOT NULL,
 attempted_at TIMESTAMPTZ,
 failure TEXT
);
CREATE INDEX stats_snapshots_requested ON analytics.stats_snapshots(requested_at);
-- +goose Down
DROP TABLE analytics.stats_snapshots;
