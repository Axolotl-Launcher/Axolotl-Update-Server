-- +goose Up
CREATE TABLE analytics_stats_snapshots (
 key TEXT PRIMARY KEY,
 spec TEXT NOT NULL,
 payload TEXT,
 generated_at DATETIME,
 requested_at DATETIME NOT NULL,
 attempted_at DATETIME,
 failure TEXT
);
CREATE INDEX stats_snapshots_requested ON analytics_stats_snapshots(requested_at);
-- +goose Down
DROP TABLE analytics_stats_snapshots;
